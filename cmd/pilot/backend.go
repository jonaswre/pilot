package main

import (
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"

	"github.com/qf-studio/pilot/internal/config"
	"github.com/qf-studio/pilot/internal/executor"
)

// backendInfo holds information about a supported backend
type backendInfo struct {
	Name       string
	Command    string
	ConfigKey  string
	getVersion func(cmd string) string
}

var supportedBackends = []backendInfo{
	{
		Name:      executor.BackendTypeClaudeCode,
		Command:   "claude",
		ConfigKey: "claude_code",
		getVersion: func(cmd string) string {
			out, err := exec.Command(cmd, "--version").Output()
			if err != nil {
				return ""
			}
			return strings.TrimSpace(string(out))
		},
	},
	{
		Name:      executor.BackendTypeCodexCLI,
		Command:   "codex",
		ConfigKey: "codex_cli",
		getVersion: func(cmd string) string {
			out, err := exec.Command(cmd, "--version").Output()
			if err != nil {
				return ""
			}
			return strings.TrimSpace(string(out))
		},
	},
	{
		Name:      executor.BackendTypeQwenCode,
		Command:   "qwen",
		ConfigKey: "qwen_code",
		getVersion: func(cmd string) string {
			out, err := exec.Command(cmd, "--version").Output()
			if err != nil {
				return ""
			}
			return strings.TrimSpace(string(out))
		},
	},
	{
		Name:      executor.BackendTypeOpenCode,
		Command:   "opencode",
		ConfigKey: "opencode",
		getVersion: func(cmd string) string {
			out, err := exec.Command(cmd, "--version").Output()
			if err != nil {
				return ""
			}
			return strings.TrimSpace(string(out))
		},
	},
}

func newBackendCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "backend",
		Short: "Manage execution backends",
		Long: `Manage AI execution backends (Claude Code, Codex CLI, Qwen Code, OpenCode).

List supported backends, check their status, and switch the active backend.`,
	}

	cmd.AddCommand(
		newBackendListCmd(),
		newBackendStatusCmd(),
		newBackendSetCmd(),
	)

	return cmd
}

func newBackendListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List all supported backends",
		Long: `Show all supported backends and whether their CLI is installed.

Example output:
  Backend        Status      Command    Config
  claude-code    ✓ installed claude     (default)
  codex-cli      ✓ installed codex
  qwen-code      ✗ missing   qwen
  opencode       ✓ installed opencode  `,
		RunE: func(cmd *cobra.Command, args []string) error {
			// Load config to check which is default
			configPath := cfgFile
			if configPath == "" {
				configPath = config.DefaultConfigPath()
			}

			cfg, err := config.Load(configPath)
			if err != nil {
				// Config doesn't exist yet, use defaults
				cfg = config.DefaultConfig()
			}

			activeBackend := executor.BackendTypeClaudeCode
			if cfg.Executor != nil && cfg.Executor.Type != "" {
				activeBackend = cfg.Executor.Type
			}

			// Print header
			fmt.Printf("%-14s %-12s %-10s %s\n", "Backend", "Status", "Command", "Config")

			for _, backend := range supportedBackends {
				// Get the actual command from config or use default
				command := backend.Command
				if cfg.Executor != nil {
					switch backend.Name {
					case executor.BackendTypeClaudeCode:
						if cfg.Executor.ClaudeCode != nil && cfg.Executor.ClaudeCode.Command != "" {
							command = cfg.Executor.ClaudeCode.Command
						}
					case executor.BackendTypeQwenCode:
						if cfg.Executor.QwenCode != nil && cfg.Executor.QwenCode.Command != "" {
							command = cfg.Executor.QwenCode.Command
						}
					case executor.BackendTypeCodexCLI:
						if cfg.Executor.CodexCLI != nil && cfg.Executor.CodexCLI.Command != "" {
							command = cfg.Executor.CodexCLI.Command
						}
					case executor.BackendTypeOpenCode:
						// OpenCode uses server command
						if cfg.Executor.OpenCode != nil && cfg.Executor.OpenCode.ServerCommand != "" {
							parts := strings.Fields(cfg.Executor.OpenCode.ServerCommand)
							if len(parts) > 0 {
								command = parts[0]
							}
						}
					}
				}

				// Check if installed
				_, err := exec.LookPath(command)
				installed := err == nil

				status := "✗ missing"
				if installed {
					status = "✓ installed"
				}

				configNote := ""
				if backend.Name == activeBackend {
					configNote = "(default)"
				}

				fmt.Printf("%-14s %-12s %-10s %s\n", backend.Name, status, command, configNote)
			}

			return nil
		},
	}
}

func newBackendStatusCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Show current backend configuration",
		Long: `Show current backend configuration and health.

Example output:
  Active backend: claude-code
  Command: claude
  Version: 1.0.26
  Status: ✓ ready`,
		RunE: func(cmd *cobra.Command, args []string) error {
			configPath := cfgFile
			if configPath == "" {
				configPath = config.DefaultConfigPath()
			}

			cfg, err := config.Load(configPath)
			if err != nil {
				cfg = config.DefaultConfig()
			}

			activeBackend := executor.BackendTypeClaudeCode
			if cfg.Executor != nil && cfg.Executor.Type != "" {
				activeBackend = cfg.Executor.Type
			}

			// Find backend info
			var info *backendInfo
			for i := range supportedBackends {
				if supportedBackends[i].Name == activeBackend {
					info = &supportedBackends[i]
					break
				}
			}

			if info == nil {
				return fmt.Errorf("unknown backend type: %s", activeBackend)
			}

			// Get the actual command from config
			command := info.Command
			if cfg.Executor != nil {
				switch activeBackend {
				case executor.BackendTypeClaudeCode:
					if cfg.Executor.ClaudeCode != nil && cfg.Executor.ClaudeCode.Command != "" {
						command = cfg.Executor.ClaudeCode.Command
					}
				case executor.BackendTypeQwenCode:
					if cfg.Executor.QwenCode != nil && cfg.Executor.QwenCode.Command != "" {
						command = cfg.Executor.QwenCode.Command
					}
				case executor.BackendTypeCodexCLI:
					if cfg.Executor.CodexCLI != nil && cfg.Executor.CodexCLI.Command != "" {
						command = cfg.Executor.CodexCLI.Command
					}
				case executor.BackendTypeOpenCode:
					if cfg.Executor.OpenCode != nil && cfg.Executor.OpenCode.ServerCommand != "" {
						parts := strings.Fields(cfg.Executor.OpenCode.ServerCommand)
						if len(parts) > 0 {
							command = parts[0]
						}
					}
				}
			}

			// Check installation and version
			_, lookErr := exec.LookPath(command)
			installed := lookErr == nil

			version := ""
			if installed {
				version = info.getVersion(command)
			}

			status := "✗ not ready (CLI not found)"
			if installed {
				status = "✓ ready"
			}

			fmt.Printf("Active backend: %s\n", activeBackend)
			fmt.Printf("Command: %s\n", command)
			if version != "" {
				fmt.Printf("Version: %s\n", version)
			}
			fmt.Printf("Status: %s\n", status)
			fmt.Println()
			fmt.Printf("Config: %s\n", configPath)
			fmt.Printf("  executor.type: %s\n", activeBackend)

			// Show backend-specific config
			if cfg.Executor != nil {
				switch activeBackend {
				case executor.BackendTypeClaudeCode:
					if cfg.Executor.ClaudeCode != nil {
						fmt.Printf("  executor.claude_code.command: %s\n", command)
					}
				case executor.BackendTypeQwenCode:
					if cfg.Executor.QwenCode != nil {
						fmt.Printf("  executor.qwen_code.command: %s\n", command)
					}
				case executor.BackendTypeCodexCLI:
					if cfg.Executor.CodexCLI != nil {
						fmt.Printf("  executor.codex_cli.command: %s\n", command)
					}
				case executor.BackendTypeOpenCode:
					if cfg.Executor.OpenCode != nil {
						if cfg.Executor.OpenCode.ServerURL != "" {
							fmt.Printf("  executor.opencode.server_url: %s\n", cfg.Executor.OpenCode.ServerURL)
						}
					}
				}
			}

			return nil
		},
	}
}

func newBackendSetCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "set <type>",
		Short: "Set active backend",
		Long: `Switch the active backend in the config file.

Valid types: claude-code, codex-cli, qwen-code, opencode

Example:
  pilot backend set qwen-code
  → Updated executor.type to "qwen-code" in ~/.pilot/config.yaml
  → Verified: qwen CLI found at /usr/local/bin/qwen`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			backendType := args[0]

			if !isSupportedBackendType(backendType) {
				return fmt.Errorf("invalid backend type: %s\nValid types: %s", backendType, strings.Join(supportedBackendTypes(), ", "))
			}

			configPath := cfgFile
			if configPath == "" {
				configPath = config.DefaultConfigPath()
			}

			// Load existing config
			cfg, err := config.Load(configPath)
			if err != nil {
				// If config doesn't exist, create a default one
				cfg = config.DefaultConfig()
			}

			// Update the backend type
			if cfg.Executor == nil {
				cfg.Executor = executor.DefaultBackendConfig()
			}
			cfg.Executor.Type = backendType

			// Write config back
			data, err := yaml.Marshal(cfg)
			if err != nil {
				return fmt.Errorf("failed to marshal config: %w", err)
			}

			if err := os.WriteFile(configPath, data, 0600); err != nil {
				return fmt.Errorf("failed to write config: %w", err)
			}

			fmt.Printf("Updated executor.type to %q in %s\n", backendType, configPath)

			// Verify CLI is installed
			var info *backendInfo
			for i := range supportedBackends {
				if supportedBackends[i].Name == backendType {
					info = &supportedBackends[i]
					break
				}
			}

			if info != nil {
				command := info.Command
				// Get custom command from config if set
				switch backendType {
				case executor.BackendTypeClaudeCode:
					if cfg.Executor.ClaudeCode != nil && cfg.Executor.ClaudeCode.Command != "" {
						command = cfg.Executor.ClaudeCode.Command
					}
				case executor.BackendTypeQwenCode:
					if cfg.Executor.QwenCode != nil && cfg.Executor.QwenCode.Command != "" {
						command = cfg.Executor.QwenCode.Command
					}
				case executor.BackendTypeCodexCLI:
					if cfg.Executor.CodexCLI != nil && cfg.Executor.CodexCLI.Command != "" {
						command = cfg.Executor.CodexCLI.Command
					}
				case executor.BackendTypeOpenCode:
					if cfg.Executor.OpenCode != nil && cfg.Executor.OpenCode.ServerCommand != "" {
						parts := strings.Fields(cfg.Executor.OpenCode.ServerCommand)
						if len(parts) > 0 {
							command = parts[0]
						}
					}
				}

				path, err := exec.LookPath(command)
				if err != nil {
					fmt.Printf("Warning: %s CLI not found in PATH\n", command)
				} else {
					fmt.Printf("Verified: %s CLI found at %s\n", command, path)
				}
			}

			return nil
		},
	}
}

func supportedBackendTypes() []string {
	types := make([]string, 0, len(supportedBackends))
	for _, backend := range supportedBackends {
		types = append(types, backend.Name)
	}
	return types
}

func isSupportedBackendType(backendType string) bool {
	for _, supported := range supportedBackendTypes() {
		if backendType == supported {
			return true
		}
	}
	return false
}
