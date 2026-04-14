package telegram

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/qf-studio/pilot/internal/adapters"
	"github.com/qf-studio/pilot/internal/testutil"
)

// TestNewNotifier tests notifier creation
func TestNewNotifier(t *testing.T) {
	config := &Config{
		BotToken: testutil.FakeTelegramBotToken,
		ChatID:   "123456",
	}

	notifier := NewNotifier(config)

	if notifier == nil {
		t.Fatal("NewNotifier returned nil")
	}
	if notifier.chatID != "123456" {
		t.Errorf("chatID = %q, want %q", notifier.chatID, "123456")
	}
	if notifier.client == nil {
		t.Error("client is nil")
	}
}

// TestDefaultConfig tests the default config values
func TestDefaultConfig(t *testing.T) {
	config := DefaultConfig()

	if config == nil {
		t.Fatal("DefaultConfig returned nil")
	}
	if config.Enabled {
		t.Error("Enabled should be false by default")
	}
}

// TestGenerateProgressBar tests progress bar generation via shared util
func TestGenerateProgressBar(t *testing.T) {
	tests := []struct {
		progress int
		expected string
	}{
		{0, "░░░░░░░░░░"},
		{10, "█░░░░░░░░░"},
		{50, "█████░░░░░"},
		{100, "██████████"},
	}

	for _, tt := range tests {
		t.Run("", func(t *testing.T) {
			got := adapters.GenerateProgressBar(tt.progress, 10)
			if got != tt.expected {
				t.Errorf("GenerateProgressBar(%d, 10) = %q, want %q", tt.progress, got, tt.expected)
			}
		})
	}
}

// newTestNotifier creates a notifier pointing at a test server.
func newTestNotifier(t *testing.T, server *httptest.Server, plainText bool) *Notifier {
	t.Helper()
	return &Notifier{
		client:        NewClientWithBaseURL(testutil.FakeTelegramBotToken, server.URL),
		chatID:        "123456",
		plainTextMode: plainText,
	}
}

// createMockServer creates a test server that returns successful responses
func createMockServer(t *testing.T, validateBody func(map[string]interface{})) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if ct := r.Header.Get("Content-Type"); ct != "application/json" {
			t.Errorf("Content-Type = %q, want application/json", ct)
		}

		if validateBody != nil {
			var body map[string]interface{}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Errorf("failed to parse request body: %v", err)
			}
			validateBody(body)
		}

		response := SendMessageResponse{
			OK:     true,
			Result: &Result{MessageID: 123},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(response)
	}))
}

// TestNotifierSendMessage tests the SendMessage method
func TestNotifierSendMessage(t *testing.T) {
	tests := []struct {
		name string
		text string
	}{
		{name: "simple message", text: "Hello, world!"},
		{name: "empty message", text: ""},
		{name: "message with special chars", text: "Test *bold* _italic_ `code`"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := createMockServer(t, func(body map[string]interface{}) {
				if text, ok := body["text"].(string); ok && text != tt.text {
					t.Errorf("text = %q, want %q", text, tt.text)
				}
				if body["chat_id"] != "123456" {
					t.Errorf("chat_id = %v, want 123456", body["chat_id"])
				}
			})
			defer server.Close()

			notifier := newTestNotifier(t, server, true)
			if err := notifier.SendMessage(context.Background(), tt.text); err != nil {
				t.Errorf("SendMessage() error = %v", err)
			}
		})
	}
}

// TestNotifierTaskStarted tests the TaskStarted method
func TestNotifierTaskStarted(t *testing.T) {
	tests := []struct {
		name      string
		taskID    string
		title     string
		plainText bool
		wantParts []string
	}{
		{
			name:      "plain text mode",
			taskID:    "TASK-01",
			title:     "Create auth handler",
			plainText: true,
			wantParts: []string{"TASK-01", "Create auth handler", "Pilot started task"},
		},
		{
			name:      "markdown mode",
			taskID:    "TG-123",
			title:     "Fix bug",
			plainText: false,
			wantParts: []string{"TG-123", "Fix bug", "*Pilot started task*"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := createMockServer(t, func(body map[string]interface{}) {
				text := body["text"].(string)
				for _, part := range tt.wantParts {
					if !strings.Contains(text, part) {
						t.Errorf("text %q missing %q", text, part)
					}
				}
			})
			defer server.Close()

			notifier := newTestNotifier(t, server, tt.plainText)
			if err := notifier.TaskStarted(context.Background(), tt.taskID, tt.title); err != nil {
				t.Errorf("TaskStarted() error = %v", err)
			}
		})
	}
}

// TestNotifierTaskCompleted tests the TaskCompleted method
func TestNotifierTaskCompleted(t *testing.T) {
	tests := []struct {
		name      string
		taskID    string
		title     string
		prURL     string
		wantParts []string
	}{
		{
			name:      "without PR",
			taskID:    "TASK-01",
			title:     "Complete task",
			prURL:     "",
			wantParts: []string{"TASK-01", "Complete task", "Pilot completed task"},
		},
		{
			name:      "with PR",
			taskID:    "TASK-02",
			title:     "Task with PR",
			prURL:     "https://github.com/org/repo/pull/123",
			wantParts: []string{"TASK-02", "Task with PR", "github.com"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := createMockServer(t, func(body map[string]interface{}) {
				text := body["text"].(string)
				for _, part := range tt.wantParts {
					if !strings.Contains(text, part) {
						t.Errorf("text %q missing %q", text, part)
					}
				}
			})
			defer server.Close()

			notifier := newTestNotifier(t, server, true)
			if err := notifier.TaskCompleted(context.Background(), tt.taskID, tt.title, tt.prURL); err != nil {
				t.Errorf("TaskCompleted() error = %v", err)
			}
		})
	}
}

// TestNotifierTaskFailed tests the TaskFailed method
func TestNotifierTaskFailed(t *testing.T) {
	tests := []struct {
		name     string
		taskID   string
		title    string
		errorMsg string
	}{
		{
			name:     "simple error",
			taskID:   "TASK-01",
			title:    "Failed task",
			errorMsg: "Build failed",
		},
		{
			name:     "multiline error",
			taskID:   "TASK-02",
			title:    "Another failure",
			errorMsg: "Error on line 1\nError on line 2",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := createMockServer(t, func(body map[string]interface{}) {
				text := body["text"].(string)
				if !strings.Contains(text, tt.taskID) {
					t.Errorf("text missing taskID %q", tt.taskID)
				}
				if !strings.Contains(text, tt.errorMsg) {
					t.Errorf("text missing errorMsg %q", tt.errorMsg)
				}
			})
			defer server.Close()

			notifier := newTestNotifier(t, server, true)
			if err := notifier.TaskFailed(context.Background(), tt.taskID, tt.title, tt.errorMsg); err != nil {
				t.Errorf("TaskFailed() error = %v", err)
			}
		})
	}
}

// TestNotifierTaskProgress tests the TaskProgress method
func TestNotifierTaskProgress(t *testing.T) {
	tests := []struct {
		name     string
		taskID   string
		status   string
		progress int
	}{
		{name: "0% progress", taskID: "TASK-01", status: "Starting", progress: 0},
		{name: "50% progress", taskID: "TASK-02", status: "Implementing", progress: 50},
		{name: "100% progress", taskID: "TASK-03", status: "Complete", progress: 100},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := createMockServer(t, func(body map[string]interface{}) {
				text := body["text"].(string)
				if !strings.Contains(text, tt.taskID) {
					t.Errorf("text missing taskID %q", tt.taskID)
				}
			})
			defer server.Close()

			notifier := newTestNotifier(t, server, true)
			if err := notifier.TaskProgress(context.Background(), tt.taskID, tt.status, tt.progress); err != nil {
				t.Errorf("TaskProgress() error = %v", err)
			}
		})
	}
}

// TestNotifierPRReady tests the PRReady method
func TestNotifierPRReady(t *testing.T) {
	tests := []struct {
		name         string
		taskID       string
		title        string
		prURL        string
		filesChanged int
	}{
		{
			name:         "single file",
			taskID:       "TASK-01",
			title:        "Add feature",
			prURL:        "https://github.com/org/repo/pull/1",
			filesChanged: 1,
		},
		{
			name:         "multiple files",
			taskID:       "TASK-02",
			title:        "Large refactor",
			prURL:        "https://github.com/org/repo/pull/2",
			filesChanged: 15,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := createMockServer(t, func(body map[string]interface{}) {
				text := body["text"].(string)
				if !strings.Contains(text, tt.taskID) {
					t.Errorf("text missing taskID %q", tt.taskID)
				}
				if !strings.Contains(text, tt.prURL) {
					t.Errorf("text missing prURL %q", tt.prURL)
				}
			})
			defer server.Close()

			notifier := newTestNotifier(t, server, true)
			if err := notifier.PRReady(context.Background(), tt.taskID, tt.title, tt.prURL, tt.filesChanged); err != nil {
				t.Errorf("PRReady() error = %v", err)
			}
		})
	}
}

// TestConfigFields tests that config has expected fields
func TestConfigFields(t *testing.T) {
	config := &Config{
		Enabled:    true,
		BotToken:   "token",
		ChatID:     "chat",
		Polling:    true,
		AllowedIDs: []int64{123, 456},
	}

	if !config.Enabled {
		t.Error("Enabled should be true")
	}
	if config.BotToken != "token" {
		t.Errorf("BotToken = %q, want %q", config.BotToken, "token")
	}
	if config.ChatID != "chat" {
		t.Errorf("ChatID = %q, want %q", config.ChatID, "chat")
	}
	if !config.Polling {
		t.Error("Polling should be true")
	}
	if len(config.AllowedIDs) != 2 {
		t.Errorf("AllowedIDs len = %d, want 2", len(config.AllowedIDs))
	}
}

// TestConfigValidate tests the Validate method
func TestConfigValidate(t *testing.T) {
	tests := []struct {
		name    string
		config  *Config
		wantErr bool
	}{
		{
			name:    "valid config",
			config:  &Config{BotToken: "token", ChatID: "123"},
			wantErr: false,
		},
		{
			name:    "missing bot token",
			config:  &Config{BotToken: "", ChatID: "123"},
			wantErr: true,
		},
		{
			name:    "missing chat ID",
			config:  &Config{BotToken: "token", ChatID: ""},
			wantErr: true,
		},
		{
			name:    "both missing",
			config:  &Config{},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.config.Validate()
			if (err != nil) != tt.wantErr {
				t.Errorf("Validate() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

// TestEscapeMarkdownNotifier tests markdown escaping for notifications
func TestEscapeMarkdownNotifier(t *testing.T) {
	tests := []struct {
		input   string
		wantEsc []string
	}{
		{input: "hello_world", wantEsc: []string{"\\_"}},
		{input: "*bold* text", wantEsc: []string{"\\*"}},
		{input: "`code`", wantEsc: []string{"\\`"}},
		{input: "[link]", wantEsc: []string{"\\["}},
		{input: "plain text", wantEsc: []string{}},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := escapeMarkdown(tt.input)
			for _, esc := range tt.wantEsc {
				if !strings.Contains(got, esc) {
					t.Errorf("escapeMarkdown(%q) = %q, want to contain %q", tt.input, got, esc)
				}
			}
		})
	}
}

// TestEscapeMarkdownDoesNotOverEscape verifies non-special chars pass through
func TestEscapeMarkdownDoesNotOverEscape(t *testing.T) {
	// These chars should NOT be escaped in legacy Markdown mode
	input := "v2.0.1 - release! (final) #123 > done"
	got := escapeMarkdown(input)

	unwanted := []string{"\\.", "\\!", "\\(", "\\)", "\\#", "\\>", "\\-"}
	for _, esc := range unwanted {
		if strings.Contains(got, esc) {
			t.Errorf("escapeMarkdown(%q) = %q, should NOT contain %q in legacy mode", input, got, esc)
		}
	}
}

// TestDefaultConfigPlainTextMode tests that PlainTextMode defaults to true
func TestDefaultConfigPlainTextMode(t *testing.T) {
	config := DefaultConfig()

	if !config.PlainTextMode {
		t.Error("PlainTextMode should default to true for better messaging app compatibility")
	}
}

// TestNotifierGetParseMode tests the getParseMode helper
func TestNotifierGetParseMode(t *testing.T) {
	tests := []struct {
		name          string
		plainTextMode bool
		want          string
	}{
		{
			name:          "plain text mode enabled",
			plainTextMode: true,
			want:          "",
		},
		{
			name:          "plain text mode disabled (markdown)",
			plainTextMode: false,
			want:          "Markdown",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			notifier := &Notifier{plainTextMode: tt.plainTextMode}
			got := notifier.getParseMode()
			if got != tt.want {
				t.Errorf("getParseMode() = %q, want %q", got, tt.want)
			}
		})
	}
}

// TestNotifierPlainTextModeConfig tests that notifier correctly inherits PlainTextMode from config
func TestNotifierPlainTextModeConfig(t *testing.T) {
	tests := []struct {
		name          string
		plainTextMode bool
	}{
		{name: "plain text enabled", plainTextMode: true},
		{name: "plain text disabled", plainTextMode: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			config := &Config{
				BotToken:      testutil.FakeTelegramBotToken,
				ChatID:        "123456",
				PlainTextMode: tt.plainTextMode,
			}

			notifier := NewNotifier(config)

			if notifier.plainTextMode != tt.plainTextMode {
				t.Errorf("notifier.plainTextMode = %v, want %v", notifier.plainTextMode, tt.plainTextMode)
			}
		})
	}
}

// TestNotifierServerError tests handling of API errors
func TestNotifierServerError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		response := SendMessageResponse{
			OK:          false,
			ErrorCode:   400,
			Description: "Bad Request: chat not found",
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(response)
	}))
	defer server.Close()

	notifier := newTestNotifier(t, server, true)
	err := notifier.SendMessage(context.Background(), "test")
	if err == nil {
		t.Error("expected error for failed API call")
	}
	if !strings.Contains(err.Error(), "chat not found") {
		t.Errorf("error = %q, want to contain 'chat not found'", err)
	}
}
