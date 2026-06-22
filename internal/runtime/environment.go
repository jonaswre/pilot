package runtime

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

const (
	// EnvironmentFilePath is the repo-relative Pilot runtime contract path.
	EnvironmentFilePath = ".pilot/environment.yaml"

	// SupportedEnvironmentVersion is the only contract schema version this build understands.
	SupportedEnvironmentVersion = 1
)

var ErrEnvironmentNotFound = errors.New("environment contract not found")

// Environment is the parsed .pilot/environment.yaml contract.
type Environment struct {
	Version int              `yaml:"version"`
	Sandbox map[string]any   `yaml:"sandbox"`
	Pilot   PilotEnvironment `yaml:"pilot"`
}

// PilotEnvironment contains Pilot-owned lifecycle fields around the OpenSandbox request.
type PilotEnvironment struct {
	Workspace WorkspaceConfig    `yaml:"workspace"`
	Checkout  CheckoutConfig     `yaml:"checkout"`
	Setup     []string           `yaml:"setup"`
	Verify    []string           `yaml:"verify"`
	Secrets   SecretRequirements `yaml:"secrets"`
}

type WorkspaceConfig struct {
	Path string `yaml:"path"`
}

type CheckoutConfig struct {
	Strategy string `yaml:"strategy"`
	Depth    int    `yaml:"depth"`
}

type SecretRequirements struct {
	Required []string `yaml:"required"`
	Optional []string `yaml:"optional"`
}

// LoadEnvironment reads and validates .pilot/environment.yaml from repoPath.
func LoadEnvironment(repoPath string) (*Environment, error) {
	data, err := os.ReadFile(filepath.Join(repoPath, EnvironmentFilePath))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, ErrEnvironmentNotFound
		}
		return nil, fmt.Errorf("read environment contract: %w", err)
	}
	return ParseEnvironment(data)
}

// ParseEnvironment decodes and validates a runtime environment contract.
func ParseEnvironment(data []byte) (*Environment, error) {
	var env Environment
	if err := yaml.Unmarshal(data, &env); err != nil {
		return nil, fmt.Errorf("parse environment contract: %w", err)
	}
	if err := env.Validate(); err != nil {
		return nil, err
	}
	return &env, nil
}

// Validate checks the v1 environment contract rules.
func (e *Environment) Validate() error {
	if e == nil {
		return errors.New("environment contract is nil")
	}
	if e.Version != SupportedEnvironmentVersion {
		return fmt.Errorf("unsupported environment version %d (expected %d)", e.Version, SupportedEnvironmentVersion)
	}
	if e.Pilot.Workspace.Path == "" {
		return errors.New("pilot.workspace.path is required")
	}
	if e.Pilot.Checkout.Strategy == "" {
		e.Pilot.Checkout.Strategy = "clone"
	}
	if e.Pilot.Checkout.Strategy != "clone" {
		return fmt.Errorf("pilot.checkout.strategy must be clone in v1, got %q", e.Pilot.Checkout.Strategy)
	}
	if len(e.Sandbox) == 0 {
		return errors.New("sandbox.image or sandbox.snapshotId is required")
	}

	hasImage := hasSandboxImage(e.Sandbox)
	hasSnapshot := hasNonEmptyString(e.Sandbox, "snapshotId")
	if !hasImage && !hasSnapshot {
		return errors.New("sandbox.image or sandbox.snapshotId is required")
	}
	if hasImage && !hasEntrypoint(e.Sandbox) {
		return errors.New("sandbox.entrypoint is required when sandbox.image is provided")
	}
	return nil
}

func (e *Environment) RequiredSecrets() []string {
	if e == nil {
		return nil
	}
	out := make([]string, len(e.Pilot.Secrets.Required))
	copy(out, e.Pilot.Secrets.Required)
	return out
}

func hasSandboxImage(sandbox map[string]any) bool {
	raw, ok := sandbox["image"]
	if !ok || raw == nil {
		return false
	}
	image, ok := raw.(map[string]any)
	if !ok {
		return true
	}
	return hasNonEmptyString(image, "uri")
}

func hasEntrypoint(sandbox map[string]any) bool {
	raw, ok := sandbox["entrypoint"]
	if !ok || raw == nil {
		return false
	}
	switch value := raw.(type) {
	case []any:
		return len(value) > 0
	case []string:
		return len(value) > 0
	default:
		return false
	}
}

func hasNonEmptyString(m map[string]any, key string) bool {
	value, ok := m[key]
	if !ok {
		return false
	}
	s, ok := value.(string)
	return ok && s != ""
}
