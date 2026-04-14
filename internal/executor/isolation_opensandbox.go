package executor

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	opensandbox "github.com/alibaba/OpenSandbox/sdks/sandbox/go"
)

// OpenSandboxIsolationProvider creates sandboxed execution environments using
// OpenSandbox (https://open-sandbox.ai/). Sandboxes are isolated containers
// with configurable runtimes (Docker, Kubernetes, gVisor, Kata, Firecracker).
//
// Lifecycle: CreateSandbox → git clone repo → configure egress → return runner → DeleteSandbox
type OpenSandboxIsolationProvider struct {
	config *OpenSandboxIsolationConfig
}

// NewOpenSandboxIsolationProvider creates a new OpenSandbox isolation provider.
func NewOpenSandboxIsolationProvider(config *OpenSandboxIsolationConfig) *OpenSandboxIsolationProvider {
	if config == nil {
		config = DefaultOpenSandboxConfig()
	}
	if config.Image == "" {
		config.Image = "pilot/executor:latest"
	}
	if config.Timeout == 0 {
		config.Timeout = 30 * time.Minute
	}
	return &OpenSandboxIsolationProvider{config: config}
}

// Name returns "opensandbox".
func (p *OpenSandboxIsolationProvider) Name() string { return IsolationTypeOpenSandbox }

// IsAvailable checks if the OpenSandbox server is configured.
func (p *OpenSandboxIsolationProvider) IsAvailable() bool {
	return p.config != nil && p.config.ServerURL != ""
}

// Prepare creates an OpenSandbox container, clones the repository, and returns
// an environment configured with an OpenSandboxCommandRunner.
func (p *OpenSandboxIsolationProvider) Prepare(ctx context.Context, opts IsolationOpts) (*IsolatedEnvironment, error) {
	if p.config.ServerURL == "" {
		return nil, fmt.Errorf("opensandbox: server_url not configured")
	}

	slog.Info("Creating OpenSandbox isolation",
		slog.String("task_id", opts.TaskID),
		slog.String("server", p.config.ServerURL),
		slog.String("image", p.config.Image),
	)

	// Create lifecycle client
	lc := opensandbox.NewLifecycleClient(p.config.ServerURL, p.config.APIKey)

	// Build sandbox creation request
	createReq := opensandbox.CreateSandboxRequest{
		Image: opensandbox.ImageSpec{URI: p.config.Image},
	}
	if p.config.Entrypoint != nil {
		createReq.Entrypoint = p.config.Entrypoint
	}
	if p.config.Resources != nil {
		createReq.ResourceLimits = opensandbox.ResourceLimits(p.config.Resources)
	}
	// Inject environment variables
	if len(p.config.EnvVars) > 0 {
		createReq.Env = p.config.EnvVars
	}

	// Create the sandbox
	sbx, err := lc.CreateSandbox(ctx, createReq)
	if err != nil {
		return nil, fmt.Errorf("opensandbox: failed to create sandbox: %w", err)
	}

	sandboxID := sbx.ID
	slog.Info("OpenSandbox created",
		slog.String("sandbox_id", sandboxID),
		slog.String("task_id", opts.TaskID),
	)

	// Build idempotent cleanup function (safe to call multiple times)
	var cleanupOnce sync.Once
	cleanup := func() {
		cleanupOnce.Do(func() {
			cleanupCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			if err := lc.DeleteSandbox(cleanupCtx, sandboxID); err != nil {
				slog.Warn("Failed to delete OpenSandbox",
					slog.String("sandbox_id", sandboxID),
					slog.Any("error", err),
				)
			} else {
				slog.Info("OpenSandbox deleted",
					slog.String("sandbox_id", sandboxID),
				)
			}
		})
	}

	// Create execd client for the sandbox
	execd := opensandbox.NewExecdClient(p.config.ServerURL, p.config.APIKey)

	// Configure egress policy if specified
	if p.config.Egress != nil && len(p.config.Egress.AllowedDomains) > 0 {
		egress := opensandbox.NewEgressClient(p.config.ServerURL, p.config.APIKey)
		rules := make([]opensandbox.NetworkRule, 0, len(p.config.Egress.AllowedDomains))
		for _, domain := range p.config.Egress.AllowedDomains {
			rules = append(rules, opensandbox.NetworkRule{
				Action: "allow",
				Target: domain,
			})
		}
		if _, err := egress.PatchPolicy(ctx, rules); err != nil {
			cleanup()
			return nil, fmt.Errorf("opensandbox: failed to configure egress: %w", err)
		}
	}

	// Clone repository inside sandbox
	if opts.ProjectPath != "" {
		slog.Info("Cloning repository in sandbox",
			slog.String("project", opts.ProjectPath),
			slog.String("branch", opts.Branch),
		)

		// Get remote URL from project path
		cloneURL, err := getGitRemoteURL(ctx, opts.ProjectPath)
		if err != nil {
			cleanup()
			return nil, fmt.Errorf("opensandbox: failed to get git remote URL: %w", err)
		}

		// Clone into /workspace (shell-escape URL to prevent injection)
		cloneCmd := fmt.Sprintf("git clone --depth 50 %s /workspace", shellQuote(cloneURL))
		if err := runSandboxCommand(ctx, execd, sandboxID, cloneCmd); err != nil {
			cleanup()
			return nil, fmt.Errorf("opensandbox: failed to clone repo: %w", err)
		}

		// Create and checkout branch if specified (shell-escape branch names)
		if opts.Branch != "" {
			branchCmd := fmt.Sprintf("cd /workspace && git checkout -B %s", shellQuote(opts.Branch))
			if opts.BaseBranch != "" {
				branchCmd = fmt.Sprintf("cd /workspace && git fetch origin %s && git checkout -B %s origin/%s",
					shellQuote(opts.BaseBranch), shellQuote(opts.Branch), shellQuote(opts.BaseBranch))
			}
			if err := runSandboxCommand(ctx, execd, sandboxID, branchCmd); err != nil {
				cleanup()
				return nil, fmt.Errorf("opensandbox: failed to create branch: %w", err)
			}
		}
	}

	cmdRunner := &OpenSandboxCommandRunner{
		execd:     execd,
		sandboxID: sandboxID,
	}

	return &IsolatedEnvironment{
		WorkDir:       "/workspace",
		CommandRunner: cmdRunner,
		Cleanup:       cleanup,
	}, nil
}

// shellQuote wraps a string in single quotes for safe shell interpolation.
// Internal single quotes are escaped as '\'' (end quote, escaped quote, start quote).
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'"
}

// getGitRemoteURL extracts the git remote URL from a local repository path.
func getGitRemoteURL(ctx context.Context, repoPath string) (string, error) {
	runner := &LocalCommandRunner{}
	running, err := runner.Run(ctx, CommandRunOpts{
		Command: "git",
		Args:    []string{"remote", "get-url", "origin"},
		Dir:     repoPath,
	})
	if err != nil {
		return "", err
	}
	var buf bytes.Buffer
	io.Copy(&buf, running.Stdout)
	running.Wait()
	url := strings.TrimSpace(buf.String())
	if url == "" {
		return "", fmt.Errorf("no remote URL found for %s", repoPath)
	}
	return url, nil
}

// runSandboxCommand runs a shell command in the sandbox and waits for completion.
func runSandboxCommand(ctx context.Context, execd *opensandbox.ExecdClient, sandboxID, cmd string) error {
	var cmdErr error
	err := execd.RunCommand(ctx, opensandbox.RunCommandRequest{
		Command: cmd,
		Timeout: 120000, // 2 minutes
	}, func(e opensandbox.StreamEvent) error {
		if e.Event == "error" {
			cmdErr = fmt.Errorf("sandbox command error: %s", e.Data)
		}
		return nil
	})
	if err != nil {
		return err
	}
	return cmdErr
}

// OpenSandboxCommandRunner executes commands inside an OpenSandbox container
// via the ExecdClient API. Bridges SSE streaming events to io.ReadCloser
// for compatibility with existing backend stdout/stderr scanning.
type OpenSandboxCommandRunner struct {
	execd     *opensandbox.ExecdClient
	sandboxID string
}

// Run executes a command inside the OpenSandbox container.
// Bridges the SSE event stream from ExecdClient.RunCommand into io.ReadCloser
// pipes that backends can scan like local subprocesses.
func (r *OpenSandboxCommandRunner) Run(ctx context.Context, opts CommandRunOpts) (*RunningCommand, error) {
	// Build the full command string (shell-escape each arg to prevent injection)
	var cmdParts []string
	cmdParts = append(cmdParts, shellQuote(opts.Command))
	for _, arg := range opts.Args {
		cmdParts = append(cmdParts, shellQuote(arg))
	}

	// Set working directory if specified
	fullCmd := strings.Join(cmdParts, " ")
	if opts.Dir != "" {
		fullCmd = fmt.Sprintf("cd %s && %s", shellQuote(opts.Dir), fullCmd)
	}

	// Prepend environment variables (shell-escape values)
	if len(opts.Env) > 0 {
		var envParts []string
		for k, v := range opts.Env {
			envParts = append(envParts, fmt.Sprintf("%s=%s", k, shellQuote(v)))
		}
		fullCmd = strings.Join(envParts, " ") + " " + fullCmd
	}

	// Create pipes for stdout and stderr
	stdoutPR, stdoutPW := io.Pipe()
	stderrPR, stderrPW := io.Pipe()

	// Derived context for Kill support — cancelling this stops the sandbox command
	runCtx, runCancel := context.WithCancel(ctx)

	// Track command completion and exit state
	done := make(chan struct{})
	var mu sync.Mutex
	var exitErr error
	var pid atomic.Int32

	// Run command with SSE streaming in background
	go func() {
		defer close(done)
		defer stdoutPW.Close()
		defer stderrPW.Close()

		// Note: RunCommand callbacks are invoked synchronously from the SSE reading
		// goroutine, so writes to exitErr within the callback are sequenced with
		// the post-return assignment. The channel close provides happens-before
		// for the Wait reader.
		err := r.execd.RunCommand(runCtx, opensandbox.RunCommandRequest{
			Command: fullCmd,
			Timeout: 0, // No timeout — controlled by backend watchdog
		}, func(e opensandbox.StreamEvent) error {
			switch e.Event {
			case "stdout":
				if _, err := stdoutPW.Write([]byte(e.Data)); err != nil {
					return err
				}
			case "stderr":
				if _, err := stderrPW.Write([]byte(e.Data)); err != nil {
					return err
				}
			case "pid":
				if v, err := strconv.Atoi(strings.TrimSpace(e.Data)); err == nil {
					pid.Store(int32(v))
				}
			case "exit":
				if e.Data != "0" && e.Data != "" {
					mu.Lock()
					exitErr = fmt.Errorf("process exited with code %s", e.Data)
					mu.Unlock()
				}
			case "error":
				mu.Lock()
				exitErr = fmt.Errorf("sandbox error: %s", e.Data)
				mu.Unlock()
			}
			return nil
		})
		if err != nil {
			mu.Lock()
			if exitErr == nil {
				exitErr = err
			}
			mu.Unlock()
		}
	}()

	return &RunningCommand{
		Stdout: stdoutPR,
		Stderr: stderrPR,
		Wait: func() error {
			<-done
			mu.Lock()
			defer mu.Unlock()
			return exitErr
		},
		Kill: func() error {
			// Cancel the derived context, which terminates ExecdClient.RunCommand
			runCancel()
			return nil
		},
		PID: int(pid.Load()),
	}, nil
}
