package executor

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNewCodexCLIBackend(t *testing.T) {
	tests := []struct {
		name          string
		config        *CodexCLIConfig
		expectCommand string
	}{
		{
			name:          "nil config uses defaults",
			config:        nil,
			expectCommand: "codex",
		},
		{
			name:          "empty command uses default",
			config:        &CodexCLIConfig{Command: ""},
			expectCommand: "codex",
		},
		{
			name:          "custom command",
			config:        &CodexCLIConfig{Command: "/custom/codex"},
			expectCommand: "/custom/codex",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			backend := NewCodexCLIBackend(tt.config)
			if backend == nil {
				t.Fatal("NewCodexCLIBackend returned nil")
			}
			if backend.config.Command != tt.expectCommand {
				t.Errorf("Command = %q, want %q", backend.config.Command, tt.expectCommand)
			}
		})
	}
}

func TestCodexCLIBackendName(t *testing.T) {
	backend := NewCodexCLIBackend(nil)
	if backend.Name() != BackendTypeCodexCLI {
		t.Errorf("Name() = %q, want %q", backend.Name(), BackendTypeCodexCLI)
	}
}

func TestCodexCLIBackendIsAvailable(t *testing.T) {
	backend := NewCodexCLIBackend(&CodexCLIConfig{
		Command: "/nonexistent/path/to/codex",
	})

	if backend.IsAvailable() {
		t.Error("IsAvailable() should return false for non-existent command")
	}
}

func TestCodexCLIBuildArgs(t *testing.T) {
	tests := []struct {
		name       string
		config     *CodexCLIConfig
		opts       ExecuteOptions
		expectArgs []string
		notExpect  []string
	}{
		{
			name:   "basic prompt",
			config: &CodexCLIConfig{Command: "codex"},
			opts: ExecuteOptions{
				Prompt:      "fix the bug",
				ProjectPath: "/project",
			},
			expectArgs: []string{"exec", "--cd", "/project", "--json", "--sandbox", "danger-full-access", "--dangerously-bypass-approvals-and-sandbox", "fix the bug"},
			notExpect:  []string{"--model", "--resume", "--from-pr", "--effort"},
		},
		{
			name:   "with model",
			config: &CodexCLIConfig{Command: "codex"},
			opts: ExecuteOptions{
				Prompt: "fix the bug",
				Model:  "gpt-5.1-codex-max",
			},
			expectArgs: []string{"--model", "gpt-5.1-codex-max"},
		},
		{
			name:   "with effort",
			config: &CodexCLIConfig{Command: "codex"},
			opts: ExecuteOptions{
				Prompt: "fix the bug",
				Effort: "max",
			},
			expectArgs: []string{"-c", "model_reasoning_effort=\"xhigh\""},
			notExpect:  []string{"--effort"},
		},
		{
			name:   "with resume",
			config: &CodexCLIConfig{Command: "codex", UseSessionResume: true},
			opts: ExecuteOptions{
				Prompt:          "continue",
				ProjectPath:     "/project",
				ResumeSessionID: "thread-abc",
			},
			expectArgs: []string{"exec", "--cd", "/project", "--json", "resume", "thread-abc", "continue"},
		},
		{
			name:   "resume disabled in config",
			config: &CodexCLIConfig{Command: "codex", UseSessionResume: false},
			opts: ExecuteOptions{
				Prompt:          "continue",
				ResumeSessionID: "thread-abc",
			},
			notExpect: []string{"resume", "thread-abc"},
		},
		{
			name:   "from-pr silently ignored",
			config: &CodexCLIConfig{Command: "codex"},
			opts: ExecuteOptions{
				Prompt: "fix CI",
				FromPR: 42,
			},
			notExpect: []string{"--from-pr", "42"},
		},
		{
			name:   "extra args appended before prompt",
			config: &CodexCLIConfig{Command: "codex", ExtraArgs: []string{"--ephemeral"}},
			opts: ExecuteOptions{
				Prompt: "test",
			},
			expectArgs: []string{"--ephemeral", "test"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			backend := NewCodexCLIBackend(tt.config)
			args := backend.buildArgs(tt.opts)

			for _, expected := range tt.expectArgs {
				found := false
				for _, arg := range args {
					if arg == expected {
						found = true
						break
					}
				}
				if !found {
					t.Errorf("args missing %q, got %v", expected, args)
				}
			}

			for _, notExpected := range tt.notExpect {
				for _, arg := range args {
					if arg == notExpected {
						t.Errorf("args should not contain %q, got %v", notExpected, args)
						break
					}
				}
			}

			if tt.name == "with resume" {
				resumeIndex := indexOfArg(args, "resume")
				jsonIndex := indexOfArg(args, "--json")
				promptIndex := indexOfArg(args, "continue")
				if resumeIndex == -1 || jsonIndex == -1 || promptIndex == -1 {
					t.Fatalf("resume args missing expected entries: %v", args)
				}
				if jsonIndex > resumeIndex {
					t.Errorf("--json must appear before resume, got %v", args)
				}
				if promptIndex < resumeIndex {
					t.Errorf("prompt must appear after resume session id, got %v", args)
				}
			}
		})
	}
}

func indexOfArg(args []string, target string) int {
	for i, arg := range args {
		if arg == target {
			return i
		}
	}
	return -1
}

func TestCodexCLIBackendParseStreamEvent(t *testing.T) {
	backend := NewCodexCLIBackend(nil)

	tests := []struct {
		name           string
		line           string
		expectType     BackendEventType
		expectTool     string
		expectError    bool
		expectSession  string
		expectMessage  string
		expectTokensIn int64
	}{
		{
			name:          "thread started",
			line:          `{"type":"thread.started","thread_id":"thread-123"}`,
			expectType:    EventTypeInit,
			expectSession: "thread-123",
		},
		{
			name:       "command execution started maps to Bash tool use",
			line:       `{"type":"item.started","item":{"id":"item_1","type":"command_execution","command":"go test ./...","status":"in_progress"}}`,
			expectType: EventTypeToolUse,
			expectTool: "Bash",
		},
		{
			name:          "agent message",
			line:          `{"type":"item.completed","item":{"id":"item_2","type":"agent_message","text":"Implemented the fix."}}`,
			expectType:    EventTypeText,
			expectMessage: "Implemented the fix.",
		},
		{
			name:           "turn completed",
			line:           `{"type":"turn.completed","usage":{"input_tokens":100,"cached_input_tokens":40,"output_tokens":25},"model":"gpt-5.1-codex-max"}`,
			expectType:     EventTypeResult,
			expectTokensIn: 100,
		},
		{
			name:          "error event",
			line:          `{"type":"error","message":"rate limit exceeded"}`,
			expectType:    EventTypeError,
			expectError:   true,
			expectMessage: "rate limit exceeded",
		},
		{
			name:       "invalid json",
			line:       `not valid json`,
			expectType: EventTypeText,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			event := backend.parseStreamEvent(tt.line)

			if event.Type != tt.expectType {
				t.Errorf("Type = %q, want %q", event.Type, tt.expectType)
			}
			if tt.expectTool != "" && event.ToolName != tt.expectTool {
				t.Errorf("ToolName = %q, want %q", event.ToolName, tt.expectTool)
			}
			if tt.expectError && !event.IsError {
				t.Error("IsError should be true")
			}
			if tt.expectSession != "" && event.SessionID != tt.expectSession {
				t.Errorf("SessionID = %q, want %q", event.SessionID, tt.expectSession)
			}
			if tt.expectMessage != "" && event.Message != tt.expectMessage {
				t.Errorf("Message = %q, want %q", event.Message, tt.expectMessage)
			}
			if tt.expectTokensIn > 0 && event.TokensInput != tt.expectTokensIn {
				t.Errorf("TokensInput = %d, want %d", event.TokensInput, tt.expectTokensIn)
			}
			if event.Raw != tt.line {
				t.Errorf("Raw = %q, want %q", event.Raw, tt.line)
			}
		})
	}
}

func TestClassifyCodexCLIError(t *testing.T) {
	tests := []struct {
		name       string
		stderr     string
		expectType CodexCLIErrorType
	}{
		{name: "rate limit", stderr: "Error: rate limit exceeded", expectType: CodexErrorTypeRateLimit},
		{name: "api error", stderr: "HTTP 401 unauthorized", expectType: CodexErrorTypeAPIError},
		{name: "invalid config", stderr: "error: unknown option --bad", expectType: CodexErrorTypeInvalidConfig},
		{name: "session not found", stderr: "No conversation found for id", expectType: CodexErrorTypeSessionNotFound},
		{name: "timeout", stderr: "signal: killed", expectType: CodexErrorTypeTimeout},
		{name: "unknown", stderr: "something else", expectType: CodexErrorTypeUnknown},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := classifyCodexCLIError(tt.stderr, nil)
			if err.Type != tt.expectType {
				t.Errorf("classifyCodexCLIError() type = %q, want %q", err.Type, tt.expectType)
			}
			if tt.stderr != "" && !strings.Contains(err.Stderr, tt.stderr) {
				t.Errorf("classifyCodexCLIError() stderr = %q, want to contain %q", err.Stderr, tt.stderr)
			}
		})
	}
}

func TestCodexCLIExecuteWithFakeCommand(t *testing.T) {
	tmpDir := t.TempDir()
	argsPath := filepath.Join(tmpDir, "args.txt")
	scriptPath := filepath.Join(tmpDir, "fake-codex")
	script := `#!/bin/sh
printf '%s\n' "$*" > "` + argsPath + `"
printf '%s\n' '{"type":"thread.started","thread_id":"thread-123"}'
printf '%s\n' '{"type":"item.completed","item":{"id":"item_1","type":"agent_message","text":"Implemented the fix."}}'
printf '%s\n' '{"type":"turn.completed","usage":{"input_tokens":100,"cached_input_tokens":40,"output_tokens":25},"model":"gpt-5.1-codex-max"}'
`
	if err := os.WriteFile(scriptPath, []byte(script), 0755); err != nil {
		t.Fatalf("write fake codex: %v", err)
	}

	backend := NewCodexCLIBackend(&CodexCLIConfig{Command: scriptPath})
	result, err := backend.Execute(context.Background(), ExecuteOptions{
		Prompt:      "fix the bug",
		ProjectPath: tmpDir,
		Model:       "gpt-5.1-codex-max",
		Effort:      "max",
	})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}

	if !result.Success {
		t.Fatal("result.Success = false, want true")
	}
	if result.Output != "Implemented the fix." {
		t.Errorf("Output = %q, want Implemented the fix.", result.Output)
	}
	if result.SessionID != "thread-123" {
		t.Errorf("SessionID = %q, want thread-123", result.SessionID)
	}
	if result.TokensInput != 100 {
		t.Errorf("TokensInput = %d, want 100", result.TokensInput)
	}
	if result.CacheReadInputTokens != 40 {
		t.Errorf("CacheReadInputTokens = %d, want 40", result.CacheReadInputTokens)
	}
	if result.TokensOutput != 25 {
		t.Errorf("TokensOutput = %d, want 25", result.TokensOutput)
	}
	if result.Model != "gpt-5.1-codex-max" {
		t.Errorf("Model = %q, want gpt-5.1-codex-max", result.Model)
	}

	argsBytes, err := os.ReadFile(argsPath)
	if err != nil {
		t.Fatalf("read args: %v", err)
	}
	args := string(argsBytes)
	for _, expected := range []string{
		"exec",
		"--cd " + tmpDir,
		"--json",
		"--sandbox danger-full-access",
		"--dangerously-bypass-approvals-and-sandbox",
		"--model gpt-5.1-codex-max",
		"-c model_reasoning_effort=\"xhigh\"",
		"fix the bug",
	} {
		if !strings.Contains(args, expected) {
			t.Errorf("captured args missing %q: %s", expected, args)
		}
	}
}
