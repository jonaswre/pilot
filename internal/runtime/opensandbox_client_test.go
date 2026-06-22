package runtime

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestOpenSandboxClientCreatesPollsAndDeletesSandbox(t *testing.T) {
	var getCount atomic.Int32
	var sawAPIKey atomic.Bool

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("OPEN-SANDBOX-API-KEY") == "test-key" {
			sawAPIKey.Store(true)
		}
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1/sandboxes":
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Errorf("decode create body: %v", err)
			}
			image := body["image"].(map[string]any)
			if image["uri"] != "alpine:3.20" {
				t.Errorf("image.uri = %#v", image["uri"])
			}
			w.WriteHeader(http.StatusAccepted)
			_, _ = w.Write([]byte(`{"id":"sbx_123","status":{"state":"Pending"},"createdAt":"2026-06-22T00:00:00Z","entrypoint":["tail","-f","/dev/null"]}`))
		case r.Method == http.MethodGet && r.URL.Path == "/v1/sandboxes/sbx_123":
			count := getCount.Add(1)
			state := "Pending"
			if count > 1 {
				state = "Running"
			}
			_, _ = w.Write([]byte(`{"id":"sbx_123","status":{"state":"` + state + `"},"createdAt":"2026-06-22T00:00:00Z","entrypoint":["tail","-f","/dev/null"]}`))
		case r.Method == http.MethodDelete && r.URL.Path == "/v1/sandboxes/sbx_123":
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	client := NewOpenSandboxClient(server.URL, "test-key", server.Client())

	sandbox, err := client.CreateSandbox(t.Context(), map[string]any{
		"image":      map[string]any{"uri": "alpine:3.20"},
		"entrypoint": []string{"tail", "-f", "/dev/null"},
	})
	if err != nil {
		t.Fatalf("CreateSandbox returned error: %v", err)
	}
	if sandbox.ID != "sbx_123" {
		t.Fatalf("sandbox ID = %q, want sbx_123", sandbox.ID)
	}

	running, err := client.WaitForRunning(t.Context(), "sbx_123", 100*time.Millisecond)
	if err != nil {
		t.Fatalf("WaitForRunning returned error: %v", err)
	}
	if running.Status.State != SandboxStateRunning {
		t.Fatalf("state = %q, want Running", running.Status.State)
	}

	if err := client.DeleteSandbox(t.Context(), "sbx_123"); err != nil {
		t.Fatalf("DeleteSandbox returned error: %v", err)
	}
	if !sawAPIKey.Load() {
		t.Fatal("client did not send OPEN-SANDBOX-API-KEY")
	}
}

func TestOpenSandboxClientGetsSandboxEndpoint(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/v1/sandboxes/sbx_123/endpoints/44772" {
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		if r.URL.Query().Get("use_server_proxy") != "true" {
			t.Fatalf("use_server_proxy = %q, want true", r.URL.Query().Get("use_server_proxy"))
		}
		_, _ = w.Write([]byte(`{"endpoint":"https://execd.example/sbx_123","headers":{"X-Sandbox":"sbx_123"}}`))
	}))
	defer server.Close()

	client := NewOpenSandboxClient(server.URL, "test-key", server.Client())
	endpoint, err := client.GetEndpoint(t.Context(), "sbx_123", DefaultExecdPort)
	if err != nil {
		t.Fatalf("GetEndpoint returned error: %v", err)
	}
	if endpoint.Endpoint != "https://execd.example/sbx_123" {
		t.Fatalf("endpoint = %q", endpoint.Endpoint)
	}
	if endpoint.Headers["X-Sandbox"] != "sbx_123" {
		t.Fatalf("headers = %#v", endpoint.Headers)
	}
}

func TestOpenSandboxClientRunCommandParsesSSE(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/command" {
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode command body: %v", err)
		}
		if body["command"] != "echo hi" {
			t.Fatalf("command = %#v", body["command"])
		}
		if body["cwd"] != "/workspace" {
			t.Fatalf("cwd = %#v", body["cwd"])
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(strings.Join([]string{
			`data: {"type":"stdout","text":"hello\n"}`,
			``,
			`data: {"type":"stderr","text":"warn\n"}`,
			``,
			`data: {"type":"result","exit_code":0}`,
			``,
			`data: {"type":"execution_complete"}`,
			``,
		}, "\n")))
	}))
	defer server.Close()

	client := NewOpenSandboxClient("http://lifecycle.invalid", "", server.Client())
	result, err := client.RunCommand(t.Context(), server.URL, nil, CommandSpec{
		Command: "echo hi",
		CWD:     "/workspace",
		Timeout: 30 * time.Second,
		Env:     map[string]string{"CI": "true"},
	})
	if err != nil {
		t.Fatalf("RunCommand returned error: %v", err)
	}
	if result.Stdout != "hello\n" {
		t.Fatalf("stdout = %q", result.Stdout)
	}
	if result.Stderr != "warn\n" {
		t.Fatalf("stderr = %q", result.Stderr)
	}
	if result.ExitCode != 0 || !result.Success {
		t.Fatalf("exit=%d success=%v", result.ExitCode, result.Success)
	}
}
