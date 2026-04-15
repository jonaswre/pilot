package executor

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
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

	// Resolve which image to use (per-project override > auto-built > global default)
	resolvedImage := resolveExecutorImage(ctx, p.config, opts)

	slog.Info("Creating OpenSandbox isolation",
		slog.String("task_id", opts.TaskID),
		slog.String("server", p.config.ServerURL),
		slog.String("image", resolvedImage),
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
		Image:          opensandbox.ImageSpec{URI: resolvedImage},
		Entrypoint:     entrypoint,
		ResourceLimits: resources,
	}
	// Inject environment variables
	if len(p.config.EnvVars) > 0 {
		createReq.Env = p.config.EnvVars
	}
	// NOTE: networkPolicy disabled for debugging — re-enable once fetch works
	// if p.config.Egress != nil && len(p.config.Egress.AllowedDomains) > 0 { ... }

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

	// Wait for execd to become ready (it's bootstrapped after container start).
	if err := waitForExecd(ctx, execd, sandboxID); err != nil {
		cleanup()
		return nil, fmt.Errorf("opensandbox: execd not ready: %w", err)
	}

	// Set up repository inside sandbox.
	// Two paths: if /workspace/.git exists (pre-baked image), fetch+checkout.
	// Otherwise, full clone from remote.
	if opts.ProjectPath != "" {
		// Get git remote URL and optionally embed GITHUB_TOKEN for private repos.
		remoteURL, err := getGitRemoteURL(ctx, opts.ProjectPath)
		if err != nil {
			cleanup()
			return nil, fmt.Errorf("opensandbox: failed to get git remote URL: %w", err)
		}
		if token := p.config.EnvVars["GITHUB_TOKEN"]; token != "" && strings.Contains(remoteURL, "github.com") {
			remoteURL = strings.Replace(remoteURL, "https://", fmt.Sprintf("https://x-token:%s@", token), 1)
		}

		// Check if repo is pre-baked in the image
		preBaked := false
		if err := runSandboxCommand(ctx, execd, sandboxID, "test -d /workspace/.git"); err == nil {
			preBaked = true
		}

		if preBaked {
			// Fast path: repo pre-baked, just fetch latest and checkout branch
			slog.Info("Pre-baked repo detected, using fast fetch+checkout",
				slog.String("task_id", opts.TaskID),
			)
			_ = runSandboxCommand(ctx, execd, sandboxID, "git config --global --add safe.directory /workspace")
			fetchCmd := fmt.Sprintf("cd /workspace && git fetch %s", shellQuote(remoteURL))
			if err := runSandboxCommand(ctx, execd, sandboxID, fetchCmd); err != nil {
				cleanup()
				return nil, fmt.Errorf("opensandbox: failed to fetch in pre-baked repo: %w", err)
			}
			// Set the origin remote URL to the authenticated URL so push works later.
			setURLCmd := fmt.Sprintf("cd /workspace && git remote set-url origin %s", shellQuote(remoteURL))
			_ = runSandboxCommand(ctx, execd, sandboxID, setURLCmd)
			if opts.Branch != "" {
				// Always start the new branch from the latest remote main (FETCH_HEAD)
				// so the PR doesn't conflict with work merged while the image was stale.
				// BaseBranch is used when explicitly set; otherwise default to origin/main.
				base := "FETCH_HEAD"
				if opts.BaseBranch != "" {
					base = "origin/" + opts.BaseBranch
				}
				branchCmd := fmt.Sprintf("cd /workspace && git checkout -B %s %s",
					shellQuote(opts.Branch), base)
				if err := runSandboxCommand(ctx, execd, sandboxID, branchCmd); err != nil {
					cleanup()
					return nil, fmt.Errorf("opensandbox: failed to checkout branch in pre-baked repo: %w", err)
				}
			}
		} else {
			// Full clone path (generic image without pre-baked repo)
			slog.Info("Cloning repository in sandbox",
				slog.String("project", opts.ProjectPath),
				slog.String("branch", opts.Branch),
			)

			_ = runSandboxCommand(ctx, execd, sandboxID, "git config --global --add safe.directory /workspace")
			cloneCmd := fmt.Sprintf("git clone --depth 50 %s /workspace", shellQuote(remoteURL))
			if err := runSandboxCommand(ctx, execd, sandboxID, cloneCmd); err != nil {
				cleanup()
				return nil, fmt.Errorf("opensandbox: failed to clone repo: %w", err)
			}
			// Set the origin remote URL to the authenticated URL so push works later.
			setURLCmd := fmt.Sprintf("cd /workspace && git remote set-url origin %s", shellQuote(remoteURL))
			_ = runSandboxCommand(ctx, execd, sandboxID, setURLCmd)

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
	}

	cmdRunner := &OpenSandboxCommandRunner{
		execd:     execd,
		sandboxID: sandboxID,
	}

	// Upload Navigator (.agent/) to sandbox if it exists in the project.
	// Always set NavigatorUploaded=true so the runner skips host-side os.MkdirAll
	// (which would target /workspace on the host, not inside the container).
	// If the upload fails, the sandbox runs without Navigator docs — non-fatal.
	if opts.ProjectPath != "" {
		if err := uploadNavigatorToSandbox(ctx, execd, opts.ProjectPath); err != nil {
			slog.Warn("Failed to upload Navigator to sandbox (continuing without it)",
				slog.String("task_id", opts.TaskID),
				slog.Any("error", err),
			)
		}
	}

	return &IsolatedEnvironment{
		WorkDir:           "/workspace",
		CommandRunner:     cmdRunner,
		Cleanup:           cleanup,
		NavigatorUploaded: true, // always skip host-side copy; sandbox handles /workspace internally
	}, nil
}

// uploadNavigatorToSandbox uploads the .agent/ directory from the source repo
// into /workspace/.agent inside the sandbox using the execd file API.
func uploadNavigatorToSandbox(ctx context.Context, execd *opensandbox.ExecdClient, sourceRepo string) error {
	sourceAgent := filepath.Join(sourceRepo, ".agent")
	info, err := os.Stat(sourceAgent)
	if err != nil {
		if os.IsNotExist(err) {
			return nil // nothing to upload
		}
		return err
	}
	if !info.IsDir() {
		return nil
	}

	// Create the destination directory in the sandbox.
	// OctalMode converts os.FileMode(0755) → 755 (octal-digits-as-decimal) as the API expects.
	if err := execd.CreateDirectory(ctx, "/workspace/.agent", opensandbox.OctalMode(0o755)); err != nil {
		return fmt.Errorf("create /workspace/.agent: %w", err)
	}

	return uploadDirToSandbox(ctx, execd, sourceAgent, "/workspace/.agent")
}

// uploadDirToSandbox recursively uploads a local directory to a sandbox path.
func uploadDirToSandbox(ctx context.Context, execd *opensandbox.ExecdClient, localDir, remoteDir string) error {
	entries, err := os.ReadDir(localDir)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		localPath := filepath.Join(localDir, entry.Name())
		remotePath := remoteDir + "/" + entry.Name()
		if entry.IsDir() {
			if err := execd.CreateDirectory(ctx, remotePath, opensandbox.OctalMode(0o755)); err != nil {
				return fmt.Errorf("create dir %s: %w", remotePath, err)
			}
			if err := uploadDirToSandbox(ctx, execd, localPath, remotePath); err != nil {
				return err
			}
		} else {
			f, err := os.Open(localPath)
			if err != nil {
				return err
			}
			uploadErr := execd.UploadFile(ctx, f, opensandbox.UploadFileOptions{
				Metadata: opensandbox.FileMetadata{Path: remotePath},
			})
			f.Close()
			if uploadErr != nil {
				return fmt.Errorf("upload %s: %w", remotePath, uploadErr)
			}
		}
	}
	return nil
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

// waitForExecd polls the execd endpoint until it accepts commands or the context expires.
// Execd is bootstrapped after container start and may not be immediately ready.
func waitForExecd(ctx context.Context, execd *opensandbox.ExecdClient, sandboxID string) error {
	deadline := time.Now().Add(30 * time.Second)
	for {
		err := execd.RunCommand(ctx, opensandbox.RunCommandRequest{
			Command: "true",
		}, func(_ opensandbox.StreamEvent) error { return nil })
		if err == nil {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("execd not ready after 30s (sandbox=%s): %w", sandboxID, err)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(2 * time.Second):
		}
	}
}

// runSandboxCommand runs a shell command in the sandbox and waits for completion.
func runSandboxCommand(ctx context.Context, execd *opensandbox.ExecdClient, sandboxID, cmd string) error {
	var cmdErr error
	var stderr strings.Builder
	err := execd.RunCommand(ctx, opensandbox.RunCommandRequest{
		Command: cmd,
		Timeout: 120000, // 2 minutes
	}, func(e opensandbox.StreamEvent) error {
		slog.Info("sandbox event", slog.String("type", e.Event), slog.String("data", e.Data))
		switch e.Event {
		case "stdout", "stderr":
			line := parseNDJSONText(e.Data)
			if line != "" {
				stderr.WriteString(line)
				stderr.WriteByte('\n')
			}
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
				output := strings.TrimSpace(stderr.String())
				if output != "" {
					cmdErr = fmt.Errorf("command exited with code %d: %s", *ev.ExitCode, output)
				} else {
					cmdErr = fmt.Errorf("command exited with code %d", *ev.ExitCode)
				}
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
			//
			// IMPORTANT: execd strips the trailing newline from each stdout line before
			// encoding it into the "text" JSON field. Without a trailing '\n' the backend's
			// bufio.Scanner never completes a line, so the heartbeat timestamp never updates
			// and the watchdog kills the process after its timeout regardless of actual
			// output. Always ensure each stdout write ends with '\n'.
			switch e.Event {
			case "stdout":
				text := parseNDJSONText(e.Data)
				if !strings.HasSuffix(text, "\n") {
					text += "\n"
				}
				if _, err := stdoutPW.Write([]byte(text)); err != nil {
					return err
				}
			case "stderr":
				text := parseNDJSONText(e.Data)
				if !strings.HasSuffix(text, "\n") {
					text += "\n"
				}
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
