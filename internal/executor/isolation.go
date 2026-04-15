package executor

import (
	"context"
	"fmt"
)

// IsolationProvider manages execution environment lifecycle.
// It creates an isolated workspace (worktree, sandbox, etc.) where the AI backend
// runs, and provides a CommandRunner configured for that environment.
//
// The isolation layer is orthogonal to the backend choice — any Backend (claude-code,
// qwen-code, opencode) can run inside any IsolationProvider (none, worktree, opensandbox).
type IsolationProvider interface {
	// Name returns the provider identifier (e.g., "none", "worktree", "opensandbox").
	Name() string

	// Prepare creates an isolated workspace for a task.
	// Returns an IsolatedEnvironment with a CommandRunner and cleanup function.
	// The Cleanup function MUST be called when execution completes.
	Prepare(ctx context.Context, opts IsolationOpts) (*IsolatedEnvironment, error)

	// IsAvailable checks if the provider is properly configured and usable.
	IsAvailable() bool
}

// IsolationOpts contains parameters for preparing an isolated environment.
type IsolationOpts struct {
	// TaskID is the unique task identifier, used for naming worktrees/sandboxes
	TaskID string

	// ProjectPath is the absolute path to the source project directory
	ProjectPath string

	// Branch is the git branch to create/use in the isolated environment
	Branch string

	// BaseBranch is the base branch for branching (e.g., "main")
	BaseBranch string

	// ProjectName is the project name, used for auto-built image naming.
	// Derived from ProjectConfig.Name or filepath.Base(ProjectPath).
	ProjectName string

	// ExecutorImage overrides the sandbox image for this task.
	// Set from ProjectConfig.ExecutorImage when available.
	// When empty, image resolution falls back to auto-build or global default.
	ExecutorImage string
}

// IsolatedEnvironment is the result of IsolationProvider.Prepare().
type IsolatedEnvironment struct {
	// WorkDir is the path where the project files live inside the isolation.
	// For none: original project path
	// For worktree: /tmp/pilot-worktree-xxx/
	// For opensandbox: /workspace (path inside the sandbox)
	WorkDir string

	// CommandRunner is configured to execute commands in this environment.
	// For none/worktree: LocalCommandRunner (local subprocess)
	// For opensandbox: OpenSandboxCommandRunner (remote execution via SDK)
	CommandRunner CommandRunner

	// Cleanup MUST be called when execution completes (success or failure).
	// Safe to call multiple times. Handles worktree removal, sandbox destruction, etc.
	Cleanup func()
}

// IsolationType constants for configuration.
const (
	IsolationTypeNone       = "none"
	IsolationTypeWorktree   = "worktree"
	IsolationTypeOpenSandbox = "opensandbox"
)

// NewIsolationProvider creates an IsolationProvider based on configuration.
// Falls back to legacy UseWorktree/WorktreePoolSize fields for backward compatibility.
func NewIsolationProvider(config *BackendConfig, worktreeManager *WorktreeManager) IsolationProvider {
	if config == nil {
		return &NoneIsolationProvider{}
	}

	// Explicit isolation config takes precedence
	if config.Isolation != nil {
		switch config.Isolation.Type {
		case IsolationTypeOpenSandbox:
			if config.Isolation.OpenSandbox == nil {
				return &NoneIsolationProvider{}
			}
			return NewOpenSandboxIsolationProvider(config.Isolation.OpenSandbox)
		case IsolationTypeWorktree:
			return NewWorktreeIsolationProvider(worktreeManager)
		case IsolationTypeNone, "":
			return &NoneIsolationProvider{}
		default:
			return &NoneIsolationProvider{}
		}
	}

	// Backward compatibility: legacy UseWorktree field
	if config.UseWorktree {
		return NewWorktreeIsolationProvider(worktreeManager)
	}

	return &NoneIsolationProvider{}
}

// NewIsolationProviderFromType creates an IsolationProvider by type string.
// Used for testing and factory patterns.
func NewIsolationProviderFromType(isoType string) (IsolationProvider, error) {
	switch isoType {
	case IsolationTypeNone, "":
		return &NoneIsolationProvider{}, nil
	case IsolationTypeWorktree:
		return nil, fmt.Errorf("worktree isolation requires a WorktreeManager — use NewIsolationProvider instead")
	case IsolationTypeOpenSandbox:
		return nil, fmt.Errorf("opensandbox isolation requires configuration — use NewIsolationProvider instead")
	default:
		return nil, fmt.Errorf("unknown isolation type: %s", isoType)
	}
}
