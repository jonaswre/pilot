package executor

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
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

	// Build sandbox creation request with required fields
	entrypoint := p.config.Entrypoint
	if len(entrypoint) == 0 {
		entrypoint = []string{"tail", "-f", "/dev/null"} // Keep sandbox alive
	}
	resources := opensandbox.ResourceLimits(p.config.Resources)
	if len(resources) == 0 {
		resources = opensandbox.ResourceLimits{"cpu": "1000m", "memory": "2Gi"}
	}
	createReq := opensandbox.CreateSandboxRequest{
		Image:          opensandbox.ImageSpec{URI: p.config.Image},
		Entrypoint:     entrypoint,
		ResourceLimits: resources,
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

	// Resolve execd endpoint for this sandbox via server proxy.
	// The execd service runs inside the sandbox on port 44772. Use the lifecycle
	// server's proxy endpoint so we don't need direct container network access.
	useProxy := true
	execdEndpoint, err := lc.GetEndpoint(ctx, sandboxID, opensandbox.DefaultExecdPort, &useProxy)
	if err != nil {
		cleanup()
		return nil, fmt.Errorf("opensandbox: failed to resolve execd endpoint: %w", err)
	}
	execdURL := execdEndpoint.Endpoint
	if !strings.HasPrefix(execdURL, "http") {
		execdURL = "http://" + execdURL
	}
	slog.Info("Resolved execd endpoint",
		slog.String("sandbox_id", sandboxID),
		slog.String("execd_url", execdURL),
	)

	// Extract access token from endpoint headers if present
	execdToken := p.config.APIKey
	if execdEndpoint.Headers != nil {
		if t := execdEndpoint.Headers["X-EXECD-ACCESS-TOKEN"]; t != "" {
			execdToken = t
		}
	}
	execd := opensandbox.NewExecdClient(execdURL, execdToken)

	// Configure egress policy if specified
	if p.config.Egress != nil && len(p.config.Egress.AllowedDomains) > 0 {
		egressEndpoint, err := lc.GetEndpoint(ctx, sandboxID, opensandbox.DefaultEgressPort, &useProxy)
		if err != nil {
			cleanup()
			return nil, fmt.Errorf("opensandbox: failed to resolve egress endpoint: %w", err)
		}
		egressURL := egressEndpoint.Endpoint
		if !strings.HasPrefix(egressURL, "http") {
			egressURL = "http://" + egressURL
		}
		egressToken := p.config.APIKey
		if egressEndpoint.Headers != nil {
			if t := egressEndpoint.Headers["OPENSANDBOX-EGRESS-AUTH"]; t != "" {
				egressToken = t
			}
		}
		egress := opensandbox.NewEgressClient(egressURL, egressToken)
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

// ndjsonEvent is the JSON envelope used by OpenSandbox's execd NDJSON stream.
// Stdout/stderr: {"type":"stdout","text":"...","timestamp":...}
// Error:         {"type":"error","error":{"ename":"CommandExecError","evalue":"42","traceback":[...]}}
// Complete:      {"type":"execution_complete","execution_time":13,"timestamp":...}
type ndjsonEvent struct {
	Type          string          `json:"type"`
	Text          string          `json:"text"`
	Error         *ndjsonError    `json:"error,omitempty"`
	ExitCode      *int            `json:"exit_code,omitempty"`
	ExecutionTime *int            `json:"execution_time,omitempty"`
}

// ndjsonError represents the error payload in execd error events.
type ndjsonError struct {
	Ename     string   `json:"ename"`
	Evalue    string   `json:"evalue"`    // Exit code for CommandExecError
	Traceback []string `json:"traceback"` // Human-readable error details
}

// parseNDJSONText extracts the text field from an NDJSON event envelope.
// If the data is not valid JSON or has no text field, returns data as-is.
func parseNDJSONText(data string) string {
	var ev ndjsonEvent
	if err := json.Unmarshal([]byte(data), &ev); err != nil {
		return data
	}
	return ev.Text
}

// parseNDJSONEvent parses a full NDJSON event from the data field.
func parseNDJSONEvent(data string) ndjsonEvent {
	var ev ndjsonEvent
	json.Unmarshal([]byte(data), &ev)
	return ev
}

// runSandboxCommand runs a shell command in the sandbox and waits for completion.
func runSandboxCommand(ctx context.Context, execd *opensandbox.ExecdClient, sandboxID, cmd string) error {
	var cmdErr error
	err := execd.RunCommand(ctx, opensandbox.RunCommandRequest{
		Command: cmd,
		Timeout: 120000, // 2 minutes
	}, func(e opensandbox.StreamEvent) error {
		switch e.Event {
		case "error":
			ev := parseNDJSONEvent(e.Data)
			if ev.Error != nil && ev.Error.Ename == "CommandExecError" {
				cmdErr = fmt.Errorf("command exited with code %s", ev.Error.Evalue)
			} else if ev.Error != nil {
				cmdErr = fmt.Errorf("sandbox error: %s: %s", ev.Error.Ename, ev.Error.Evalue)
			} else {
				cmdErr = fmt.Errorf("sandbox command error: %s", parseNDJSONText(e.Data))
			}
		case "execution_complete":
			ev := parseNDJSONEvent(e.Data)
			if ev.ExitCode != nil && *ev.ExitCode != 0 {
				cmdErr = fmt.Errorf("command exited with code %d", *ev.ExitCode)
			}
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

	// Build the command string (working directory handled via RunCommandRequest.Cwd)
	fullCmd := strings.Join(cmdParts, " ")

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
			Cwd:     opts.Dir,
			Envs:    opts.Env,
			Timeout: 0, // No timeout — controlled by backend watchdog
		}, func(e opensandbox.StreamEvent) error {
			// OpenSandbox execd streams NDJSON events: {"type":"stdout","text":"...","timestamp":...}
			// The SDK parses "type" → e.Event, raw JSON → e.Data.
			// We extract the "text" field for stdout/stderr to pass clean output to backends.
			switch e.Event {
			case "stdout":
				text := parseNDJSONText(e.Data)
				if _, err := stdoutPW.Write([]byte(text)); err != nil {
					return err
				}
			case "stderr":
				text := parseNDJSONText(e.Data)
				if _, err := stderrPW.Write([]byte(text)); err != nil {
					return err
				}
			case "init":
				// Init event contains session/command ID — extract PID if available
				ev := parseNDJSONEvent(e.Data)
				if ev.Text != "" {
					// init text is typically the command ID, not a PID
					slog.Debug("OpenSandbox command init", slog.String("id", ev.Text))
				}
			case "execution_complete":
				ev := parseNDJSONEvent(e.Data)
				if ev.ExitCode != nil && *ev.ExitCode != 0 {
					mu.Lock()
					exitErr = fmt.Errorf("process exited with code %d", *ev.ExitCode)
					mu.Unlock()
				}
			case "error":
				ev := parseNDJSONEvent(e.Data)
				mu.Lock()
				if ev.Error != nil && ev.Error.Ename == "CommandExecError" {
					exitErr = fmt.Errorf("process exited with code %s", ev.Error.Evalue)
				} else if ev.Error != nil {
					exitErr = fmt.Errorf("sandbox error: %s: %s", ev.Error.Ename, ev.Error.Evalue)
				} else {
					exitErr = fmt.Errorf("sandbox error: %s", parseNDJSONText(e.Data))
				}
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
