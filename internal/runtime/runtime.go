package runtime

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path"
	"strings"
	"time"
)

type SandboxClient interface {
	CreateSandbox(context.Context, map[string]any) (*Sandbox, error)
	WaitForRunning(context.Context, string, time.Duration) (*Sandbox, error)
	DeleteSandbox(context.Context, string) error
	GetEndpoint(context.Context, string, int) (*SandboxEndpoint, error)
	RunCommand(context.Context, string, map[string]string, CommandSpec) (*CommandResult, error)
}

type ExecutionRequest struct {
	TaskID      string
	ProjectPath string
	RepoURL     string
	Branch      string
	BaseBranch  string
	ProjectName string
	SourceRepo  string
}

type PreparedExecution struct {
	WorkspacePath string
	SandboxID     string
	Environment   *Environment

	runCommand func(context.Context, CommandSpec) (*CommandResult, error)
	verify     []string
	cleanup    func(context.Context, bool) error
}

func (p *PreparedExecution) RunCommand(ctx context.Context, spec CommandSpec) (*CommandResult, error) {
	return p.runCommand(ctx, spec)
}

func (p *PreparedExecution) RunVerify(ctx context.Context) error {
	for _, command := range p.verify {
		result, err := p.RunCommand(ctx, CommandSpec{Command: command, CWD: p.WorkspacePath})
		if err != nil {
			return fmt.Errorf("runtime_verify_failed: %w", err)
		}
		if result != nil && !result.Success {
			return fmt.Errorf("runtime_verify_failed: command %q exited %d: %s", command, result.ExitCode, strings.TrimSpace(result.Stderr))
		}
	}
	return nil
}

func (p *PreparedExecution) Cleanup(ctx context.Context, success bool) error {
	if p.cleanup == nil {
		return nil
	}
	return p.cleanup(ctx, success)
}

type ExecutionProvider interface {
	Prepare(context.Context, ExecutionRequest) (*PreparedExecution, error)
}

type HostProvider struct{}

func NewHostProvider() *HostProvider {
	return &HostProvider{}
}

func (p *HostProvider) Prepare(ctx context.Context, req ExecutionRequest) (*PreparedExecution, error) {
	return &PreparedExecution{
		WorkspacePath: req.ProjectPath,
		runCommand: func(ctx context.Context, spec CommandSpec) (*CommandResult, error) {
			cmd := exec.CommandContext(ctx, "bash", "-lc", spec.Command)
			if spec.CWD != "" {
				cmd.Dir = spec.CWD
			}
			cmd.Env = os.Environ()
			for k, v := range spec.Env {
				cmd.Env = append(cmd.Env, k+"="+v)
			}
			output, err := cmd.CombinedOutput()
			result := &CommandResult{
				Stdout:   string(output),
				ExitCode: 0,
				Success:  err == nil,
			}
			if err != nil {
				result.ExitCode = 1
				result.Stderr = err.Error()
			}
			return result, err
		},
		cleanup: func(context.Context, bool) error { return nil },
	}, nil
}

type OpenSandboxProvider struct {
	config *Config
	client SandboxClient
}

func NewOpenSandboxProvider(config *Config, client SandboxClient) *OpenSandboxProvider {
	return &OpenSandboxProvider{config: config, client: client}
}

func (p *OpenSandboxProvider) Prepare(ctx context.Context, req ExecutionRequest) (*PreparedExecution, error) {
	env, err := LoadEnvironment(req.ProjectPath)
	if err != nil {
		return nil, err
	}
	if p.config == nil {
		return nil, fmt.Errorf("runtime_unavailable: runtime config is required")
	}
	if err := p.config.Validate(); err != nil {
		return nil, fmt.Errorf("runtime_unavailable: %w", err)
	}
	if p.client == nil {
		p.client = NewOpenSandboxClient(p.config.OpenSandbox.Endpoint, p.config.OpenSandbox.APIKey, nil)
	}
	if req.RepoURL == "" {
		return nil, fmt.Errorf("runtime_workspace_setup_failed: repo URL is required")
	}

	secrets, err := p.config.ResolveSecrets(env.RequiredSecrets())
	if err != nil {
		return nil, fmt.Errorf("environment_secret_missing: %w", err)
	}

	payload := copySandboxPayload(env.Sandbox)
	mergeStringAnyMap(payload, "metadata", map[string]any{
		"pilot_task_id":     req.TaskID,
		"pilot_project":     req.ProjectName,
		"pilot_branch":      req.Branch,
		"pilot_source_repo": req.SourceRepo,
	})
	runtimeEnv := map[string]any{
		"PILOT_REPO_URL":    req.RepoURL,
		"PILOT_BRANCH":      req.Branch,
		"PILOT_BASE_BRANCH": req.BaseBranch,
		"PILOT_WORKSPACE":   env.Pilot.Workspace.Path,
		"PILOT_EXECUTOR":    "1",
	}
	for name, value := range secrets {
		runtimeEnv[name] = value
	}
	mergeStringAnyMap(payload, "env", runtimeEnv)

	sandbox, err := p.client.CreateSandbox(ctx, payload)
	if err != nil {
		return nil, fmt.Errorf("runtime_sandbox_create_failed: %w", err)
	}
	prepared := &PreparedExecution{
		WorkspacePath: env.Pilot.Workspace.Path,
		SandboxID:     sandbox.ID,
		Environment:   env,
		verify:        append([]string(nil), env.Pilot.Verify...),
	}

	endpoint, err := p.client.GetEndpoint(ctx, sandbox.ID, DefaultExecdPort)
	if err != nil {
		_ = p.cleanup(ctx, sandbox.ID, false)
		return nil, fmt.Errorf("runtime_sandbox_create_failed: %w", err)
	}
	if _, err := p.client.WaitForRunning(ctx, sandbox.ID, time.Second); err != nil {
		_ = p.cleanup(ctx, sandbox.ID, false)
		return nil, fmt.Errorf("runtime_sandbox_create_failed: %w", err)
	}

	prepared.runCommand = func(ctx context.Context, spec CommandSpec) (*CommandResult, error) {
		if spec.CWD == "" {
			spec.CWD = prepared.WorkspacePath
		}
		return p.client.RunCommand(ctx, endpoint.Endpoint, endpoint.Headers, spec)
	}
	prepared.cleanup = func(ctx context.Context, success bool) error {
		return p.cleanup(ctx, sandbox.ID, success)
	}

	if err := p.setupWorkspace(ctx, prepared, req, env); err != nil {
		_ = prepared.Cleanup(ctx, false)
		return nil, err
	}
	return prepared, nil
}

func (p *OpenSandboxProvider) setupWorkspace(ctx context.Context, prepared *PreparedExecution, req ExecutionRequest, env *Environment) error {
	cloneCommand := buildCloneCommand(req.RepoURL, env.Pilot.Workspace.Path, env.Pilot.Checkout.Depth)
	if err := runRequiredCommand(ctx, prepared, "runtime_workspace_setup_failed", cloneCommand, "/"); err != nil {
		return err
	}
	if req.Branch != "" {
		base := req.BaseBranch
		if base == "" {
			base = "main"
		}
		checkoutCommand := fmt.Sprintf("git checkout -B %s %s", shellQuote(req.Branch), shellQuote("origin/"+base))
		if err := runRequiredCommand(ctx, prepared, "runtime_workspace_setup_failed", checkoutCommand, prepared.WorkspacePath); err != nil {
			return err
		}
	}
	for _, command := range env.Pilot.Setup {
		if err := runRequiredCommand(ctx, prepared, "runtime_setup_command_failed", command, prepared.WorkspacePath); err != nil {
			return err
		}
	}
	return nil
}

func (p *OpenSandboxProvider) cleanup(ctx context.Context, sandboxID string, success bool) error {
	cleanup := p.config.OpenSandbox.Cleanup
	if !cleanup.AlwaysDelete && (!success && cleanup.RetainOnFailure) {
		return nil
	}
	if !success && cleanup.RetainOnFailure {
		return nil
	}
	return p.client.DeleteSandbox(ctx, sandboxID)
}

func runRequiredCommand(ctx context.Context, prepared *PreparedExecution, category, command, cwd string) error {
	result, err := prepared.RunCommand(ctx, CommandSpec{Command: command, CWD: cwd})
	if err != nil {
		return fmt.Errorf("%s: %w", category, err)
	}
	if result != nil && !result.Success {
		return fmt.Errorf("%s: command %q exited %d: %s", category, command, result.ExitCode, strings.TrimSpace(result.Stderr))
	}
	return nil
}

func buildCloneCommand(repoURL, workspace string, depth int) string {
	parent := path.Dir(workspace)
	parts := []string{"rm -rf", shellQuote(workspace), "&& mkdir -p", shellQuote(parent), "&& git clone"}
	if depth > 0 {
		parts = append(parts, "--depth", fmt.Sprintf("%d", depth))
	}
	parts = append(parts, shellQuote(repoURL), shellQuote(workspace))
	return strings.Join(parts, " ")
}

func copySandboxPayload(in map[string]any) map[string]any {
	out := make(map[string]any, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

func mergeStringAnyMap(target map[string]any, key string, values map[string]any) {
	existing := map[string]any{}
	if raw, ok := target[key]; ok {
		switch typed := raw.(type) {
		case map[string]any:
			for k, v := range typed {
				existing[k] = v
			}
		case map[string]string:
			for k, v := range typed {
				existing[k] = v
			}
		}
	}
	for k, v := range values {
		if v != "" {
			existing[k] = v
		}
	}
	target[key] = existing
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "'\"'\"'") + "'"
}
