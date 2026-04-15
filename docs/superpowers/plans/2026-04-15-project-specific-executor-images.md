# Project-Specific Executor Images — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Auto-build project-specific Docker images from `Dockerfile.executor` so OpenSandbox tasks start with pre-baked repos and dependencies instead of cloning from scratch.

**Architecture:** The `OpenSandboxIsolationProvider` gains an image resolution step before sandbox creation. It checks for a per-task image override, then an auto-built image, then falls back to the global default. Auto-build runs `docker build` on the host via `LocalCommandRunner`. Inside the sandbox, `Prepare()` detects `/workspace/.git` and does a fast fetch+checkout instead of full clone.

**Tech Stack:** Go, Docker CLI (host-side), OpenSandbox SDK

**Spec:** `docs/superpowers/specs/2026-04-15-project-specific-executor-images-design.md`

---

## File Map

| Action | File | Purpose |
|--------|------|---------|
| Modify | `internal/executor/isolation_config.go` | Add `AutoBuild`, `DockerfilePath` fields |
| Modify | `internal/executor/isolation.go` | Add `ProjectName`, `ExecutorImage` to `IsolationOpts` |
| Create | `internal/executor/isolation_image.go` | Image resolution, existence check, auto-build, name sanitization |
| Modify | `internal/executor/isolation_opensandbox.go` | Use resolved image, smart clone detection |
| Modify | `internal/executor/runner.go` | Add `ExecutorImage` to `Task`, plumb into `IsolationOpts` |
| Modify | `internal/config/config.go` | Add `ExecutorImage` to `ProjectConfig` |
| Modify | `internal/orchestrator/orchestrator.go` | Set `ExecutorImage` on `executor.Task` from project config |
| Create | `internal/executor/isolation_image_test.go` | Unit tests for image resolution + name sanitization |
| Modify | `internal/executor/isolation_test.go` | Tests for new `IsolationOpts` fields |
| Modify | `internal/executor/isolation_opensandbox_integration_test.go` | Integration test for auto-build + smart clone |
| Modify | `configs/pilot.example.yaml` | Document new fields |

---

### Task 1: Config fields — `AutoBuild`, `DockerfilePath`, `ExecutorImage`

**Files:**
- Modify: `internal/executor/isolation_config.go:29-60` (OpenSandboxIsolationConfig struct)
- Modify: `internal/executor/isolation_config.go:69-87` (DefaultOpenSandboxConfig)
- Modify: `internal/config/config.go:171-180` (ProjectConfig struct)
- Modify: `configs/pilot.example.yaml:156-165` (isolation section)

- [ ] **Step 1: Add fields to `OpenSandboxIsolationConfig`**

In `internal/executor/isolation_config.go`, add two fields to the `OpenSandboxIsolationConfig` struct after the `EnvVars` field (line 58):

```go
// AutoBuild enables automatic image building from Dockerfile.executor
// in the project root when the target image does not exist locally.
// Default: true
AutoBuild *bool `yaml:"auto_build,omitempty"`

// DockerfilePath is the path (relative to project root) of the
// Dockerfile used to build project-specific executor images.
// Default: "Dockerfile.executor"
DockerfilePath string `yaml:"dockerfile_path,omitempty"`
```

- [ ] **Step 2: Update `DefaultOpenSandboxConfig` to set `AutoBuild` default**

In `DefaultOpenSandboxConfig()`, add:

```go
func DefaultOpenSandboxConfig() *OpenSandboxIsolationConfig {
	autoBuild := true
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
		AutoBuild:      &autoBuild,
		DockerfilePath: "Dockerfile.executor",
	}
}
```

- [ ] **Step 3: Add helper to read `AutoBuild` with default**

Below `DefaultOpenSandboxConfig()`:

```go
// IsAutoBuildEnabled returns whether auto-build is enabled (defaults to true).
func (c *OpenSandboxIsolationConfig) IsAutoBuildEnabled() bool {
	if c == nil || c.AutoBuild == nil {
		return true
	}
	return *c.AutoBuild
}
```

- [ ] **Step 4: Add `ExecutorImage` to `ProjectConfig`**

In `internal/config/config.go`, add to `ProjectConfig` struct after the `TeamReviewers` field (line 177):

```go
// ExecutorImage overrides the OpenSandbox container image for this project.
// When set, auto-build is skipped and this image is used directly.
// Example: "myorg/myapp-executor:latest"
ExecutorImage string `yaml:"executor_image,omitempty"`
```

- [ ] **Step 5: Update example config**

In `configs/pilot.example.yaml`, update the isolation comment block (around line 156) to:

```yaml
  # Isolation config (preferred, overrides use_worktree)
  # isolation:
  #   type: "worktree"         # "none" (default), "worktree", or "opensandbox"
  #   worktree:
  #     pool_size: 2           # Pre-created worktrees for faster task startup
  #   opensandbox:
  #     server_url: "http://localhost:8080"
  #     api_key: "${OPENSANDBOX_API_KEY}"
  #     image: "pilot/executor:latest"     # Fallback image (used when no Dockerfile.executor)
  #     auto_build: true                   # Build from Dockerfile.executor automatically
  #     dockerfile_path: "Dockerfile.executor"  # Path relative to project root
  #     timeout: 30m
```

And in the projects section (around line 248), add:

```yaml
    # executor_image: "myorg/myapp-executor:latest"  # Override sandbox image for this project
```

- [ ] **Step 6: Build to verify**

Run: `export PATH="/home/jonaswre/.local/share/mise/installs/go/1.24.2/bin:$PATH" && go build ./...`
Expected: Clean build, no errors.

- [ ] **Step 7: Commit**

```bash
git add internal/executor/isolation_config.go internal/config/config.go configs/pilot.example.yaml
git commit -m "feat(executor): add AutoBuild, DockerfilePath, ExecutorImage config fields"
```

---

### Task 2: IsolationOpts — add `ProjectName` and `ExecutorImage`

**Files:**
- Modify: `internal/executor/isolation.go:28-40` (IsolationOpts struct)

- [ ] **Step 1: Add fields to `IsolationOpts`**

In `internal/executor/isolation.go`, add two fields to `IsolationOpts` after `BaseBranch`:

```go
// ProjectName is the project name, used for auto-built image naming.
// Derived from ProjectConfig.Name or filepath.Base(ProjectPath).
ProjectName string

// ExecutorImage overrides the sandbox image for this task.
// Set from ProjectConfig.ExecutorImage when available.
// When empty, image resolution falls back to auto-build or global default.
ExecutorImage string
```

- [ ] **Step 2: Build to verify**

Run: `export PATH="/home/jonaswre/.local/share/mise/installs/go/1.24.2/bin:$PATH" && go build ./...`
Expected: Clean build.

- [ ] **Step 3: Commit**

```bash
git add internal/executor/isolation.go
git commit -m "feat(executor): add ProjectName and ExecutorImage to IsolationOpts"
```

---

### Task 3: Image resolution — `isolation_image.go`

**Files:**
- Create: `internal/executor/isolation_image.go`
- Create: `internal/executor/isolation_image_test.go`

- [ ] **Step 1: Write tests for `sanitizeImageName`**

Create `internal/executor/isolation_image_test.go`:

```go
package executor

import "testing"

func TestSanitizeImageName(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"my-app", "my-app"},
		{"My App", "my-app"},
		{"my_app_v2", "my-app-v2"},
		{"UPPER", "upper"},
		{"special!@#chars", "special---chars"},
		{"  spaces  ", "spaces"},
		{"a--b--c", "a--b--c"},
		{"", "project"},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := sanitizeImageName(tt.input)
			if got != tt.expected {
				t.Errorf("sanitizeImageName(%q) = %q, want %q", tt.input, got, tt.expected)
			}
		})
	}
}
```

- [ ] **Step 2: Run test — verify it fails**

Run: `export PATH="/home/jonaswre/.local/share/mise/installs/go/1.24.2/bin:$PATH" && go test ./internal/executor/ -run TestSanitizeImageName -v -count=1`
Expected: FAIL — `sanitizeImageName` not defined.

- [ ] **Step 3: Write tests for `executorImageTag`**

Append to `internal/executor/isolation_image_test.go`:

```go
func TestExecutorImageTag(t *testing.T) {
	tests := []struct {
		projectName string
		expected    string
	}{
		{"pilot", "pilot-executor/pilot:latest"},
		{"My App", "pilot-executor/my-app:latest"},
		{"", "pilot-executor/project:latest"},
	}

	for _, tt := range tests {
		t.Run(tt.projectName, func(t *testing.T) {
			got := executorImageTag(tt.projectName)
			if got != tt.expected {
				t.Errorf("executorImageTag(%q) = %q, want %q", tt.projectName, got, tt.expected)
			}
		})
	}
}
```

- [ ] **Step 4: Implement `isolation_image.go`**

Create `internal/executor/isolation_image.go`:

```go
package executor

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
)

// executorImageTag returns the Docker image tag for a project-specific executor.
// Format: pilot-executor/<sanitized-name>:latest
func executorImageTag(projectName string) string {
	return "pilot-executor/" + sanitizeImageName(projectName) + ":latest"
}

// sanitizeImageName converts a project name to a valid Docker image name component.
// Lowercase, replace non-alphanumeric with hyphens, trim.
func sanitizeImageName(name string) string {
	name = strings.TrimSpace(strings.ToLower(name))
	if name == "" {
		return "project"
	}
	re := regexp.MustCompile(`[^a-z0-9-]`)
	return re.ReplaceAllString(name, "-")
}

// executorImageExists checks if a Docker image exists locally.
func executorImageExists(ctx context.Context, imageName string) bool {
	runner := &LocalCommandRunner{}
	running, err := runner.Run(ctx, CommandRunOpts{
		Command: "docker",
		Args:    []string{"image", "inspect", imageName},
	})
	if err != nil {
		return false
	}
	io.Copy(io.Discard, running.Stdout)
	io.Copy(io.Discard, running.Stderr)
	return running.Wait() == nil
}

// buildMu serializes image builds per project name to prevent concurrent builds.
var buildMu sync.Map // map[string]*sync.Mutex

func getBuildMutex(projectName string) *sync.Mutex {
	val, _ := buildMu.LoadOrStore(projectName, &sync.Mutex{})
	return val.(*sync.Mutex)
}

// buildExecutorImage builds a project-specific executor image from a Dockerfile.
// Returns the image tag on success.
func buildExecutorImage(ctx context.Context, projectPath, projectName, dockerfilePath string) (string, error) {
	mu := getBuildMutex(projectName)
	mu.Lock()
	defer mu.Unlock()

	tag := executorImageTag(projectName)

	// Re-check after acquiring lock (another goroutine may have built it)
	if executorImageExists(ctx, tag) {
		return tag, nil
	}

	dfPath := dockerfilePath
	if dfPath == "" {
		dfPath = "Dockerfile.executor"
	}
	fullDfPath := filepath.Join(projectPath, dfPath)

	slog.Info("Building project-specific executor image",
		slog.String("tag", tag),
		slog.String("dockerfile", fullDfPath),
		slog.String("context", projectPath),
	)

	runner := &LocalCommandRunner{}
	running, err := runner.Run(ctx, CommandRunOpts{
		Command: "docker",
		Args: []string{
			"build",
			"-t", tag,
			"-f", fullDfPath,
			projectPath,
		},
	})
	if err != nil {
		return "", fmt.Errorf("failed to start docker build: %w", err)
	}

	// Capture build output for logging
	var stderr bytes.Buffer
	io.Copy(io.Discard, running.Stdout)
	io.Copy(&stderr, running.Stderr)

	if err := running.Wait(); err != nil {
		return "", fmt.Errorf("docker build failed: %w\nstderr: %s", err, stderr.String())
	}

	slog.Info("Built project-specific executor image", slog.String("tag", tag))
	return tag, nil
}

// resolveExecutorImage determines which container image to use for a task.
// Priority:
//  1. opts.ExecutorImage (per-project override from config)
//  2. Auto-built image (pilot-executor/<project>:latest) if it exists or can be built
//  3. config.Image (global default, e.g., "pilot/executor:latest")
func resolveExecutorImage(ctx context.Context, config *OpenSandboxIsolationConfig, opts IsolationOpts) string {
	// 1. Explicit per-project override
	if opts.ExecutorImage != "" {
		slog.Info("Using per-project executor image override",
			slog.String("image", opts.ExecutorImage),
		)
		return opts.ExecutorImage
	}

	// 2. Auto-built image
	projectName := opts.ProjectName
	if projectName == "" && opts.ProjectPath != "" {
		projectName = filepath.Base(opts.ProjectPath)
	}

	if projectName != "" {
		tag := executorImageTag(projectName)

		// Check if auto-built image already exists
		if executorImageExists(ctx, tag) {
			slog.Info("Using existing project-specific executor image",
				slog.String("image", tag),
			)
			return tag
		}

		// Try auto-build if enabled and Dockerfile exists
		if config.IsAutoBuildEnabled() && opts.ProjectPath != "" {
			dfPath := config.DockerfilePath
			if dfPath == "" {
				dfPath = "Dockerfile.executor"
			}
			fullDfPath := filepath.Join(opts.ProjectPath, dfPath)

			if _, err := os.Stat(fullDfPath); err == nil {
				built, err := buildExecutorImage(ctx, opts.ProjectPath, projectName, dfPath)
				if err != nil {
					slog.Warn("Auto-build failed, falling back to global image",
						slog.String("project", projectName),
						slog.Any("error", err),
					)
				} else {
					return built
				}
			}
		}
	}

	// 3. Global default
	return config.Image
}
```

- [ ] **Step 5: Run tests — verify they pass**

Run: `export PATH="/home/jonaswre/.local/share/mise/installs/go/1.24.2/bin:$PATH" && go test ./internal/executor/ -run "TestSanitizeImageName|TestExecutorImageTag" -v -count=1`
Expected: All PASS.

- [ ] **Step 6: Write test for `resolveExecutorImage`**

Append to `internal/executor/isolation_image_test.go`:

```go
func TestResolveExecutorImage(t *testing.T) {
	t.Run("explicit override wins", func(t *testing.T) {
		config := DefaultOpenSandboxConfig()
		opts := IsolationOpts{
			ExecutorImage: "custom/image:v2",
			ProjectName:   "myapp",
			ProjectPath:   "/tmp/nonexistent",
		}
		got := resolveExecutorImage(t.Context(), config, opts)
		if got != "custom/image:v2" {
			t.Errorf("expected custom/image:v2, got %s", got)
		}
	})

	t.Run("falls back to global default when no Dockerfile and no override", func(t *testing.T) {
		config := DefaultOpenSandboxConfig()
		config.Image = "pilot/executor:latest"
		opts := IsolationOpts{
			ProjectName: "nonexistent-project-xyz",
			ProjectPath: "/tmp/nonexistent-path-xyz",
		}
		got := resolveExecutorImage(t.Context(), config, opts)
		if got != "pilot/executor:latest" {
			t.Errorf("expected pilot/executor:latest, got %s", got)
		}
	})

	t.Run("falls back to global default with auto_build disabled", func(t *testing.T) {
		autoBuild := false
		config := DefaultOpenSandboxConfig()
		config.AutoBuild = &autoBuild
		config.Image = "pilot/executor:latest"
		opts := IsolationOpts{
			ProjectName: "testproject",
			ProjectPath: "/tmp/nonexistent",
		}
		got := resolveExecutorImage(t.Context(), config, opts)
		if got != "pilot/executor:latest" {
			t.Errorf("expected pilot/executor:latest, got %s", got)
		}
	})

	t.Run("derives project name from path when ProjectName empty", func(t *testing.T) {
		config := DefaultOpenSandboxConfig()
		config.Image = "fallback:latest"
		opts := IsolationOpts{
			ProjectPath: "/home/user/projects/my-cool-app",
		}
		// No Docker, no Dockerfile — should fall back to global
		got := resolveExecutorImage(t.Context(), config, opts)
		if got != "fallback:latest" {
			t.Errorf("expected fallback:latest, got %s", got)
		}
	})
}
```

- [ ] **Step 7: Run all image tests**

Run: `export PATH="/home/jonaswre/.local/share/mise/installs/go/1.24.2/bin:$PATH" && go test ./internal/executor/ -run "TestSanitize|TestExecutorImage|TestResolveExecutor" -v -count=1`
Expected: All PASS.

- [ ] **Step 8: Commit**

```bash
git add internal/executor/isolation_image.go internal/executor/isolation_image_test.go
git commit -m "feat(executor): image resolution — auto-build, existence check, name sanitization"
```

---

### Task 4: Smart clone detection in `Prepare()`

**Files:**
- Modify: `internal/executor/isolation_opensandbox.go:49-218` (Prepare method)

- [ ] **Step 1: Use `resolveExecutorImage` in `Prepare()`**

In `internal/executor/isolation_opensandbox.go`, replace the hardcoded `p.config.Image` in the `Prepare()` method. After the `slog.Info("Creating OpenSandbox isolation"` log (line 56), add image resolution and update the `CreateSandboxRequest`:

Replace:

```go
slog.Info("Creating OpenSandbox isolation",
	slog.String("task_id", opts.TaskID),
	slog.String("server", p.config.ServerURL),
	slog.String("image", p.config.Image),
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
	Image:          opensandbox.ImageSpec{URI: p.config.Image},
	Entrypoint:     entrypoint,
	ResourceLimits: resources,
}
```

With:

```go
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
```

- [ ] **Step 2: Replace full clone with smart clone detection**

Replace the "Clone repository inside sandbox" block (lines 173-206):

```go
// Clone repository inside sandbox
if opts.ProjectPath != "" {
	slog.Info("Cloning repository in sandbox",
		slog.String("project", opts.ProjectPath),
		slog.String("branch", opts.Branch),
	)

	// Get remote URL from project path
	cloneURL, err := getGitRemoteURL(ctx, opts.ProjectPath)
	if err != nil {
		cleanup()
		return nil, fmt.Errorf("opensandbox: failed to get git remote URL: %w", err)
	}

	// Clone into /workspace (shell-escape URL to prevent injection)
	cloneCmd := fmt.Sprintf("git clone --depth 50 %s /workspace", shellQuote(cloneURL))
	if err := runSandboxCommand(ctx, execd, sandboxID, cloneCmd); err != nil {
		cleanup()
		return nil, fmt.Errorf("opensandbox: failed to clone repo: %w", err)
	}

	// Create and checkout branch if specified (shell-escape branch names)
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
```

With:

```go
// Set up repository inside sandbox.
// Two paths: if /workspace/.git exists (pre-baked image), fetch+checkout.
// Otherwise, full clone from remote.
if opts.ProjectPath != "" {
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
		if err := runSandboxCommand(ctx, execd, sandboxID, "cd /workspace && git fetch origin"); err != nil {
			cleanup()
			return nil, fmt.Errorf("opensandbox: failed to fetch in pre-baked repo: %w", err)
		}
		if opts.Branch != "" {
			branchCmd := fmt.Sprintf("cd /workspace && git checkout -B %s", shellQuote(opts.Branch))
			if opts.BaseBranch != "" {
				branchCmd = fmt.Sprintf("cd /workspace && git checkout -B %s origin/%s",
					shellQuote(opts.Branch), shellQuote(opts.BaseBranch))
			}
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

		cloneURL, err := getGitRemoteURL(ctx, opts.ProjectPath)
		if err != nil {
			cleanup()
			return nil, fmt.Errorf("opensandbox: failed to get git remote URL: %w", err)
		}

		cloneCmd := fmt.Sprintf("git clone --depth 50 %s /workspace", shellQuote(cloneURL))
		if err := runSandboxCommand(ctx, execd, sandboxID, cloneCmd); err != nil {
			cleanup()
			return nil, fmt.Errorf("opensandbox: failed to clone repo: %w", err)
		}

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
```

- [ ] **Step 3: Build to verify**

Run: `export PATH="/home/jonaswre/.local/share/mise/installs/go/1.24.2/bin:$PATH" && go build ./...`
Expected: Clean build.

- [ ] **Step 4: Run existing tests**

Run: `export PATH="/home/jonaswre/.local/share/mise/installs/go/1.24.2/bin:$PATH" && go test ./internal/executor/ -run "TestNewIsolation|TestNoneIsolation|TestSanitize|TestExecutorImage|TestResolveExecutor" -v -count=1`
Expected: All PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/executor/isolation_opensandbox.go
git commit -m "feat(executor): smart clone detection — fetch+checkout for pre-baked images"
```

---

### Task 5: Runner + Orchestrator wiring

**Files:**
- Modify: `internal/executor/runner.go:110-167` (Task struct)
- Modify: `internal/executor/runner.go:960-988` (Execute, isolation opts)
- Modify: `internal/orchestrator/orchestrator.go:197-204` (execTask construction)

- [ ] **Step 1: Add `ExecutorImage` to `Task` struct**

In `internal/executor/runner.go`, add after the `LocalMode` field (line 166):

```go
// ExecutorImage overrides the OpenSandbox container image for this task.
// Set from ProjectConfig.ExecutorImage by the orchestrator.
// When empty, the isolation provider uses auto-build or global default.
ExecutorImage string
```

- [ ] **Step 2: Plumb fields into `IsolationOpts` in `Execute()`**

In `internal/executor/runner.go`, update the `provider.Prepare()` call (around line 983). Replace:

```go
env, err := provider.Prepare(ctx, IsolationOpts{
	TaskID:      task.ID,
	ProjectPath: task.ProjectPath,
	Branch:      task.Branch,
	BaseBranch:  "",
})
```

With:

```go
env, err := provider.Prepare(ctx, IsolationOpts{
	TaskID:        task.ID,
	ProjectPath:   task.ProjectPath,
	Branch:        task.Branch,
	BaseBranch:    "",
	ProjectName:   filepath.Base(task.ProjectPath),
	ExecutorImage: task.ExecutorImage,
})
```

Also add `"path/filepath"` to the imports if not already present (it is — `filepath` is already used at line 1416).

- [ ] **Step 3: Set `ExecutorImage` in orchestrator**

In `internal/orchestrator/orchestrator.go`, update the `execTask` construction at line 197. Replace:

```go
execTask := &executor.Task{
	ID:          task.ID,
	Title:       task.Document.Title,
	Description: task.Document.Markdown,
	Priority:    task.Ticket.Priority,
	ProjectPath: task.ProjectPath,
	Branch:      task.Branch,
}
```

With:

```go
execTask := &executor.Task{
	ID:          task.ID,
	Title:       task.Document.Title,
	Description: task.Document.Markdown,
	Priority:    task.Ticket.Priority,
	ProjectPath: task.ProjectPath,
	Branch:      task.Branch,
	ExecutorImage: task.ExecutorImage,
}
```

The orchestrator's `Task` struct (internal, different from `executor.Task`) also needs the field. Find it:

```go
// In orchestrator.go, the internal Task struct
type Task struct {
    // ... existing fields ...
    ProjectPath string
    // ... more fields ...
}
```

Add `ExecutorImage string` to the internal `Task` struct. Then update each `Process*Ticket` method that constructs internal tasks to set it. The orchestrator receives `projectPath` as a parameter; to get the `ExecutorImage`, check if a project config lookup is available.

However, looking at the orchestrator, the `Process*Ticket` methods receive `projectPath` as a string, not the full project config. The simplest approach: add `executorImage` as a parameter to these methods, or look up the project config in the method.

The cleanest approach: the `Orchestrator` struct has a `config` field. Check:

- [ ] **Step 3a: Verify orchestrator has config access**

Check if the orchestrator can resolve project config. If it has `*config.Config`, use `config.GetProject(projectPath).ExecutorImage`. If not, the caller (adapter) can pass it.

Look at the orchestrator struct. If it doesn't have config, the simpler path is: the runner derives project name from `filepath.Base(task.ProjectPath)` (already done in step 2), and `ExecutorImage` stays empty unless explicitly set. The per-project override (`ProjectConfig.ExecutorImage`) gets wired later when someone configures it.

For now, the auto-build path (which uses `ProjectName` derived from path) works without any orchestrator changes. Only the explicit `ExecutorImage` override needs orchestrator wiring, and that's an optional config field.

**Decision:** Skip orchestrator wiring for `ExecutorImage` in this task. The auto-build flow works fully via `ProjectName` derived from `filepath.Base()`. The `ExecutorImage` override on `executor.Task` is ready for future wiring when the orchestrator gains project config access.

- [ ] **Step 4: Build to verify**

Run: `export PATH="/home/jonaswre/.local/share/mise/installs/go/1.24.2/bin:$PATH" && go build ./...`
Expected: Clean build.

- [ ] **Step 5: Run full test suite**

Run: `export PATH="/home/jonaswre/.local/share/mise/installs/go/1.24.2/bin:$PATH" && go test ./internal/executor/ ./internal/orchestrator/ -count=1`
Expected: All PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/executor/runner.go internal/orchestrator/orchestrator.go
git commit -m "feat(executor): wire ExecutorImage + ProjectName through runner and orchestrator"
```

---

### Task 6: Update integration tests

**Files:**
- Modify: `internal/executor/isolation_opensandbox_integration_test.go`

- [ ] **Step 1: Add auto-build integration test**

Append to `internal/executor/isolation_opensandbox_integration_test.go`, inside the `TestOpenSandboxIntegration` function:

```go
t.Run("AutoBuildAndSmartClone", func(t *testing.T) {
	// This test requires a git repo with a Dockerfile.executor
	// Skip if not in a git repo
	if _, err := getGitRemoteURL(ctx, "."); err != nil {
		t.Skip("not in a git repo with remote, skipping auto-build test")
	}

	// Create a temp dir with a minimal Dockerfile.executor
	tmpDir := t.TempDir()
	// Init a git repo so getGitRemoteURL works
	if err := runLocalCmd(ctx, tmpDir, "git", "init"); err != nil {
		t.Fatalf("git init failed: %v", err)
	}
	if err := runLocalCmd(ctx, tmpDir, "git", "remote", "add", "origin", "https://github.com/octocat/Hello-World.git"); err != nil {
		t.Fatalf("git remote add failed: %v", err)
	}

	// Write a simple Dockerfile.executor
	dockerfile := `FROM python:3.11-slim
RUN mkdir -p /workspace && echo "prebaked" > /workspace/.git-marker
WORKDIR /workspace
`
	if err := os.WriteFile(filepath.Join(tmpDir, "Dockerfile.executor"), []byte(dockerfile), 0644); err != nil {
		t.Fatalf("write Dockerfile.executor: %v", err)
	}

	autoBuild := true
	abConfig := &OpenSandboxIsolationConfig{
		ServerURL: "http://localhost:8080/v1",
		Image:     "python:3.11-slim",
		Timeout:   5 * time.Minute,
		Resources: map[string]string{"cpu": "500m", "memory": "512Mi"},
		AutoBuild: &autoBuild,
	}

	provider := NewOpenSandboxIsolationProvider(abConfig)
	env, err := provider.Prepare(ctx, IsolationOpts{
		TaskID:      "test-autobuild-1",
		ProjectPath: tmpDir,
		ProjectName: "test-autobuild",
	})
	if err != nil {
		t.Fatalf("Prepare with auto-build failed: %v", err)
	}
	defer env.Cleanup()

	// Verify the image was built
	if !executorImageExists(ctx, "pilot-executor/test-autobuild:latest") {
		t.Error("expected auto-built image to exist")
	}
})
```

Add helper at the bottom of the file:

```go
func runLocalCmd(ctx context.Context, dir, command string, args ...string) error {
	runner := &LocalCommandRunner{}
	running, err := runner.Run(ctx, CommandRunOpts{
		Command: command,
		Args:    args,
		Dir:     dir,
	})
	if err != nil {
		return err
	}
	io.Copy(io.Discard, running.Stdout)
	io.Copy(io.Discard, running.Stderr)
	return running.Wait()
}
```

Also add needed imports: `"os"`, `"path/filepath"`.

- [ ] **Step 2: Build to verify**

Run: `export PATH="/home/jonaswre/.local/share/mise/installs/go/1.24.2/bin:$PATH" && go build ./internal/executor/`
Expected: Clean build.

- [ ] **Step 3: Run unit tests (not integration)**

Run: `export PATH="/home/jonaswre/.local/share/mise/installs/go/1.24.2/bin:$PATH" && go test ./internal/executor/ -run "TestNewIsolation|TestNoneIsolation|TestSanitize|TestExecutorImage|TestResolveExecutor" -v -count=1`
Expected: All PASS. (Integration tests are gated and won't run.)

- [ ] **Step 4: Commit**

```bash
git add internal/executor/isolation_opensandbox_integration_test.go
git commit -m "test(executor): add auto-build + smart clone integration test"
```

---

### Task 7: Final verification

- [ ] **Step 1: Full build**

Run: `export PATH="/home/jonaswre/.local/share/mise/installs/go/1.24.2/bin:$PATH" && go build ./...`
Expected: Clean.

- [ ] **Step 2: Full test suite**

Run: `export PATH="/home/jonaswre/.local/share/mise/installs/go/1.24.2/bin:$PATH" && go test ./internal/executor/ ./internal/config/ ./internal/orchestrator/ ./cmd/pilot/ -count=1`
Expected: All PASS.

- [ ] **Step 3: Verify no regressions in existing isolation tests**

Run: `export PATH="/home/jonaswre/.local/share/mise/installs/go/1.24.2/bin:$PATH" && go test ./internal/executor/ -run TestNewIsolationProvider -v -count=1`
Expected: All 10 subtests PASS.
