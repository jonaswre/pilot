// Package main provides the onboard backend selection stage.
// GH-1340: Backend selection for pilot onboard command.
package main

import (
	"fmt"
	"os/exec"

	"github.com/qf-studio/pilot/internal/executor"
)

// BackendOption represents an available execution backend
type BackendOption struct {
	Name        string
	Type        string // config value: "claude-code", "qwen-code", "opencode"
	Description string
	CLICommand  string // command to check with exec.LookPath
	Installed   bool
}

// detectBackends checks which backend CLIs are installed
func detectBackends() []BackendOption {
	backends := []BackendOption{
		{
			Name:        "Claude Code",
			Type:        "claude-code",
			Description: "Anthropic's CLI",
			CLICommand:  "claude",
		},
		{
			Name:        "Qwen Code",
			Type:        "qwen-code",
			Description: "Alibaba's open-source CLI",
			CLICommand:  "qwen",
		},
		{
			Name:        "OpenCode",
			Type:        "opencode",
			Description: "Server/client architecture",
			CLICommand:  "opencode",
		},
	}

	// Check which CLIs are installed
	for i := range backends {
		_, err := exec.LookPath(backends[i].CLICommand)
		backends[i].Installed = err == nil
	}

	return backends
}

// onboardBackendSetup runs the backend selection stage
func onboardBackendSetup(state *OnboardState) error {
	printStageHeader("EXECUTION BACKEND", state.CurrentStage, state.StagesTotal)
	fmt.Println()

	backends := detectBackends()

	// Count installed backends
	installedCount := 0
	var singleInstalled *BackendOption
	for i := range backends {
		if backends[i].Installed {
			installedCount++
			singleInstalled = &backends[i]
		}
	}

	// If only one backend is installed, auto-select it
	if installedCount == 1 {
		fmt.Printf("  Detected: %s %s\n",
			onboardSuccessStyle.Render("✓"),
			singleInstalled.Name)
		fmt.Printf("  %s\n", onboardDimStyle.Render(singleInstalled.Description))
		fmt.Println()

		// Initialize executor config if needed
		if state.Config.Executor == nil {
			state.Config.Executor = executor.DefaultBackendConfig()
		}
		state.Config.Executor.Type = singleInstalled.Type

		fmt.Printf("  %s Backend: %s\n",
			onboardSuccessStyle.Render("✓"),
			onboardValueStyle.Render(singleInstalled.Name))

		// Isolation setup
		if err := onboardIsolationSetup(state); err != nil {
			return err
		}

		fmt.Println()
		printStageFooter()
		return nil
	}

	// Show menu
	fmt.Println("  Which AI coding backend should Pilot use?")
	fmt.Println()

	for i, b := range backends {
		status := ""
		if b.Installed {
			status = onboardSuccessStyle.Render(" ✓")
		} else {
			status = onboardDimStyle.Render(" (not installed)")
		}

		defaultMarker := ""
		if i == 0 {
			defaultMarker = onboardDimStyle.Render(" (default)")
		}

		fmt.Printf("    %s %s — %s%s%s\n",
			onboardValueStyle.Render(fmt.Sprintf("[%d]", i+1)),
			b.Name,
			onboardDimStyle.Render(b.Description),
			status,
			defaultMarker)
	}
	fmt.Println()

	// Prompt for selection
	fmt.Printf("  Your choice %s ", onboardCursorStyle.Render("[1]:"))
	line := readLine(state.Reader)

	// Parse selection (default to 1)
	idx := 1
	if line != "" {
		if _, err := fmt.Sscanf(line, "%d", &idx); err != nil || idx < 1 || idx > len(backends) {
			idx = 1
		}
	}

	selected := backends[idx-1]

	// Initialize executor config if needed
	if state.Config.Executor == nil {
		state.Config.Executor = executor.DefaultBackendConfig()
	}
	state.Config.Executor.Type = selected.Type

	// If OpenCode is selected, prompt for server URL
	if selected.Type == "opencode" {
		fmt.Println()
		fmt.Print("  OpenCode server URL [http://localhost:8080]: ")
		serverURL := readLine(state.Reader)
		if serverURL == "" {
			serverURL = "http://localhost:8080"
		}
		if state.Config.Executor.OpenCode == nil {
			state.Config.Executor.OpenCode = &executor.OpenCodeConfig{}
		}
		state.Config.Executor.OpenCode.ServerURL = serverURL
	}

	// Show confirmation
	fmt.Println()
	if !selected.Installed {
		fmt.Printf("  %s %s is not installed. Install it before running Pilot.\n",
			onboardDimStyle.Render("⚠"),
			selected.Name)
	}
	fmt.Printf("  %s Backend: %s\n",
		onboardSuccessStyle.Render("✓"),
		onboardValueStyle.Render(selected.Name))

	// Isolation setup
	if err := onboardIsolationSetup(state); err != nil {
		return err
	}

	fmt.Println()
	printStageFooter()
	return nil
}

// onboardIsolationSetup prompts the user to select an execution isolation mode
// and configures the executor isolation settings accordingly.
func onboardIsolationSetup(state *OnboardState) error {
	fmt.Println()
	fmt.Println("  Execution isolation:")
	fmt.Println()

	isolationOptions := []string{
		"None — run locally (default)",
		"Worktree — git worktree per task",
		"OpenSandbox — sandboxed containers",
	}

	for i, opt := range isolationOptions {
		defaultMarker := ""
		if i == 0 {
			defaultMarker = onboardDimStyle.Render(" (default)")
		}
		fmt.Printf("    %s %s%s\n",
			onboardValueStyle.Render(fmt.Sprintf("[%d]", i+1)),
			opt,
			defaultMarker)
	}
	fmt.Println()

	fmt.Printf("  %s ", onboardCursorStyle.Render("▸"))
	line := readLine(state.Reader)

	idx := 1
	if line != "" {
		if _, err := fmt.Sscanf(line, "%d", &idx); err != nil || idx < 1 || idx > len(isolationOptions) {
			idx = 1
		}
	}

	switch idx {
	case 2:
		// Worktree isolation
		if state.Config.Executor.Isolation == nil {
			state.Config.Executor.Isolation = &executor.IsolationConfig{}
		}
		state.Config.Executor.Isolation.Type = executor.IsolationTypeWorktree
		fmt.Printf("\n  %s Isolation: %s\n",
			onboardSuccessStyle.Render("✓"),
			onboardValueStyle.Render("worktree"))

	case 3:
		// OpenSandbox isolation
		if state.Config.Executor.Isolation == nil {
			state.Config.Executor.Isolation = &executor.IsolationConfig{}
		}
		state.Config.Executor.Isolation.Type = executor.IsolationTypeOpenSandbox

		serverURL := readLineWithDefault(state.Reader, "OpenSandbox server URL", "http://localhost:8080/v1")

		fmt.Print("  API key (optional, or set OPENSANDBOX_API_KEY) ")
		fmt.Printf("%s ", onboardCursorStyle.Render("▸"))
		apiKey := readLine(state.Reader)

		osCfg := executor.DefaultOpenSandboxConfig()
		osCfg.ServerURL = serverURL
		if apiKey != "" {
			osCfg.APIKey = apiKey
		} else {
			osCfg.APIKey = "${OPENSANDBOX_API_KEY}"
		}
		osCfg.Image = "pilot/executor:latest"
		osCfg.Resources = map[string]string{
			"cpu":    "2000m",
			"memory": "4Gi",
		}
		osCfg.Egress = &executor.EgressConfig{
			AllowedDomains: []string{
				"github.com",
				"api.github.com",
				"api.anthropic.com",
				"registry.npmjs.org",
				"pypi.org",
			},
		}
		osCfg.EnvVars = map[string]string{
			"ANTHROPIC_API_KEY": "${ANTHROPIC_API_KEY}",
			"GITHUB_TOKEN":      "${GITHUB_TOKEN}",
		}
		state.Config.Executor.Isolation.OpenSandbox = osCfg

		fmt.Printf("\n  %s Isolation: %s\n",
			onboardSuccessStyle.Render("✓"),
			onboardValueStyle.Render("opensandbox"))
		fmt.Printf("  %s Build executor image: %s\n",
			onboardDimStyle.Render("→"),
			onboardValueStyle.Render("make docker-build-executor"))

	default:
		// None — leave isolation nil (no isolation config)
	}

	return nil
}
