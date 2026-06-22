package runtime

import (
	"testing"
	"time"
)

func TestRuntimeConfigDefaultsToHost(t *testing.T) {
	cfg := DefaultConfig()

	if cfg.Provider != ProviderHost {
		t.Fatalf("Provider = %q, want %q", cfg.Provider, ProviderHost)
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate default config: %v", err)
	}
}

func TestRuntimeConfigValidateRejectsInvalidProvider(t *testing.T) {
	cfg := &Config{Provider: Provider("docker")}

	err := cfg.Validate()
	if err == nil {
		t.Fatal("Validate returned nil, want invalid provider error")
	}
}

func TestRuntimeConfigValidateRequiresOpenSandboxEndpoint(t *testing.T) {
	cfg := &Config{Provider: ProviderOpenSandbox}

	err := cfg.Validate()
	if err == nil {
		t.Fatal("Validate returned nil, want missing endpoint error")
	}
}

func TestRuntimeConfigOpenSandboxValid(t *testing.T) {
	cfg := &Config{
		Provider: ProviderOpenSandbox,
		OpenSandbox: OpenSandboxConfig{
			Endpoint:       "http://opensandbox.local:8080",
			APIKey:         "test-key",
			DefaultTimeout: 45 * time.Minute,
			Cleanup: CleanupConfig{
				AlwaysDelete:    true,
				RetainOnFailure: false,
				RetainFor:       2 * time.Hour,
			},
			Secrets: map[string]SecretMapping{
				"GITHUB_TOKEN": {FromEnv: "GITHUB_TOKEN"},
				"NPM_TOKEN":    {FromEnv: "NPM_TOKEN", Optional: true},
			},
		},
	}

	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate returned error: %v", err)
	}
}

func TestResolveSecretsFromEnvironment(t *testing.T) {
	t.Setenv("TEST_GITHUB_TOKEN", "gh-test-token")

	cfg := &Config{
		Provider: ProviderOpenSandbox,
		OpenSandbox: OpenSandboxConfig{
			Endpoint: "http://opensandbox.local:8080",
			Secrets: map[string]SecretMapping{
				"GITHUB_TOKEN": {FromEnv: "TEST_GITHUB_TOKEN"},
				"NPM_TOKEN":    {FromEnv: "TEST_NPM_TOKEN", Optional: true},
			},
		},
	}

	values, err := cfg.ResolveSecrets([]string{"GITHUB_TOKEN", "NPM_TOKEN"})
	if err != nil {
		t.Fatalf("ResolveSecrets returned error: %v", err)
	}
	if values["GITHUB_TOKEN"] != "gh-test-token" {
		t.Fatalf("GITHUB_TOKEN = %q, want gh-test-token", values["GITHUB_TOKEN"])
	}
	if _, ok := values["NPM_TOKEN"]; ok {
		t.Fatal("optional unset NPM_TOKEN should not be included")
	}
}

func TestResolveSecretsMissingRequiredMapping(t *testing.T) {
	cfg := DefaultConfig()

	_, err := cfg.ResolveSecrets([]string{"GITHUB_TOKEN"})
	if err == nil {
		t.Fatal("ResolveSecrets returned nil, want missing mapping error")
	}
}

func TestResolveSecretsMissingRequiredValue(t *testing.T) {
	cfg := &Config{
		Provider: ProviderOpenSandbox,
		OpenSandbox: OpenSandboxConfig{
			Endpoint: "http://opensandbox.local:8080",
			Secrets: map[string]SecretMapping{
				"GITHUB_TOKEN": {FromEnv: "TEST_MISSING_GITHUB_TOKEN"},
			},
		},
	}

	_, err := cfg.ResolveSecrets([]string{"GITHUB_TOKEN"})
	if err == nil {
		t.Fatal("ResolveSecrets returned nil, want missing value error")
	}
}
