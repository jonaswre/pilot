package executor

import "time"

// IsolationConfig configures the execution isolation backend.
// When set in BackendConfig.Isolation, takes precedence over legacy UseWorktree/WorktreePoolSize fields.
type IsolationConfig struct {
	// Type specifies which isolation backend to use: "none", "worktree", or "opensandbox"
	Type string `yaml:"type"`

	// Worktree contains worktree-specific settings (only used when type: "worktree")
	Worktree *WorktreeIsolationConfig `yaml:"worktree,omitempty"`

	// OpenSandbox contains OpenSandbox-specific settings (only used when type: "opensandbox")
	OpenSandbox *OpenSandboxIsolationConfig `yaml:"opensandbox,omitempty"`
}

// WorktreeIsolationConfig contains settings for git worktree isolation.
type WorktreeIsolationConfig struct {
	// PoolSize sets the number of pre-created worktrees to pool.
	// When > 0, worktrees are reused across tasks in sequential mode.
	// Default: 0 (disabled)
	PoolSize int `yaml:"pool_size,omitempty"`
}

// OpenSandboxIsolationConfig contains settings for OpenSandbox isolation.
// OpenSandbox provides container/microVM isolation via Docker, Kubernetes,
// gVisor, Kata, or Firecracker runtimes.
type OpenSandboxIsolationConfig struct {
	// ServerURL is the OpenSandbox server URL (e.g., "http://localhost:8080")
	ServerURL string `yaml:"server_url"`

	// APIKey is the authentication key for the OpenSandbox server.
	// Supports env var expansion: "${OPENSANDBOX_API_KEY}"
	APIKey string `yaml:"api_key,omitempty"`

	// Image is the container image to use for sandboxes.
	// Should include git, Node.js, and Claude Code CLI.
	// Default: "pilot/executor:latest"
	Image string `yaml:"image,omitempty"`

	// Entrypoint overrides the container entrypoint.
	Entrypoint []string `yaml:"entrypoint,omitempty"`

	// Resources specifies CPU and memory limits for sandboxes.
	// Keys: "cpu" (e.g., "1000m"), "memory" (e.g., "2Gi")
	Resources map[string]string `yaml:"resources,omitempty"`

	// Timeout is the maximum sandbox lifetime.
	// Default: 30m
	Timeout time.Duration `yaml:"timeout,omitempty"`

	// Egress configures network egress rules for sandboxes.
	Egress *EgressConfig `yaml:"egress,omitempty"`

	// EnvVars contains additional environment variables to inject into sandboxes.
	// Use for passing API keys, tokens, etc.
	EnvVars map[string]string `yaml:"env_vars,omitempty"`
}

// EgressConfig configures network egress rules for sandboxed environments.
type EgressConfig struct {
	// AllowedDomains lists domains that sandboxes can reach.
	// Default: github.com, api.anthropic.com, registry.npmjs.org
	AllowedDomains []string `yaml:"allowed_domains,omitempty"`
}

// DefaultOpenSandboxConfig returns sensible defaults for OpenSandbox isolation.
func DefaultOpenSandboxConfig() *OpenSandboxIsolationConfig {
	return &OpenSandboxIsolationConfig{
		Image:   "pilot/executor:latest",
		Timeout: 30 * time.Minute,
		Resources: map[string]string{
			"cpu":    "1000m",
			"memory": "2Gi",
		},
		Egress: &EgressConfig{
			AllowedDomains: []string{
				"github.com",
				"api.github.com",
				"api.anthropic.com",
				"registry.npmjs.org",
				"pypi.org",
			},
		},
	}
}
