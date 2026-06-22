package runtime

import (
	"fmt"
	"os"
	"time"
)

type Provider string

const (
	ProviderHost        Provider = "host"
	ProviderOpenSandbox Provider = "opensandbox"
)

// Config selects where Pilot task execution runs.
type Config struct {
	Provider    Provider          `yaml:"provider"`
	OpenSandbox OpenSandboxConfig `yaml:"opensandbox,omitempty"`
}

type OpenSandboxConfig struct {
	Endpoint        string                   `yaml:"endpoint,omitempty"`
	APIKey          string                   `yaml:"api_key,omitempty"`
	DefaultTimeout  time.Duration            `yaml:"default_timeout,omitempty"`
	Cleanup         CleanupConfig            `yaml:"cleanup,omitempty"`
	Secrets         map[string]SecretMapping `yaml:"secrets,omitempty"`
	FallbackSandbox map[string]any           `yaml:"fallback_sandbox,omitempty"`
}

type CleanupConfig struct {
	AlwaysDelete    bool          `yaml:"always_delete,omitempty"`
	RetainOnFailure bool          `yaml:"retain_on_failure,omitempty"`
	RetainFor       time.Duration `yaml:"retain_for,omitempty"`
}

type SecretMapping struct {
	FromEnv  string `yaml:"from_env,omitempty"`
	Optional bool   `yaml:"optional,omitempty"`
}

func DefaultConfig() *Config {
	return &Config{
		Provider: ProviderHost,
		OpenSandbox: OpenSandboxConfig{
			DefaultTimeout: 45 * time.Minute,
			Cleanup: CleanupConfig{
				AlwaysDelete: true,
			},
			Secrets: map[string]SecretMapping{},
		},
	}
}

func (c *Config) Validate() error {
	if c == nil {
		return nil
	}
	if c.Provider == "" {
		c.Provider = ProviderHost
	}
	switch c.Provider {
	case ProviderHost:
		return nil
	case ProviderOpenSandbox:
		if c.OpenSandbox.Endpoint == "" {
			return fmt.Errorf("runtime.opensandbox.endpoint is required when runtime.provider is %q", ProviderOpenSandbox)
		}
		return nil
	default:
		return fmt.Errorf("runtime.provider must be %q or %q, got %q", ProviderHost, ProviderOpenSandbox, c.Provider)
	}
}

func (c *Config) ResolveSecrets(required []string) (map[string]string, error) {
	if c == nil {
		return nil, fmt.Errorf("runtime config is required to resolve secrets")
	}
	values := make(map[string]string)
	for _, name := range required {
		mapping, ok := c.OpenSandbox.Secrets[name]
		if !ok {
			return nil, fmt.Errorf("runtime secret %q is required but has no mapping", name)
		}
		value := ""
		if mapping.FromEnv != "" {
			value = os.Getenv(mapping.FromEnv)
		}
		if value == "" {
			if mapping.Optional {
				continue
			}
			return nil, fmt.Errorf("runtime secret %q is required but %q is empty", name, mapping.FromEnv)
		}
		values[name] = value
	}
	return values, nil
}
