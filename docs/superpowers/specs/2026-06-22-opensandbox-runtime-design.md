# OpenSandbox Runtime Design

Date: 2026-06-22

## Summary

Pilot should add a repo-authored execution environment contract and an OpenSandbox-backed runtime path so autonomous tasks run in fresh, reproducible sandboxes instead of depending on the daemon host. The first sandbox implementation should use OpenSandbox as the runtime abstraction. Docker and Kubernetes are OpenSandbox deployment/runtime choices, not direct Pilot providers in v1.

Pilot keeps `host` as the compatibility runtime. Existing installs continue to work without `.pilot/environment.yaml`; OpenSandbox mode requires the repo contract unless an operator explicitly configures a fallback sandbox.

## Goals

- Give each agent task a fresh execution environment with declared prerequisites.
- Keep repo requirements in the repo, committed and reviewable.
- Use OpenSandbox's native sandbox request shape instead of inventing a competing Pilot schema.
- Support local and production deployments through OpenSandbox backends, including Docker and Kubernetes.
- Keep agent/backend behavior separate from runtime behavior.
- Fail before agent execution when the environment contract or runtime setup is invalid.

## Non-Goals For V1

- Direct Docker provider in Pilot.
- Direct Kubernetes Job/Pod provider in Pilot.
- Pilot-built runtime images.
- Pilot-managed sidecar services.
- Bind-mounting local worktrees into sandboxes.
- Per-project runtime provider selection.
- Model, backend, commit, PR, or workflow policy in `.pilot/environment.yaml`.

## Runtime Modes

Pilot supports two runtime modes in v1:

```text
host
  Compatibility mode. Uses existing local execution and optional worktree isolation.

opensandbox
  Primary sandbox mode. Creates one short-lived OpenSandbox sandbox per task.
```

`host` supports existing behavior and may run basic setup and verify commands when safe. It does not support image isolation, service sidecars, network isolation, or resource isolation. If a repo contract requires unsupported features while `host` is selected, Pilot fails closed before execution.

`opensandbox` creates a fresh sandbox per task, clones/fetches the repo inside the sandbox, runs setup, invokes the existing backend command inside the sandbox, runs verification, pushes the branch, and deletes or retains the sandbox according to runtime cleanup policy.

## Architecture

```text
Pilot Runner
  -> Environment Contract Loader
  -> Runtime Orchestrator
  -> OpenSandbox Runtime Adapter
  -> Existing Backend
  -> Git Finalizer
```

The existing `Backend` abstraction remains responsible for Claude Code, Codex, OpenCode, and related invocation semantics. The new runtime layer is responsible for where backend commands execute.

In OpenSandbox mode, backend process execution goes through OpenSandbox command/session APIs instead of local `exec.Command`.

## Repo Contract

Repos declare execution requirements in `.pilot/environment.yaml`. The schema is a thin wrapper around OpenSandbox's `CreateSandboxRequest` shape.

Pilot reads the contract before sandbox creation from the configured project checkout when available. If the execution environment does not have a local project checkout, Pilot reads the file from the source control provider at the task base branch. The contract is resolved from the base branch, not from an agent-created task branch, so a task cannot silently change its own runtime requirements mid-execution.

Example:

```yaml
version: 1

sandbox:
  image:
    uri: ghcr.io/acme/api-agent:2026-06-22
  platform:
    os: linux
    arch: amd64
  timeout: 3600
  resourceLimits:
    cpu: "2"
    memory: "4Gi"
  resourceRequests:
    cpu: "1"
    memory: "2Gi"
  entrypoint:
    - tail
    - -f
    - /dev/null
  env:
    CI: "true"
  metadata:
    project: acme-api
  networkPolicy:
    defaultAction: deny
    egress:
      - action: allow
        target: github.com
      - action: allow
        target: api.github.com
      - action: allow
        target: registry.npmjs.org
  credentialProxy:
    enabled: true
  volumes: []
  extensions: {}

pilot:
  workspace:
    path: /workspace

  checkout:
    strategy: clone
    depth: 0

  setup:
    - go mod download
    - cd web && npm ci

  verify:
    - make test
    - make lint

  secrets:
    required:
      - GITHUB_TOKEN
      - ANTHROPIC_API_KEY
    optional:
      - NPM_TOKEN
```

### Contract Rules

- `version` is the Pilot contract version, not the OpenSandbox API version.
- `sandbox` is passed through to OpenSandbox sandbox creation after validation and secret resolution.
- Pilot should preserve unknown OpenSandbox-compatible fields where possible.
- `pilot.workspace.path` tells Pilot where the repo should live inside the sandbox.
- `pilot.checkout.strategy` supports `clone` in v1.
- `pilot.setup` runs from `pilot.workspace.path` after clone and branch checkout, before the backend starts. It is fail-fast.
- `pilot.verify` runs from `pilot.workspace.path` after backend execution and before final success. It is fail-fast.
- `pilot.secrets` declares logical secret names only. Values are supplied by daemon/runtime config and are never committed to the repo.

First-class service sidecars are deferred until OpenSandbox supports them natively in the sandbox API. Repos that need databases or other local services in v1 can use images that start those dependencies internally or setup commands that launch local processes.

## Global Runtime Config

Runtime selection is global for the Pilot installation, not per project and not repo-authored.

Example `~/.pilot/config.yaml`:

```yaml
runtime:
  provider: opensandbox

  opensandbox:
    endpoint: http://opensandbox-server.opensandbox-system:8080
    api_key: "${OPEN_SANDBOX_API_KEY}"
    default_timeout: 3600
    cleanup:
      always_delete: true
      retain_on_failure: false
      retain_for: 2h

    secrets:
      GITHUB_TOKEN:
        from_env: GITHUB_TOKEN
      ANTHROPIC_API_KEY:
        from_env: ANTHROPIC_API_KEY
      NPM_TOKEN:
        from_env: NPM_TOKEN
        optional: true

    fallback_sandbox: null
```

Provider behavior:

- `host`: current behavior remains, `.pilot/environment.yaml` is optional.
- `opensandbox`: `.pilot/environment.yaml` is required unless `fallback_sandbox` is explicitly configured.

Pilot injects task metadata into `sandbox.metadata`:

- `pilot_task_id`
- `pilot_project`
- `pilot_branch`
- `pilot_source_repo`

Pilot injects runtime environment values:

- `PILOT_REPO_URL`
- `PILOT_BRANCH`
- `PILOT_BASE_BRANCH`
- `PILOT_WORKSPACE`
- resolved secret environment variables

## OpenSandbox Execution Lifecycle

```text
1. Load and validate .pilot/environment.yaml.
2. Resolve required secrets from daemon config.
3. Create OpenSandbox sandbox from sandbox config.
4. Wait until the sandbox is running.
5. Create a shell/session inside the sandbox.
6. Clone the repo into pilot.workspace.path.
7. Checkout or create the task branch.
8. Run pilot.setup commands.
9. Invoke the existing backend command inside the sandbox.
10. Run pilot.verify commands.
11. Inspect git status and latest commit.
12. Push the branch.
13. Continue existing PR creation and notification flow.
14. Delete or retain the sandbox according to cleanup policy.
```

Sandboxed runtimes bypass local worktree creation. The sandbox owns clone, checkout, branch push, and cleanup. The host runtime keeps current local worktree behavior.

## Failure Behavior

Pilot fails closed for unsupported or invalid runtime requirements.

Error categories:

```text
environment_contract_invalid
environment_secret_missing
runtime_unavailable
runtime_capability_unsupported
runtime_sandbox_create_failed
runtime_workspace_setup_failed
runtime_setup_command_failed
backend_failed
runtime_verify_failed
runtime_cleanup_failed
```

Failure rules:

- Invalid contract: fail before sandbox creation.
- Missing secret: fail before sandbox creation.
- Sandbox create/provision failure: fail with `runtime_sandbox_create_failed`.
- Setup failure: fail without invoking the backend.
- Backend failure: preserve existing retry and error classification where possible.
- Verify failure: mark task failed.
- Cleanup failure: record the task result and emit a cleanup warning.

## Progress And Observability

Add runtime-specific progress milestones:

```text
Runtime: loading environment
Runtime: creating sandbox
Runtime: sandbox ready
Workspace: cloning repository
Workspace: checking out branch
Setup: running command N/M
Agent: running backend
Verify: running command N/M
Runtime: deleting sandbox
```

Execution records and logs should include:

- OpenSandbox sandbox ID.
- Sandbox URL when available.
- Separate setup, backend, verify, and cleanup logs.
- Cleanup/retention decision.

On failure, sandbox retention follows global runtime config:

- delete immediately
- retain failed sandboxes for inspection
- retain for a fixed TTL

## Compatibility With Existing Workflow Config

`.pilot/workflow.yaml` remains the per-repo agent/workflow configuration. It continues to own agent settings, policy fields, hooks, and prompt appendix.

`.pilot/environment.yaml` is runtime-only. It must not include model selection, backend selection, commit policy, PR policy, or workflow instructions.

## Doctor And Preflight

`pilot doctor` should report and validate runtime setup:

- selected runtime provider
- OpenSandbox endpoint reachability
- OpenSandbox API authentication
- required repo environment file when provider is `opensandbox`
- required secret mappings
- optional disposable smoke-test sandbox

## Testing

Unit tests:

- parse `.pilot/environment.yaml`
- validate schema version
- validate required sandbox fields
- validate missing secrets
- validate host capability restrictions
- validate OpenSandbox request translation

Runtime adapter tests:

- fake OpenSandbox server
- create sandbox request payload
- wait-for-running behavior
- command execution success and failure
- cleanup on success
- cleanup on failure
- retained sandbox policy

Runner integration tests:

- host runtime remains unchanged
- OpenSandbox runtime bypasses local worktree
- setup failure prevents backend execution
- verify failure marks task failed
- successful task pushes branch and returns commit SHA

Doctor tests:

- missing endpoint
- bad auth
- missing environment file
- missing secret mapping
- smoke sandbox success and failure

## Rollout

Phase 1:

- config structs
- environment parser and validator
- host compatibility behavior
- fake OpenSandbox adapter tests

Phase 2:

- real OpenSandbox adapter
- sandbox lifecycle
- clone/setup/backend/verify execution path
- doctor checks

Phase 3:

- docs and examples
- Helm config docs for Pilot plus OpenSandbox
- migration guide from host/worktree mode

Later:

- service sidecars when OpenSandbox supports them
- snapshot/image cache support
- richer artifact retention
- optional fallback sandbox
