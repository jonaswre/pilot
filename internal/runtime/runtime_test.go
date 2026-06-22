package runtime

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type fakeSandboxClient struct {
	createdPayload map[string]any
	commands       []CommandSpec
	deleteCalls    []string
	commandResults []fakeCommandResult
}

type fakeCommandResult struct {
	result *CommandResult
	err    error
}

func (f *fakeSandboxClient) CreateSandbox(ctx context.Context, payload map[string]any) (*Sandbox, error) {
	f.createdPayload = payload
	return &Sandbox{ID: "sbx_test", Status: SandboxStatus{State: SandboxStatePending}}, nil
}

func (f *fakeSandboxClient) WaitForRunning(ctx context.Context, sandboxID string, pollInterval time.Duration) (*Sandbox, error) {
	return &Sandbox{ID: sandboxID, Status: SandboxStatus{State: SandboxStateRunning}}, nil
}

func (f *fakeSandboxClient) DeleteSandbox(ctx context.Context, sandboxID string) error {
	f.deleteCalls = append(f.deleteCalls, sandboxID)
	return nil
}

func (f *fakeSandboxClient) GetEndpoint(ctx context.Context, sandboxID string, port int) (*SandboxEndpoint, error) {
	return &SandboxEndpoint{
		Endpoint: "https://execd.example/" + sandboxID,
		Headers:  map[string]string{"X-Sandbox": sandboxID},
	}, nil
}

func (f *fakeSandboxClient) RunCommand(ctx context.Context, execdEndpoint string, headers map[string]string, spec CommandSpec) (*CommandResult, error) {
	f.commands = append(f.commands, spec)
	if len(f.commandResults) == 0 {
		return &CommandResult{ExitCode: 0, Success: true}, nil
	}
	next := f.commandResults[0]
	f.commandResults = f.commandResults[1:]
	return next.result, next.err
}

func TestRuntimeHostProviderUsesLocalWorkspace(t *testing.T) {
	provider := NewHostProvider()
	prepared, err := provider.Prepare(t.Context(), ExecutionRequest{
		TaskID:      "GH-1",
		ProjectPath: "/repo",
		Branch:      "pilot/GH-1",
	})
	if err != nil {
		t.Fatalf("Prepare returned error: %v", err)
	}
	if prepared.WorkspacePath != "/repo" {
		t.Fatalf("WorkspacePath = %q, want /repo", prepared.WorkspacePath)
	}
	if prepared.SandboxID != "" {
		t.Fatalf("SandboxID = %q, want empty", prepared.SandboxID)
	}
}

func TestRuntimeOpenSandboxPrepareCreatesSandboxAndRunsSetup(t *testing.T) {
	repo := writeEnvironmentContract(t, `
version: 1
sandbox:
  image:
    uri: alpine:3.20
  entrypoint: ["tail", "-f", "/dev/null"]
  metadata:
    project: demo
pilot:
  workspace:
    path: /workspace
  checkout:
    strategy: clone
    depth: 1
  setup:
    - go mod download
  verify:
    - make test
  secrets:
    required:
      - GITHUB_TOKEN
`)
	t.Setenv("TEST_GITHUB_TOKEN", "gh-secret")
	fake := &fakeSandboxClient{}
	provider := NewOpenSandboxProvider(&Config{
		Provider: ProviderOpenSandbox,
		OpenSandbox: OpenSandboxConfig{
			Endpoint: "http://opensandbox.local:8080",
			Secrets: map[string]SecretMapping{
				"GITHUB_TOKEN": {FromEnv: "TEST_GITHUB_TOKEN"},
			},
			Cleanup: CleanupConfig{AlwaysDelete: true},
		},
	}, fake)

	prepared, err := provider.Prepare(t.Context(), ExecutionRequest{
		TaskID:      "GH-1",
		ProjectPath: repo,
		RepoURL:     "https://github.com/acme/demo.git",
		Branch:      "pilot/GH-1",
		BaseBranch:  "main",
	})
	if err != nil {
		t.Fatalf("Prepare returned error: %v", err)
	}

	if prepared.WorkspacePath != "/workspace" {
		t.Fatalf("WorkspacePath = %q, want /workspace", prepared.WorkspacePath)
	}
	if prepared.SandboxID != "sbx_test" {
		t.Fatalf("SandboxID = %q, want sbx_test", prepared.SandboxID)
	}
	env := fake.createdPayload["env"].(map[string]any)
	if env["PILOT_BRANCH"] != "pilot/GH-1" {
		t.Fatalf("PILOT_BRANCH = %#v", env["PILOT_BRANCH"])
	}
	if env["GITHUB_TOKEN"] != "gh-secret" {
		t.Fatalf("GITHUB_TOKEN not injected")
	}
	metadata := fake.createdPayload["metadata"].(map[string]any)
	if metadata["pilot_task_id"] != "GH-1" {
		t.Fatalf("metadata = %#v", metadata)
	}
	if len(fake.commands) != 3 {
		t.Fatalf("commands = %#v, want clone checkout setup", fake.commands)
	}
	if !strings.Contains(fake.commands[0].Command, "git clone --depth 1") {
		t.Fatalf("clone command = %q", fake.commands[0].Command)
	}
	if fake.commands[2].Command != "go mod download" {
		t.Fatalf("setup command = %q", fake.commands[2].Command)
	}

	if err := prepared.RunVerify(t.Context()); err != nil {
		t.Fatalf("RunVerify returned error: %v", err)
	}
	if fake.commands[len(fake.commands)-1].Command != "make test" {
		t.Fatalf("verify command = %q", fake.commands[len(fake.commands)-1].Command)
	}
	if err := prepared.Cleanup(t.Context(), true); err != nil {
		t.Fatalf("Cleanup returned error: %v", err)
	}
	if len(fake.deleteCalls) != 1 || fake.deleteCalls[0] != "sbx_test" {
		t.Fatalf("delete calls = %#v", fake.deleteCalls)
	}
}

func TestRuntimeOpenSandboxSetupFailurePreventsPreparedExecution(t *testing.T) {
	repo := writeEnvironmentContract(t, `
version: 1
sandbox:
  image:
    uri: alpine:3.20
  entrypoint: ["tail", "-f", "/dev/null"]
pilot:
  workspace:
    path: /workspace
  checkout:
    strategy: clone
  setup:
    - go mod download
`)
	fake := &fakeSandboxClient{
		commandResults: []fakeCommandResult{
			{result: &CommandResult{ExitCode: 0, Success: true}},
			{result: &CommandResult{ExitCode: 0, Success: true}},
			{result: &CommandResult{ExitCode: 1, Success: false, Stderr: "setup failed"}},
		},
	}
	provider := NewOpenSandboxProvider(&Config{
		Provider:    ProviderOpenSandbox,
		OpenSandbox: OpenSandboxConfig{Endpoint: "http://opensandbox.local:8080", Cleanup: CleanupConfig{AlwaysDelete: true}},
	}, fake)

	_, err := provider.Prepare(t.Context(), ExecutionRequest{
		TaskID:      "GH-2",
		ProjectPath: repo,
		RepoURL:     "https://github.com/acme/demo.git",
		Branch:      "pilot/GH-2",
		BaseBranch:  "main",
	})
	if err == nil || !strings.Contains(err.Error(), "runtime_setup_command_failed") {
		t.Fatalf("Prepare err = %v, want setup failure", err)
	}
	if len(fake.deleteCalls) != 1 {
		t.Fatalf("sandbox should be deleted after setup failure, delete calls = %#v", fake.deleteCalls)
	}
}

func TestRuntimeOpenSandboxVerifyFailure(t *testing.T) {
	repo := writeEnvironmentContract(t, `
version: 1
sandbox:
  image:
    uri: alpine:3.20
  entrypoint: ["tail", "-f", "/dev/null"]
pilot:
  workspace:
    path: /workspace
  checkout:
    strategy: clone
  verify:
    - make test
`)
	fake := &fakeSandboxClient{
		commandResults: []fakeCommandResult{
			{result: &CommandResult{ExitCode: 0, Success: true}},
			{result: &CommandResult{ExitCode: 0, Success: true}},
			{result: &CommandResult{ExitCode: 1, Success: false, Stderr: "tests failed"}},
		},
	}
	provider := NewOpenSandboxProvider(&Config{
		Provider:    ProviderOpenSandbox,
		OpenSandbox: OpenSandboxConfig{Endpoint: "http://opensandbox.local:8080", Cleanup: CleanupConfig{AlwaysDelete: true}},
	}, fake)

	prepared, err := provider.Prepare(t.Context(), ExecutionRequest{
		TaskID:      "GH-3",
		ProjectPath: repo,
		RepoURL:     "https://github.com/acme/demo.git",
		Branch:      "pilot/GH-3",
		BaseBranch:  "main",
	})
	if err != nil {
		t.Fatalf("Prepare returned error: %v", err)
	}
	err = prepared.RunVerify(t.Context())
	if err == nil || !strings.Contains(err.Error(), "runtime_verify_failed") {
		t.Fatalf("RunVerify err = %v, want verify failure", err)
	}
}

func TestRuntimeOpenSandboxMissingEnvironmentContract(t *testing.T) {
	fake := &fakeSandboxClient{}
	provider := NewOpenSandboxProvider(&Config{
		Provider:    ProviderOpenSandbox,
		OpenSandbox: OpenSandboxConfig{Endpoint: "http://opensandbox.local:8080"},
	}, fake)

	_, err := provider.Prepare(t.Context(), ExecutionRequest{
		TaskID:      "GH-4",
		ProjectPath: t.TempDir(),
		RepoURL:     "https://github.com/acme/demo.git",
		Branch:      "pilot/GH-4",
	})
	if !errors.Is(err, ErrEnvironmentNotFound) {
		t.Fatalf("Prepare err = %v, want ErrEnvironmentNotFound", err)
	}
	if fake.createdPayload != nil {
		t.Fatal("sandbox should not be created when contract is missing")
	}
}

func writeEnvironmentContract(t *testing.T, content string) string {
	t.Helper()
	repo := t.TempDir()
	dir := filepath.Join(repo, ".pilot")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "environment.yaml"), []byte(content), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	return repo
}
