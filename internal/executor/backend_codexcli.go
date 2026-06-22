package executor

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/qf-studio/pilot/internal/logging"
)

// CodexCLIErrorType categorizes different types of Codex CLI failures.
type CodexCLIErrorType string

const (
	CodexErrorTypeRateLimit       CodexCLIErrorType = "rate_limit"
	CodexErrorTypeAPIError        CodexCLIErrorType = "api_error"
	CodexErrorTypeTimeout         CodexCLIErrorType = "timeout"
	CodexErrorTypeInvalidConfig   CodexCLIErrorType = "invalid_config"
	CodexErrorTypeSessionNotFound CodexCLIErrorType = "session_not_found"
	CodexErrorTypeOOM             CodexCLIErrorType = "oom_killed"
	CodexErrorTypeUnknown         CodexCLIErrorType = "unknown"
)

// CodexCLIError represents a classified error from Codex CLI.
type CodexCLIError struct {
	Type    CodexCLIErrorType
	Message string
	Stderr  string
}

func (e *CodexCLIError) Error() string {
	if e.Stderr != "" {
		return fmt.Sprintf("%s: %s (stderr: %s)", e.Type, e.Message, e.Stderr)
	}
	return fmt.Sprintf("%s: %s", e.Type, e.Message)
}

// ErrorType implements BackendError.
func (e *CodexCLIError) ErrorType() string { return string(e.Type) }

// ErrorMessage implements BackendError.
func (e *CodexCLIError) ErrorMessage() string { return e.Message }

// ErrorStderr implements BackendError.
func (e *CodexCLIError) ErrorStderr() string { return e.Stderr }

func classifyCodexCLIError(stderr string, originalErr error) *CodexCLIError {
	if exitCode := extractCodexExitCode(originalErr); exitCode == 137 || exitCode == 139 {
		sigName := "SIGKILL"
		if exitCode == 139 {
			sigName = "SIGSEGV"
		}
		return &CodexCLIError{
			Type:    CodexErrorTypeOOM,
			Message: fmt.Sprintf("Process killed by %s (exit code %d)", sigName, exitCode),
			Stderr:  strings.TrimSpace(stderr),
		}
	}

	stderrLower := strings.ToLower(stderr)

	if strings.Contains(stderrLower, "rate limit") ||
		strings.Contains(stderrLower, "too many requests") ||
		strings.Contains(stderrLower, "429") ||
		strings.Contains(stderrLower, "hit your limit") {
		return &CodexCLIError{
			Type:    CodexErrorTypeRateLimit,
			Message: "Codex CLI rate limit reached",
			Stderr:  strings.TrimSpace(stderr),
		}
	}

	if strings.Contains(stderrLower, "unknown option") ||
		strings.Contains(stderrLower, "unrecognized") ||
		strings.Contains(stderrLower, "invalid model") ||
		strings.Contains(stderrLower, "invalid config") ||
		strings.Contains(stderrLower, "invalid value") {
		return &CodexCLIError{
			Type:    CodexErrorTypeInvalidConfig,
			Message: "Invalid Codex CLI configuration",
			Stderr:  strings.TrimSpace(stderr),
		}
	}

	if strings.Contains(stderrLower, "authentication") ||
		strings.Contains(stderrLower, "unauthorized") ||
		strings.Contains(stderrLower, "api key") ||
		strings.Contains(stderrLower, "403") ||
		strings.Contains(stderrLower, "401") {
		return &CodexCLIError{
			Type:    CodexErrorTypeAPIError,
			Message: "Codex API error",
			Stderr:  strings.TrimSpace(stderr),
		}
	}

	if strings.Contains(stderrLower, "session not found") ||
		strings.Contains(stderrLower, "no session") ||
		strings.Contains(stderrLower, "no conversation found") ||
		strings.Contains(stderrLower, "thread not found") ||
		strings.Contains(stderrLower, "invalid session") {
		return &CodexCLIError{
			Type:    CodexErrorTypeSessionNotFound,
			Message: "Codex session not found for resume",
			Stderr:  strings.TrimSpace(stderr),
		}
	}

	if strings.Contains(stderrLower, "killed") ||
		strings.Contains(stderrLower, "signal") ||
		strings.Contains(stderrLower, "timeout") {
		return &CodexCLIError{
			Type:    CodexErrorTypeTimeout,
			Message: "Process killed or timed out",
			Stderr:  strings.TrimSpace(stderr),
		}
	}

	msg := "Unknown error"
	if originalErr != nil {
		msg = originalErr.Error()
	}
	return &CodexCLIError{
		Type:    CodexErrorTypeUnknown,
		Message: msg,
		Stderr:  strings.TrimSpace(stderr),
	}
}

func extractCodexExitCode(err error) int {
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		return -1
	}
	if ws, ok := exitErr.Sys().(syscall.WaitStatus); ok {
		if ws.Signaled() {
			return 128 + int(ws.Signal())
		}
	}
	return exitErr.ExitCode()
}

// CodexCLIBackend implements Backend for OpenAI Codex CLI.
type CodexCLIBackend struct {
	config           *CodexCLIConfig
	heartbeatTimeout time.Duration
	log              *slog.Logger
	subprocessLimits *SubprocessLimitsConfig
}

// NewCodexCLIBackend creates a new Codex CLI backend.
func NewCodexCLIBackend(config *CodexCLIConfig) *CodexCLIBackend {
	if config == nil {
		config = &CodexCLIConfig{Command: "codex"}
	}
	if config.Command == "" {
		config.Command = "codex"
	}
	return &CodexCLIBackend{
		config:           config,
		heartbeatTimeout: DefaultHeartbeatTimeout,
		log:              logging.WithComponent("executor.codexcli"),
	}
}

// SetHeartbeatTimeout sets a custom heartbeat timeout for this backend.
func (b *CodexCLIBackend) SetHeartbeatTimeout(d time.Duration) {
	b.heartbeatTimeout = d
}

// SetSubprocessLimits configures RSS telemetry and optional memory cap.
func (b *CodexCLIBackend) SetSubprocessLimits(cfg *SubprocessLimitsConfig) {
	b.subprocessLimits = cfg
}

// Name returns the backend identifier.
func (b *CodexCLIBackend) Name() string {
	return BackendTypeCodexCLI
}

// IsAvailable checks if Codex CLI is installed.
func (b *CodexCLIBackend) IsAvailable() bool {
	path, err := exec.LookPath(b.config.Command)
	if err != nil {
		return false
	}
	out, err := exec.Command(path, "--version").Output()
	if err != nil {
		b.log.Warn("codex-cli: could not determine version", "error", err)
		return true
	}
	version := strings.TrimSpace(string(out))
	b.log.Info("codex-cli: detected version", "version", version)
	return true
}

func (b *CodexCLIBackend) buildArgs(opts ExecuteOptions) []string {
	args := []string{"exec"}
	args = append(args,
		"--cd", opts.ProjectPath,
		"--json",
		"--sandbox", "danger-full-access",
		"--dangerously-bypass-approvals-and-sandbox",
	)

	if opts.Model != "" {
		args = append(args, "--model", opts.Model)
		b.log.Info("Using routed model", slog.String("model", opts.Model))
	}

	if opts.Effort != "" {
		args = append(args, "-c", fmt.Sprintf("model_reasoning_effort=%q", mapCodexReasoningEffort(opts.Effort)))
		b.log.Info("Using routed effort", slog.String("effort", opts.Effort))
	}

	args = append(args, b.config.ExtraArgs...)
	if opts.ResumeSessionID != "" && b.config.UseSessionResume {
		args = append(args, "resume", opts.ResumeSessionID)
		b.log.Info("Resuming Codex CLI session", slog.String("session_id", opts.ResumeSessionID))
	}
	args = append(args, opts.Prompt)
	return args
}

func mapCodexReasoningEffort(effort string) string {
	switch strings.ToLower(strings.TrimSpace(effort)) {
	case "minimal", "low", "medium", "high", "xhigh":
		return strings.ToLower(strings.TrimSpace(effort))
	case "max":
		return "xhigh"
	default:
		return effort
	}
}

// Execute runs a prompt through Codex CLI.
func (b *CodexCLIBackend) Execute(ctx context.Context, opts ExecuteOptions) (*BackendResult, error) {
	result, err := b.execute(ctx, opts)
	if err != nil && opts.ResumeSessionID != "" {
		if codexErr, ok := err.(*CodexCLIError); ok && codexErr.Type == CodexErrorTypeSessionNotFound {
			b.log.Warn("codex-cli: session not found, retrying without resume",
				slog.String("session_id", opts.ResumeSessionID),
				slog.String("error", codexErr.Message),
			)
			opts.ResumeSessionID = ""
			return b.execute(ctx, opts)
		}
	}
	return result, err
}

func (b *CodexCLIBackend) execute(ctx context.Context, opts ExecuteOptions) (*BackendResult, error) {
	args := b.buildArgs(opts)

	cmd := exec.CommandContext(ctx, b.config.Command, args...)
	cmd.Dir = opts.ProjectPath
	cmd.Env = append(os.Environ(), "PILOT_EXECUTOR=1")

	b.log.Debug("Starting Codex CLI",
		slog.String("command", b.config.Command),
		slog.String("project", opts.ProjectPath),
	)

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("failed to create stdout pipe: %w", err)
	}

	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, fmt.Errorf("failed to create stderr pipe: %w", err)
	}

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("failed to start Codex CLI: %w", err)
	}
	b.log.Debug("Codex CLI started", slog.Int("pid", cmd.Process.Pid))

	applyResourceLimits(cmd.Process.Pid, b.subprocessLimits)

	sampleInterval := 10 * time.Second
	if b.subprocessLimits != nil && b.subprocessLimits.SampleIntervalSec > 0 {
		sampleInterval = time.Duration(b.subprocessLimits.SampleIntervalSec) * time.Second
	}
	rssSamplerCtx, cancelRSSSampler := context.WithCancel(context.Background())
	rssCh := StartRSSSampler(rssSamplerCtx, cmd.Process.Pid, sampleInterval)

	result := &BackendResult{}
	stderrOutput := newBoundedBuffer(MaxStderrBufferBytes)
	var wg sync.WaitGroup

	cmdDone := make(chan struct{})
	var lastEventAt atomic.Int64
	lastEventAt.Store(time.Now().UnixNano())

	heartbeatCtx, cancelHeartbeat := context.WithCancel(context.Background())
	defer cancelHeartbeat()
	logging.SafeGo("executor-backend-codexcli", func() {
		ticker := time.NewTicker(HeartbeatCheckInterval)
		defer ticker.Stop()
		for {
			select {
			case <-heartbeatCtx.Done():
				return
			case <-cmdDone:
				return
			case <-ticker.C:
				lastNano := lastEventAt.Load()
				lastTime := time.Unix(0, lastNano)
				age := time.Since(lastTime)
				if age > b.heartbeatTimeout {
					b.log.Warn("Heartbeat timeout detected, killing hung process",
						slog.Int("pid", cmd.Process.Pid),
						slog.Duration("last_event_age", age),
						slog.Duration("timeout", b.heartbeatTimeout),
					)
					if opts.HeartbeatCallback != nil {
						opts.HeartbeatCallback(cmd.Process.Pid, age)
					}
					if cmd.Process != nil {
						if err := cmd.Process.Kill(); err != nil {
							b.log.Error("Failed to kill hung process", slog.Int("pid", cmd.Process.Pid), slog.Any("error", err))
						}
					}
					return
				}
			}
		}
	})

	if opts.WatchdogTimeout > 0 {
		logging.SafeGo("executor-backend-codexcli", func() {
			select {
			case <-cmdDone:
				return
			case <-time.After(opts.WatchdogTimeout):
				if cmd.Process == nil {
					return
				}
				b.log.Warn("Watchdog timeout expired, forcibly killing subprocess",
					slog.Int("pid", cmd.Process.Pid),
					slog.Duration("watchdog_timeout", opts.WatchdogTimeout),
				)
				if opts.WatchdogCallback != nil {
					opts.WatchdogCallback(cmd.Process.Pid, opts.WatchdogTimeout)
				}
				if err := cmd.Process.Kill(); err != nil {
					b.log.Error("Watchdog failed to kill process", slog.Int("pid", cmd.Process.Pid), slog.Any("error", err))
				}
			}
		})
	}

	wg.Add(1)
	logging.SafeGo("executor-backend-codexcli", func() {
		defer wg.Done()
		scanner := bufio.NewScanner(stdout)
		buf := make([]byte, 0, 64*1024)
		scanner.Buffer(buf, 1024*1024)

		for scanner.Scan() {
			line := scanner.Text()
			lastEventAt.Store(time.Now().UnixNano())

			if opts.Verbose {
				fmt.Printf("   %s\n", line)
			}

			event := b.parseStreamEvent(line)
			if opts.EventHandler != nil {
				opts.EventHandler(event)
			}

			switch event.Type {
			case EventTypeInit:
				if event.SessionID != "" {
					result.SessionID = event.SessionID
				}
			case EventTypeText:
				if event.Message != "" {
					result.LastAssistantText = event.Message
				}
			case EventTypeResult:
				cancelHeartbeat()
				if event.IsError {
					result.Error = event.Message
				} else {
					if event.Message != "" {
						result.Output = event.Message
					} else if result.LastAssistantText != "" {
						result.Output = result.LastAssistantText
					}
					result.SawSuccessResult = true
				}
			case EventTypeError:
				result.Error = event.Message
			}

			result.TokensInput += event.TokensInput
			result.TokensOutput += event.TokensOutput
			result.CacheReadInputTokens += event.CacheReadInputTokens
			if event.Model != "" {
				result.Model = event.Model
			}
		}
	})

	wg.Add(1)
	logging.SafeGo("executor-backend-codexcli", func() {
		defer wg.Done()
		scanner := bufio.NewScanner(stderr)
		for scanner.Scan() {
			line := scanner.Text()
			stderrOutput.WriteLine(line)
			if opts.Verbose {
				fmt.Printf("   [err] %s\n", line)
			}
		}
	})

	logging.SafeGo("executor-backend-codexcli", func() {
		select {
		case <-cmdDone:
			return
		case <-ctx.Done():
			if cmd.Process == nil {
				return
			}
			b.log.Warn("Context cancelled, waiting grace period before hard kill",
				slog.Int("pid", cmd.Process.Pid),
				slog.Duration("grace_period", GracePeriod),
			)
			select {
			case <-cmdDone:
				return
			case <-time.After(GracePeriod):
				if cmd.Process != nil {
					if err := cmd.Process.Kill(); err != nil {
						b.log.Error("Failed to kill process", slog.Int("pid", cmd.Process.Pid), slog.Any("error", err))
					}
				}
			}
		}
	})

	wg.Wait()
	err = cmd.Wait()
	close(cmdDone)

	cancelRSSSampler()
	if rssSample, ok := <-rssCh; ok {
		result.PeakRSSMB = rssSample.PeakMB
		result.FinalRSSMB = rssSample.FinalMB
	}

	result.Stderr = stderrOutput.String()
	if err != nil {
		if result.SawSuccessResult {
			result.Success = true
			return result, nil
		}

		result.Success = false
		codexErr := classifyCodexCLIError(result.Stderr, err)
		result.ErrorType = string(codexErr.Type)
		if result.Error == "" {
			result.Error = codexErr.Error()
		}

		b.log.Warn("Codex CLI execution failed",
			slog.String("error_type", string(codexErr.Type)),
			slog.String("message", codexErr.Message),
			slog.String("stderr", codexErr.Stderr),
		)
		return result, codexErr
	}

	if result.Output == "" && result.LastAssistantText != "" {
		result.Output = result.LastAssistantText
	}
	result.Success = true
	return result, nil
}

type codexStreamEvent struct {
	Type     string           `json:"type"`
	ThreadID string           `json:"thread_id,omitempty"`
	Item     *codexStreamItem `json:"item,omitempty"`
	Usage    *codexUsageInfo  `json:"usage,omitempty"`
	Model    string           `json:"model,omitempty"`
	Message  string           `json:"message,omitempty"`
	Error    string           `json:"error,omitempty"`
}

type codexStreamItem struct {
	ID      string `json:"id,omitempty"`
	Type    string `json:"type,omitempty"`
	Command string `json:"command,omitempty"`
	Status  string `json:"status,omitempty"`
	Text    string `json:"text,omitempty"`
}

type codexUsageInfo struct {
	InputTokens       int64 `json:"input_tokens"`
	CachedInputTokens int64 `json:"cached_input_tokens,omitempty"`
	OutputTokens      int64 `json:"output_tokens"`
}

func (b *CodexCLIBackend) parseStreamEvent(line string) BackendEvent {
	event := BackendEvent{Raw: line}

	var streamEvent codexStreamEvent
	if err := json.Unmarshal([]byte(line), &streamEvent); err != nil {
		event.Type = EventTypeText
		event.Message = line
		return event
	}

	switch streamEvent.Type {
	case "thread.started":
		event.Type = EventTypeInit
		event.SessionID = streamEvent.ThreadID
		event.Message = "Codex CLI initialized"
	case "item.started":
		event = b.parseCodexItemEvent(event, streamEvent.Item, true)
	case "item.completed":
		event = b.parseCodexItemEvent(event, streamEvent.Item, false)
	case "turn.completed":
		event.Type = EventTypeResult
	case "turn.failed":
		event.Type = EventTypeError
		event.IsError = true
		event.Message = firstNonEmpty(streamEvent.Message, streamEvent.Error, "Codex CLI turn failed")
	case "error":
		event.Type = EventTypeError
		event.IsError = true
		event.Message = firstNonEmpty(streamEvent.Message, streamEvent.Error, "Codex CLI error")
	default:
		event.Type = EventTypeProgress
		event.Message = streamEvent.Type
	}

	if streamEvent.Usage != nil {
		event.TokensInput = streamEvent.Usage.InputTokens
		event.CacheReadInputTokens = streamEvent.Usage.CachedInputTokens
		event.TokensOutput = streamEvent.Usage.OutputTokens
	}
	if streamEvent.Model != "" {
		event.Model = streamEvent.Model
	}

	return event
}

func (b *CodexCLIBackend) parseCodexItemEvent(event BackendEvent, item *codexStreamItem, started bool) BackendEvent {
	if item == nil {
		event.Type = EventTypeProgress
		return event
	}

	switch item.Type {
	case "command_execution":
		if started {
			event.Type = EventTypeToolUse
			event.ToolName = "Bash"
			event.ToolInput = map[string]interface{}{"command": item.Command}
			event.Message = "Using Bash"
		} else {
			event.Type = EventTypeToolResult
			event.ToolResult = item.Status
			event.IsError = item.Status == "failed"
		}
	case "agent_message":
		event.Type = EventTypeText
		event.Message = item.Text
	default:
		event.Type = EventTypeProgress
		event.Message = item.Type
	}

	return event
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}
