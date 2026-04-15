package executor

import "testing"

func TestSanitizeImageName(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"my-app", "my-app"},
		{"My App", "my-app"},
		{"my_app_v2", "my-app-v2"},
		{"UPPER", "upper"},
		{"special!@#chars", "special---chars"},
		{"  spaces  ", "spaces"},
		{"a--b--c", "a--b--c"},
		{"", "project"},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := sanitizeImageName(tt.input)
			if got != tt.expected {
				t.Errorf("sanitizeImageName(%q) = %q, want %q", tt.input, got, tt.expected)
			}
		})
	}
}

func TestExecutorImageTag(t *testing.T) {
	tests := []struct {
		projectName string
		expected    string
	}{
		{"pilot", "pilot-executor/pilot:latest"},
		{"My App", "pilot-executor/my-app:latest"},
		{"", "pilot-executor/project:latest"},
	}

	for _, tt := range tests {
		t.Run(tt.projectName, func(t *testing.T) {
			got := executorImageTag(tt.projectName)
			if got != tt.expected {
				t.Errorf("executorImageTag(%q) = %q, want %q", tt.projectName, got, tt.expected)
			}
		})
	}
}

func TestResolveExecutorImage(t *testing.T) {
	t.Run("explicit override wins", func(t *testing.T) {
		config := DefaultOpenSandboxConfig()
		opts := IsolationOpts{
			ExecutorImage: "custom/image:v2",
			ProjectName:   "myapp",
			ProjectPath:   "/tmp/nonexistent",
		}
		got := resolveExecutorImage(t.Context(), config, opts)
		if got != "custom/image:v2" {
			t.Errorf("expected custom/image:v2, got %s", got)
		}
	})

	t.Run("falls back to global default when no Dockerfile and no override", func(t *testing.T) {
		config := DefaultOpenSandboxConfig()
		config.Image = "pilot/executor:latest"
		opts := IsolationOpts{
			ProjectName: "nonexistent-project-xyz",
			ProjectPath: "/tmp/nonexistent-path-xyz",
		}
		got := resolveExecutorImage(t.Context(), config, opts)
		if got != "pilot/executor:latest" {
			t.Errorf("expected pilot/executor:latest, got %s", got)
		}
	})

	t.Run("falls back to global default with auto_build disabled", func(t *testing.T) {
		autoBuild := false
		config := DefaultOpenSandboxConfig()
		config.AutoBuild = &autoBuild
		config.Image = "pilot/executor:latest"
		opts := IsolationOpts{
			ProjectName: "testproject",
			ProjectPath: "/tmp/nonexistent",
		}
		got := resolveExecutorImage(t.Context(), config, opts)
		if got != "pilot/executor:latest" {
			t.Errorf("expected pilot/executor:latest, got %s", got)
		}
	})

	t.Run("derives project name from path when ProjectName empty", func(t *testing.T) {
		config := DefaultOpenSandboxConfig()
		config.Image = "fallback:latest"
		opts := IsolationOpts{
			ProjectPath: "/home/user/projects/my-cool-app",
		}
		got := resolveExecutorImage(t.Context(), config, opts)
		if got != "fallback:latest" {
			t.Errorf("expected fallback:latest, got %s", got)
		}
	})
}
