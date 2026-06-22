package main

import (
	"testing"

	"github.com/qf-studio/pilot/internal/executor"
)

func TestSupportedBackendsIncludesCodexCLI(t *testing.T) {
	var found bool
	for _, backend := range supportedBackends {
		if backend.Name == executor.BackendTypeCodexCLI {
			found = true
			if backend.Command != "codex" {
				t.Errorf("Codex command = %q, want codex", backend.Command)
			}
			if backend.ConfigKey != "codex_cli" {
				t.Errorf("Codex ConfigKey = %q, want codex_cli", backend.ConfigKey)
			}
		}
	}
	if !found {
		t.Fatal("supportedBackends missing codex-cli")
	}
}

func TestBackendValidTypesIncludesCodexCLI(t *testing.T) {
	if !isSupportedBackendType(executor.BackendTypeCodexCLI) {
		t.Fatal("codex-cli should be a valid backend type")
	}
}

func TestDetectBackendsIncludesCodexCLI(t *testing.T) {
	backends := detectBackends()
	var found bool
	for _, backend := range backends {
		if backend.Type == executor.BackendTypeCodexCLI {
			found = true
			if backend.Name != "Codex CLI" {
				t.Errorf("Codex backend name = %q, want Codex CLI", backend.Name)
			}
			if backend.CLICommand != "codex" {
				t.Errorf("Codex CLI command = %q, want codex", backend.CLICommand)
			}
		}
	}
	if !found {
		t.Fatal("detectBackends missing codex-cli")
	}
}
