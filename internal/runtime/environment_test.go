package runtime

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestEnvironmentParseValidContract(t *testing.T) {
	data := []byte(`
version: 1
sandbox:
  image:
    uri: ghcr.io/acme/api-agent:2026-06-22
  entrypoint:
    - tail
    - -f
    - /dev/null
  timeout: 3600
  metadata:
    project: acme-api
pilot:
  workspace:
    path: /workspace
  checkout:
    strategy: clone
    depth: 0
  setup:
    - go mod download
  verify:
    - make test
  secrets:
    required:
      - GITHUB_TOKEN
      - ANTHROPIC_API_KEY
    optional:
      - NPM_TOKEN
`)

	env, err := ParseEnvironment(data)
	if err != nil {
		t.Fatalf("ParseEnvironment returned error: %v", err)
	}

	if env.Version != 1 {
		t.Fatalf("Version = %d, want 1", env.Version)
	}
	if env.Pilot.Workspace.Path != "/workspace" {
		t.Fatalf("workspace path = %q, want /workspace", env.Pilot.Workspace.Path)
	}
	if env.Pilot.Checkout.Strategy != "clone" {
		t.Fatalf("checkout strategy = %q, want clone", env.Pilot.Checkout.Strategy)
	}
	if got := env.Sandbox["timeout"]; got != 3600 {
		t.Fatalf("sandbox timeout = %#v, want 3600", got)
	}
	if got := env.RequiredSecrets(); !reflect.DeepEqual(got, []string{"GITHUB_TOKEN", "ANTHROPIC_API_KEY"}) {
		t.Fatalf("RequiredSecrets() = %#v", got)
	}
}

func TestEnvironmentLoadMissingFile(t *testing.T) {
	_, err := LoadEnvironment(t.TempDir())
	if !errors.Is(err, ErrEnvironmentNotFound) {
		t.Fatalf("LoadEnvironment missing file err = %v, want ErrEnvironmentNotFound", err)
	}
}

func TestEnvironmentLoadFromRepoPath(t *testing.T) {
	dir := t.TempDir()
	envPath := filepath.Join(dir, ".pilot")
	if err := os.MkdirAll(envPath, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(filepath.Join(envPath, "environment.yaml"), []byte(`
version: 1
sandbox:
  snapshotId: snap_123
pilot:
  workspace:
    path: /workspace
  checkout:
    strategy: clone
`), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	env, err := LoadEnvironment(dir)
	if err != nil {
		t.Fatalf("LoadEnvironment returned error: %v", err)
	}
	if env.Sandbox["snapshotId"] != "snap_123" {
		t.Fatalf("snapshotId = %#v, want snap_123", env.Sandbox["snapshotId"])
	}
}

func TestEnvironmentValidateRejectsUnsupportedVersion(t *testing.T) {
	_, err := ParseEnvironment([]byte(`
version: 2
sandbox:
  image:
    uri: alpine:3.20
pilot:
  workspace:
    path: /workspace
  checkout:
    strategy: clone
`))
	if err == nil || !strings.Contains(err.Error(), "unsupported environment version") {
		t.Fatalf("ParseEnvironment err = %v, want unsupported version", err)
	}
}

func TestEnvironmentValidateRejectsMissingWorkspacePath(t *testing.T) {
	_, err := ParseEnvironment([]byte(`
version: 1
sandbox:
  image:
    uri: alpine:3.20
pilot:
  checkout:
    strategy: clone
`))
	if err == nil || !strings.Contains(err.Error(), "pilot.workspace.path is required") {
		t.Fatalf("ParseEnvironment err = %v, want missing workspace path", err)
	}
}

func TestEnvironmentValidateRejectsNonCloneCheckout(t *testing.T) {
	_, err := ParseEnvironment([]byte(`
version: 1
sandbox:
  image:
    uri: alpine:3.20
pilot:
  workspace:
    path: /workspace
  checkout:
    strategy: upload
`))
	if err == nil || !strings.Contains(err.Error(), "pilot.checkout.strategy must be clone") {
		t.Fatalf("ParseEnvironment err = %v, want non-clone checkout rejection", err)
	}
}

func TestEnvironmentValidateRejectsMissingImageAndSnapshot(t *testing.T) {
	_, err := ParseEnvironment([]byte(`
version: 1
sandbox:
  timeout: 3600
pilot:
  workspace:
    path: /workspace
  checkout:
    strategy: clone
`))
	if err == nil || !strings.Contains(err.Error(), "sandbox.image or sandbox.snapshotId is required") {
		t.Fatalf("ParseEnvironment err = %v, want missing startup source", err)
	}
}

func TestEnvironmentValidateRejectsImageWithoutEntrypoint(t *testing.T) {
	_, err := ParseEnvironment([]byte(`
version: 1
sandbox:
  image:
    uri: alpine:3.20
pilot:
  workspace:
    path: /workspace
  checkout:
    strategy: clone
`))
	if err == nil || !strings.Contains(err.Error(), "sandbox.entrypoint is required when sandbox.image is provided") {
		t.Fatalf("ParseEnvironment err = %v, want missing entrypoint", err)
	}
}
