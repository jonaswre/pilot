package executor

import (
	"context"
	"os/exec"
	"strings"
	"testing"

	pilotruntime "github.com/qf-studio/pilot/internal/runtime"
)

type fakeExecutionProvider struct {
	requests []pilotruntime.ExecutionRequest
	prepared *pilotruntime.PreparedExecution
	err      error
}

func (f *fakeExecutionProvider) Prepare(ctx context.Context, req pilotruntime.ExecutionRequest) (*pilotruntime.PreparedExecution, error) {
	f.requests = append(f.requests, req)
	return f.prepared, f.err
}

type recordingRuntimeCommands struct {
	specs []pilotruntime.CommandSpec
}

func (r *recordingRuntimeCommands) run(ctx context.Context, spec pilotruntime.CommandSpec) (*pilotruntime.CommandResult, error) {
	r.specs = append(r.specs, spec)
	switch {
	case spec.Command == "claude-custom":
		return &pilotruntime.CommandResult{
			Stdout: strings.Join([]string{
				`{"type":"system","subtype":"init","session_id":"remote-session"}`,
				`{"type":"result","result":"implemented remotely","is_error":false,"usage":{"input_tokens":11,"output_tokens":7},"model":"claude-remote"}`,
			}, "\n") + "\n",
			ExitCode: 0,
			Success:  true,
		}, nil
	case spec.Command == "make test":
		return &pilotruntime.CommandResult{ExitCode: 0, Success: true}, nil
	case strings.Contains(spec.Command, "git rev-list"):
		return &pilotruntime.CommandResult{Stdout: "1\n", ExitCode: 0, Success: true}, nil
	case strings.Contains(spec.Command, "git rev-parse HEAD"):
		return &pilotruntime.CommandResult{Stdout: "abcdef1234567890\n", ExitCode: 0, Success: true}, nil
	case strings.Contains(spec.Command, "git diff --numstat"):
		return &pilotruntime.CommandResult{Stdout: "3\t1\tinternal/demo.go\n", ExitCode: 0, Success: true}, nil
	case strings.Contains(spec.Command, "git push"):
		return &pilotruntime.CommandResult{ExitCode: 0, Success: true}, nil
	default:
		return &pilotruntime.CommandResult{ExitCode: 0, Success: true}, nil
	}
}

type fakeRuntimePRCreator struct {
	sourceBranch string
	targetBranch string
	title        string
}

func (f *fakeRuntimePRCreator) CreatePR(ctx context.Context, sourceBranch, targetBranch, title, body string) (string, error) {
	f.sourceBranch = sourceBranch
	f.targetBranch = targetBranch
	f.title = title
	return "https://git.example/acme/demo/merge_requests/1", nil
}

func TestRunnerOpenSandboxRuntimeExecutesBackendAndCreatesPR(t *testing.T) {
	repo := initRuntimeTestRepo(t)
	commands := &recordingRuntimeCommands{}
	provider := &fakeExecutionProvider{
		prepared: pilotruntime.NewPreparedExecution(
			"/workspace",
			"sbx_test",
			nil,
			[]string{"make test"},
			commands.run,
			func(context.Context, bool) error { return nil },
		),
	}
	prCreator := &fakeRuntimePRCreator{}

	backend := NewClaudeCodeBackend(&ClaudeCodeConfig{Command: "claude-custom"})
	runner := NewRunnerWithBackend(backend)
	runner.SetRuntimeConfig(&pilotruntime.Config{
		Provider: pilotruntime.ProviderOpenSandbox,
		OpenSandbox: pilotruntime.OpenSandboxConfig{
			Endpoint: "http://opensandbox.local:8080",
		},
	})
	runner.SetRuntimeProvider(provider)
	runner.SetPRCreator(prCreator)

	result, err := runner.Execute(t.Context(), &Task{
		ID:            "GH-123",
		Title:         "feat(runtime): add sandbox path",
		Description:   "Run this task in OpenSandbox",
		ProjectPath:   repo,
		Branch:        "pilot/GH-123",
		BaseBranch:    "main",
		CreatePR:      true,
		SourceAdapter: "gitlab",
		SourceIssueID: "123",
	})
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	if !result.Success {
		t.Fatalf("Success = false, error = %q", result.Error)
	}
	if result.PRUrl != "https://git.example/acme/demo/merge_requests/1" {
		t.Fatalf("PRUrl = %q", result.PRUrl)
	}
	if result.CommitSHA != "abcdef1234567890" {
		t.Fatalf("CommitSHA = %q", result.CommitSHA)
	}
	if result.TokensInput != 11 || result.TokensOutput != 7 {
		t.Fatalf("tokens = %d/%d", result.TokensInput, result.TokensOutput)
	}

	if len(provider.requests) != 1 {
		t.Fatalf("Prepare calls = %d", len(provider.requests))
	}
	if provider.requests[0].RepoURL != "https://github.com/acme/demo.git" {
		t.Fatalf("RepoURL = %q", provider.requests[0].RepoURL)
	}
	if provider.requests[0].Branch != "pilot/GH-123" {
		t.Fatalf("Branch = %q", provider.requests[0].Branch)
	}
	if prCreator.sourceBranch != "pilot/GH-123" || prCreator.targetBranch != "main" {
		t.Fatalf("PR creator branches = %q -> %q", prCreator.sourceBranch, prCreator.targetBranch)
	}

	if !runtimeSpecsContain(commands.specs, func(spec pilotruntime.CommandSpec) bool {
		return spec.Command == "claude-custom" && spec.CWD == "/workspace" && stringSliceContains(spec.Args, "-p")
	}) {
		t.Fatalf("backend command spec not recorded: %#v", commands.specs)
	}
	if !runtimeSpecsContain(commands.specs, func(spec pilotruntime.CommandSpec) bool {
		return spec.Command == "make test" && spec.CWD == "/workspace"
	}) {
		t.Fatalf("verify command not recorded: %#v", commands.specs)
	}
	if !runtimeSpecsContain(commands.specs, func(spec pilotruntime.CommandSpec) bool {
		return strings.Contains(spec.Command, "git push -u origin")
	}) {
		t.Fatalf("push command not recorded: %#v", commands.specs)
	}
}

func initRuntimeTestRepo(t *testing.T) string {
	t.Helper()
	repo := t.TempDir()
	runGit := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = repo
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	runGit("init", "-b", "main")
	runGit("config", "user.email", "pilot@example.com")
	runGit("config", "user.name", "Pilot")
	runGit("remote", "add", "origin", "https://github.com/acme/demo.git")
	return repo
}

func runtimeSpecsContain(specs []pilotruntime.CommandSpec, match func(pilotruntime.CommandSpec) bool) bool {
	for _, spec := range specs {
		if match(spec) {
			return true
		}
	}
	return false
}
