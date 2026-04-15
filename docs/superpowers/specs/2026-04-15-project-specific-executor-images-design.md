# Project-Specific Executor Images for OpenSandbox

**Date:** 2026-04-15
**Status:** Approved
**Branch:** feat/opensandbox-isolation

## Problem

OpenSandbox isolation currently spawns a generic `pilot/executor:latest` container and clones the repo + installs dependencies on every task execution. For projects with large dependency trees (Go modules, node_modules, Python venvs), this adds minutes of overhead per task.

## Solution

Allow projects to include a `Dockerfile.executor` in their repo root. Pilot auto-builds a project-specific image on first task, then reuses it. The image has the repo and dependencies pre-baked at `/workspace`. On task start, `Prepare()` detects the pre-baked repo and does a fast `git fetch + checkout` instead of a full clone.

## Architecture

### Image lifecycle

```
Task arrives for project "my-app"
  → Does image pilot-executor/my-app:latest exist locally?
    NO  → Does <project-path>/Dockerfile.executor exist?
           YES → docker build -t pilot-executor/my-app:latest -f Dockerfile.executor <project-path>
           NO  → fall back to global config image (pilot/executor:latest) + full clone
    YES → use existing image

Sandbox starts with resolved image
  → Does /workspace/.git exist inside container?
    YES → git fetch origin && git checkout -B <branch> [origin/<base>]  (fast path)
    NO  → full clone from remote  (generic image path)
```

### Config changes

#### `OpenSandboxIsolationConfig` (isolation_config.go)

Two new fields:

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

#### `ProjectConfig` (config/config.go)

One new field:

```go
// ExecutorImage overrides the OpenSandbox image for this project.
// When set, auto-build is skipped and this image is used directly.
// Example: "myorg/myapp-executor:latest"
ExecutorImage string `yaml:"executor_image,omitempty"`
```

### Image naming convention

Auto-built images use the pattern `pilot-executor/<project-name>:latest`.

- Project name "pilot" -> `pilot-executor/pilot:latest`
- Project name "frontend" -> `pilot-executor/frontend:latest`
- Sanitized: lowercase, replace non-alphanumeric with `-`

### Prepare() flow changes (isolation_opensandbox.go)

`Prepare()` receives `IsolationOpts` which already has `ProjectPath`. Changes:

1. **Resolve image:** Before creating the sandbox, resolve which image to use:
   - Check if `ProjectConfig.ExecutorImage` is set (requires plumbing project config to `IsolationOpts`)
   - Otherwise, check if auto-built image exists: `docker image inspect pilot-executor/<name>:latest`
   - If not and `AutoBuild` is true and `Dockerfile.executor` exists at project path, build it
   - Fall back to `config.Image` (global default)

2. **Smart clone detection:** After sandbox creation, before cloning:
   - Run `test -d /workspace/.git` inside the sandbox
   - If exists: `git fetch origin && git checkout -B <branch> [origin/<base>]`
   - If not: full clone (existing behavior)

### New types and functions

#### `IsolationOpts` — new field

```go
// ProjectName is the project name from config, used for image naming.
ProjectName string

// ExecutorImage overrides the sandbox image for this task.
// Set from ProjectConfig.ExecutorImage when available.
ExecutorImage string
```

#### Image resolution function

```go
// resolveExecutorImage determines which container image to use for a task.
// Priority: opts.ExecutorImage > auto-built image > config.Image (global default).
// When auto-build is enabled and no image exists, builds from Dockerfile.executor.
func (p *OpenSandboxIsolationProvider) resolveExecutorImage(
    ctx context.Context, opts IsolationOpts,
) (string, error)
```

#### Image build function

```go
// buildExecutorImage builds a project-specific executor image from Dockerfile.executor.
// Image is tagged as pilot-executor/<project-name>:latest.
func buildExecutorImage(ctx context.Context, projectPath, projectName, dockerfilePath string) (string, error)
```

Uses `docker build` via `LocalCommandRunner` (not the sandbox — build happens on the host).

#### Image existence check

```go
// executorImageExists checks if a Docker image exists locally.
func executorImageExists(ctx context.Context, imageName string) bool
```

Uses `docker image inspect <name>` — exit code 0 means exists.

### Runner wiring

`runner.go` line ~983 where `provider.Prepare()` is called — the runner already has access to the task's project config. Plumb `ProjectName` and `ExecutorImage` into `IsolationOpts`:

```go
env, err := provider.Prepare(ctx, IsolationOpts{
    TaskID:        task.ID,
    ProjectPath:   task.ProjectPath,
    Branch:        task.Branch,
    BaseBranch:    "",
    ProjectName:   projectName,     // from project config
    ExecutorImage: executorImage,   // from project config
})
```

Project config lookup: the runner has `*BackendConfig`, not the full app config. Two fields need resolving:

1. **ProjectName:** Derived from `filepath.Base(task.ProjectPath)` in the runner. No config lookup needed — the directory basename is a reasonable image name.
2. **ExecutorImage:** Added as a new field on `Task` struct. The orchestrator (which has full config access) sets it from `ProjectConfig.ExecutorImage` when constructing the task. If unset, the runner leaves it empty and auto-build logic kicks in.

This keeps the runner decoupled from `config.Config` while giving the orchestrator control over per-project image overrides.

### Example Dockerfile.executor for a Go project

```dockerfile
FROM pilot/executor:latest

# Copy project source
COPY . /workspace
WORKDIR /workspace

# Pre-install Go dependencies
RUN go mod download

# Pre-build (optional — warms build cache)
RUN go build ./... || true
```

### Example Dockerfile.executor for a Node.js project

```dockerfile
FROM pilot/executor:latest

COPY . /workspace
WORKDIR /workspace

RUN npm ci
```

### Example config.yaml

```yaml
# Global OpenSandbox config (unchanged)
executor:
  isolation:
    type: opensandbox
    opensandbox:
      server_url: "${OPENSANDBOX_SERVER_URL}"
      api_key: "${OPENSANDBOX_API_KEY}"
      image: "pilot/executor:latest"    # fallback for projects without Dockerfile.executor
      auto_build: true                  # default
      dockerfile_path: "Dockerfile.executor"  # default

# Per-project override (optional)
projects:
  - name: "my-app"
    path: "/home/user/projects/my-app"
    # executor_image: "custom/image:v2"  # skip auto-build, use this directly
```

## Error handling

- **Docker not available:** If `docker` CLI not found, log warning and fall back to generic image + clone. Don't fail the task.
- **Build failure:** Log full build output, fall back to generic image + clone. Emit a warning event so the user knows.
- **Stale image:** Images can go stale as deps change. User can rebuild with `make docker-build-executor` or delete the image to trigger auto-rebuild. No automatic staleness detection in v1.
- **Concurrent builds:** Use a sync.Mutex on the build path per project name to prevent concurrent builds of the same image.

## Testing

- **Unit test:** `resolveExecutorImage` with mock docker check — returns correct image for each scenario (override, auto-built exists, auto-build needed, fallback)
- **Unit test:** Smart clone detection — verify `git fetch + checkout` path when `/workspace/.git` exists
- **Unit test:** Image name sanitization — special chars, uppercase, spaces
- **Integration test:** Full flow with real Docker (gated behind `OPENSANDBOX_INTEGRATION=1`) — build from Dockerfile.executor, start sandbox, verify `/workspace` has repo + deps

## Files to modify

1. `internal/executor/isolation_config.go` — Add `AutoBuild`, `DockerfilePath` fields
2. `internal/executor/isolation.go` — Add `ProjectName`, `ExecutorImage` to `IsolationOpts`
3. `internal/executor/isolation_opensandbox.go` — Image resolution, auto-build, smart clone
4. `internal/executor/runner.go` — Plumb project name + executor image into `IsolationOpts`
5. `internal/config/config.go` — Add `ExecutorImage` to `ProjectConfig`
6. `configs/pilot.example.yaml` — Document new fields
7. `internal/executor/isolation_opensandbox_integration_test.go` — Extend integration tests
8. `internal/executor/isolation_test.go` — Unit tests for image resolution

## Out of scope (v1)

- Automatic image rebuilding when Dockerfile.executor or deps change
- Remote image registry push/pull
- Multi-stage build optimizations
- Image layer caching strategies
- `pilot build-executor` CLI command (can add later)
