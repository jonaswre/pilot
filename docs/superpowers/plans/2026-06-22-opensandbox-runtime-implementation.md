# OpenSandbox Runtime Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Implement the approved OpenSandbox runtime design so Pilot can run tasks in repo-declared OpenSandbox sandboxes while preserving current host behavior.

**Architecture:** Add a runtime layer below `Runner` and above backend process execution. The repo contract parser loads `.pilot/environment.yaml`; global config selects `host` or `opensandbox`; OpenSandbox mode creates a short-lived sandbox, clones the repo inside it, runs setup/backend/verify commands through Execd, pushes the branch, and tears down according to cleanup policy.

**Tech Stack:** Go, YAML via `gopkg.in/yaml.v3`, net/http, OpenSandbox lifecycle API, OpenSandbox Execd API, existing Pilot executor/config/health packages.

---

## Baseline Evidence

Baseline command:

```bash
go test ./...
```

Observed pre-existing failures before feature code:

- `github.com/qf-studio/pilot/desktop`: git graph tests assume a different commit history.
- `github.com/qf-studio/pilot/internal/executor`: `TestAnthropicBackend_IsAvailable` depends on local API-key state; other executor tests interact with local Claude/preflight behavior.

Use targeted package tests during implementation and rerun the full suite at the end to identify new regressions separately from baseline failures.

## File Structure

- Create `internal/runtime/environment.go`: `.pilot/environment.yaml` types, loading, validation, secret requirements, OpenSandbox sandbox payload passthrough.
- Create `internal/runtime/environment_test.go`: parser and validation TDD coverage.
- Create `internal/runtime/config.go`: global runtime config types, defaults, secret resolution.
- Create `internal/runtime/config_test.go`: defaults and secret resolution tests.
- Create `internal/runtime/opensandbox_client.go`: lifecycle + Execd HTTP client interface and implementation.
- Create `internal/runtime/opensandbox_client_test.go`: fake-server request/response/SSE tests.
- Create `internal/runtime/runtime.go`: provider interfaces, command execution types, OpenSandbox runtime orchestration.
- Create `internal/runtime/runtime_test.go`: runtime lifecycle tests using fake client.
- Modify `internal/config/config.go`: add `Runtime *runtime.Config` to global config and validate it.
- Modify `internal/config/config_test.go`: YAML load/default/validation tests.
- Modify `internal/executor/backend.go`: add command-builder support for CLI backends and optional remote command execution metadata.
- Modify `internal/executor/backend_claudecode.go`: expose command construction and stream parser for remote execution.
- Modify `internal/executor/backend_qwencode.go`: expose command construction and stream parser for remote execution.
- Modify `internal/executor/runner.go`: select runtime path; preserve host behavior; bypass local worktree for OpenSandbox mode.
- Modify `internal/health/health.go`: add runtime doctor checks.
- Modify `cmd/pilot/doctor.go`: display runtime checks already surfaced by health report.
- Modify `configs/pilot.example.yaml`: document global runtime config.
- Create `configs/environment.example.yaml`: repo environment contract example.
- Modify docs under `docs/content/getting-started/configuration.mdx` or deployment docs with runtime configuration notes.

## Task 1: Environment Contract Parser

**Files:**
- Create: `internal/runtime/environment.go`
- Create: `internal/runtime/environment_test.go`

- [ ] **Step 1: Write failing parser tests**

Add tests for valid OpenSandbox passthrough, missing file behavior, unsupported version, missing workspace path, non-clone checkout, missing sandbox image/snapshot, and secret declarations.

Run:

```bash
go test ./internal/runtime -run 'TestEnvironment' -count=1
```

Expected: fails because `internal/runtime` does not exist.

- [ ] **Step 2: Implement environment types and loader**

Implement:

```go
const EnvironmentFilePath = ".pilot/environment.yaml"
const SupportedEnvironmentVersion = 1

type Environment struct {
    Version int `yaml:"version"`
    Sandbox map[string]any `yaml:"sandbox"`
    Pilot PilotEnvironment `yaml:"pilot"`
}

type PilotEnvironment struct {
    Workspace WorkspaceConfig `yaml:"workspace"`
    Checkout CheckoutConfig `yaml:"checkout"`
    Setup []string `yaml:"setup"`
    Verify []string `yaml:"verify"`
    Secrets SecretRequirements `yaml:"secrets"`
}

type WorkspaceConfig struct {
    Path string `yaml:"path"`
}

type CheckoutConfig struct {
    Strategy string `yaml:"strategy"`
    Depth int `yaml:"depth"`
}

type SecretRequirements struct {
    Required []string `yaml:"required"`
    Optional []string `yaml:"optional"`
}
```

Expose:

```go
func LoadEnvironment(repoPath string) (*Environment, error)
func ParseEnvironment(data []byte) (*Environment, error)
func (e *Environment) Validate() error
func (e *Environment) RequiredSecrets() []string
```

- [ ] **Step 3: Verify parser tests pass**

Run:

```bash
go test ./internal/runtime -run 'TestEnvironment' -count=1
```

Expected: pass.

## Task 2: Global Runtime Config

**Files:**
- Create: `internal/runtime/config.go`
- Create: `internal/runtime/config_test.go`
- Modify: `internal/config/config.go`
- Modify: `internal/config/config_test.go`

- [ ] **Step 1: Write failing config tests**

Add tests proving:

- default provider is `host`
- `runtime.provider: opensandbox` loads endpoint/API key/cleanup/secrets
- invalid provider fails validation
- OpenSandbox requires endpoint
- required secrets resolve from environment variables

Run:

```bash
go test ./internal/runtime ./internal/config -run 'TestRuntime|TestConfigRuntime' -count=1
```

Expected: fail because runtime config does not exist.

- [ ] **Step 2: Implement runtime config types**

Add:

```go
type Provider string

const (
    ProviderHost Provider = "host"
    ProviderOpenSandbox Provider = "opensandbox"
)

type Config struct {
    Provider Provider `yaml:"provider"`
    OpenSandbox OpenSandboxConfig `yaml:"opensandbox,omitempty"`
}

type OpenSandboxConfig struct {
    Endpoint string `yaml:"endpoint,omitempty"`
    APIKey string `yaml:"api_key,omitempty"`
    DefaultTimeout time.Duration `yaml:"default_timeout,omitempty"`
    Cleanup CleanupConfig `yaml:"cleanup,omitempty"`
    Secrets map[string]SecretMapping `yaml:"secrets,omitempty"`
    FallbackSandbox map[string]any `yaml:"fallback_sandbox,omitempty"`
}
```

Wire `Runtime *runtime.Config` into `config.Config`, `DefaultConfig`, and `Validate`.

- [ ] **Step 3: Verify config tests pass**

Run:

```bash
go test ./internal/runtime ./internal/config -run 'TestRuntime|TestConfigRuntime' -count=1
```

Expected: pass.

## Task 3: OpenSandbox HTTP Client

**Files:**
- Create: `internal/runtime/opensandbox_client.go`
- Create: `internal/runtime/opensandbox_client_test.go`

- [ ] **Step 1: Write failing fake-server tests**

Cover:

- `POST /v1/sandboxes` includes `OPEN-SANDBOX-API-KEY`
- poll `GET /v1/sandboxes/{id}` until `status.state == "Running"`
- delete calls `DELETE /v1/sandboxes/{id}`
- `POST /command` parses SSE `stdout`, `stderr`, `result`, and `execution_complete`

Run:

```bash
go test ./internal/runtime -run 'TestOpenSandboxClient' -count=1
```

Expected: fail because client does not exist.

- [ ] **Step 2: Implement lifecycle and Execd client**

Use OpenSandbox lifecycle API:

```text
POST   /v1/sandboxes
GET    /v1/sandboxes/{sandboxId}
DELETE /v1/sandboxes/{sandboxId}
```

Use Execd API:

```text
POST /command
```

Represent command output as:

```go
type CommandResult struct {
    Stdout string
    Stderr string
    ExitCode int
    Success bool
    Events []CommandEvent
}
```

- [ ] **Step 3: Verify client tests pass**

Run:

```bash
go test ./internal/runtime -run 'TestOpenSandboxClient' -count=1
```

Expected: pass.

## Task 4: Runtime Orchestration

**Files:**
- Create: `internal/runtime/runtime.go`
- Create: `internal/runtime/runtime_test.go`

- [ ] **Step 1: Write failing runtime tests**

Cover:

- host provider returns current local project path and no sandbox.
- OpenSandbox provider creates sandbox, clones repo, checks out branch, runs setup.
- setup failure prevents backend invocation.
- verify failure marks runtime failure after backend.
- cleanup deletes sandbox on success and failure when configured.
- missing `.pilot/environment.yaml` fails in OpenSandbox mode.

Run:

```bash
go test ./internal/runtime -run 'TestRuntime' -count=1
```

Expected: fail because orchestrator does not exist.

- [ ] **Step 2: Implement runtime provider interface**

Implement:

```go
type ExecutionRequest struct {
    TaskID string
    ProjectPath string
    RepoURL string
    Branch string
    BaseBranch string
}

type PreparedExecution struct {
    WorkspacePath string
    SandboxID string
    RunCommand func(context.Context, CommandSpec) (*CommandResult, error)
    Cleanup func(context.Context) error
}

type Provider interface {
    Prepare(context.Context, ExecutionRequest) (*PreparedExecution, error)
}
```

OpenSandbox `Prepare` should:

1. load and validate environment contract
2. resolve secrets
3. merge Pilot metadata/env into sandbox payload
4. create sandbox and wait for running
5. run `git clone`, branch checkout/create, and setup commands

- [ ] **Step 3: Verify runtime tests pass**

Run:

```bash
go test ./internal/runtime -run 'TestRuntime' -count=1
```

Expected: pass.

## Task 5: Backend Remote Command Support

**Files:**
- Modify: `internal/executor/backend.go`
- Modify: `internal/executor/backend_claudecode.go`
- Modify: `internal/executor/backend_qwencode.go`
- Modify: `internal/executor/backend_claudecode_test.go`
- Modify: `internal/executor/backend_qwencode_test.go`

- [ ] **Step 1: Write failing backend command-builder tests**

Cover Claude Code and Qwen Code command construction for local and remote execution:

- command name
- args include prompt, verbose, stream-json, max turns, model, effort, allowed tools
- env contains `PILOT_EXECUTOR=1` and configured provider env

Run:

```bash
go test ./internal/executor -run 'Test.*CommandSpec' -count=1
```

Expected: fail because command spec extraction does not exist.

- [ ] **Step 2: Extract CLI command specs**

Add an internal command representation:

```go
type CommandSpec struct {
    Command string
    Args []string
    Dir string
    Env []string
}
```

Add backend methods or helpers that build `CommandSpec` without starting a local process. Keep local execution behavior unchanged.

- [ ] **Step 3: Verify backend command-builder tests pass**

Run:

```bash
go test ./internal/executor -run 'Test.*CommandSpec' -count=1
```

Expected: pass.

## Task 6: Runner Wiring

**Files:**
- Modify: `internal/executor/runner.go`
- Modify: `internal/executor/runner_test.go`

- [ ] **Step 1: Write failing runner tests**

Cover:

- host runtime still creates worktree when `use_worktree` is true
- OpenSandbox runtime bypasses local worktree
- setup/runtime failure returns a failed execution result before backend execution
- successful remote backend path uses sandbox workspace for git finalization

Run:

```bash
go test ./internal/executor -run 'TestRunner.*Runtime|TestExecute.*Runtime' -count=1
```

Expected: fail because runner runtime wiring does not exist.

- [ ] **Step 2: Wire runtime into Runner**

Add runtime provider to `Runner` and constructors. In `executeWithOptions`, if global runtime provider is OpenSandbox:

1. prepare runtime before branch/worktree code
2. set `executionPath` to sandbox workspace
3. bypass local worktree creation
4. run backend through runtime command executor
5. run verify after backend success
6. cleanup sandbox with retention policy

- [ ] **Step 3: Verify runner runtime tests pass**

Run:

```bash
go test ./internal/executor -run 'TestRunner.*Runtime|TestExecute.*Runtime' -count=1
```

Expected: pass.

## Task 7: Doctor, Examples, And Docs

**Files:**
- Modify: `internal/health/health.go`
- Modify: `internal/health/health_test.go`
- Modify: `configs/pilot.example.yaml`
- Create: `configs/environment.example.yaml`
- Modify: `docs/content/getting-started/configuration.mdx`

- [ ] **Step 1: Write failing doctor tests**

Cover:

- runtime provider appears in config checks
- OpenSandbox provider without endpoint is error
- missing required secret mapping is warning/error
- host provider reports compatibility mode

Run:

```bash
go test ./internal/health -run 'TestRuntime' -count=1
```

Expected: fail because health runtime checks do not exist.

- [ ] **Step 2: Implement checks and examples**

Add runtime config checks and docs examples for:

- global `runtime.provider`
- OpenSandbox endpoint/API key
- cleanup policy
- secret mappings
- `.pilot/environment.yaml` example

- [ ] **Step 3: Verify docs-related tests pass**

Run:

```bash
go test ./internal/health -run 'TestRuntime' -count=1
```

Expected: pass.

## Task 8: Final Verification

**Files:**
- All changed files

- [ ] **Step 1: Run targeted package tests**

Run:

```bash
go test ./internal/runtime ./internal/config ./internal/health ./internal/executor -count=1
```

Expected: pass, except any explicitly documented baseline executor tests that still depend on local environment should be isolated and explained.

- [ ] **Step 2: Run config/docs sanity checks**

Run:

```bash
go test ./cmd/pilot ./internal/wiring -count=1
git diff --check
```

Expected: pass.

- [ ] **Step 3: Run full suite and compare with baseline**

Run:

```bash
go test ./...
```

Expected: no new failures beyond the baseline failures listed above. If new failures appear, fix them before completion.

- [ ] **Step 4: Commit implementation**

Use focused conventional commits:

```bash
git add internal/runtime internal/config internal/executor internal/health cmd configs docs
git commit -m "feat(runtime): add OpenSandbox execution runtime"
```
