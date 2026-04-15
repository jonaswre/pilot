package executor

import (
	"bytes"
	"context"
	"io"
	"os"
	"strings"
	"testing"
	"time"
)

// TestOpenSandboxIntegration exercises the full OpenSandbox isolation flow
// against a live OpenSandbox server at localhost:8080.
//
// Requires: docker run -d --name opensandbox-server -p 8080:8080 \
//
//	-v /var/run/docker.sock:/var/run/docker.sock opensandbox/server:latest
//
// Skip with: go test -run TestOpenSandboxIntegration -short
func TestOpenSandboxIntegration(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}
	if os.Getenv("OPENSANDBOX_INTEGRATION") == "" {
		t.Skip("set OPENSANDBOX_INTEGRATION=1 to run")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	config := &OpenSandboxIsolationConfig{
		ServerURL: "http://localhost:8080/v1",
		Image:     "python:3.11-slim",
		Timeout:   5 * time.Minute,
		Resources: map[string]string{
			"cpu":    "500m",
			"memory": "512Mi",
		},
	}

	provider := NewOpenSandboxIsolationProvider(config)

	t.Run("IsAvailable", func(t *testing.T) {
		if !provider.IsAvailable() {
			t.Fatal("provider should be available with ServerURL configured")
		}
	})

	t.Run("Name", func(t *testing.T) {
		if provider.Name() != IsolationTypeOpenSandbox {
			t.Fatalf("expected %q, got %q", IsolationTypeOpenSandbox, provider.Name())
		}
	})

	t.Run("PrepareAndRunCommand", func(t *testing.T) {
		env, err := provider.Prepare(ctx, IsolationOpts{
			TaskID: "test-opensandbox-1",
		})
		if err != nil {
			t.Fatalf("Prepare failed: %v", err)
		}
		defer env.Cleanup()

		if env.CommandRunner == nil {
			t.Fatal("CommandRunner should not be nil")
		}
		if env.Cleanup == nil {
			t.Fatal("Cleanup should not be nil")
		}

		// Run a simple echo command
		running, err := env.CommandRunner.Run(ctx, CommandRunOpts{
			Command: "echo",
			Args:    []string{"hello", "from", "sandbox"},
		})
		if err != nil {
			t.Fatalf("Run failed: %v", err)
		}

		var stdout bytes.Buffer
		io.Copy(&stdout, running.Stdout)
		if err := running.Wait(); err != nil {
			t.Fatalf("Wait returned error: %v", err)
		}

		output := strings.TrimSpace(stdout.String())
		if output != "hello from sandbox" {
			t.Errorf("expected 'hello from sandbox', got %q", output)
		}
	})

	t.Run("StderrCapture", func(t *testing.T) {
		env, err := provider.Prepare(ctx, IsolationOpts{
			TaskID: "test-opensandbox-2",
		})
		if err != nil {
			t.Fatalf("Prepare failed: %v", err)
		}
		defer env.Cleanup()

		running, err := env.CommandRunner.Run(ctx, CommandRunOpts{
			Command: "sh",
			Args:    []string{"-c", "echo errout >&2"},
		})
		if err != nil {
			t.Fatalf("Run failed: %v", err)
		}

		var stderr bytes.Buffer
		io.Copy(&stderr, running.Stderr)
		running.Wait()

		output := strings.TrimSpace(stderr.String())
		if !strings.Contains(output, "errout") {
			t.Errorf("expected stderr to contain 'errout', got %q", output)
		}
	})

	t.Run("ExitCodeTracking", func(t *testing.T) {
		env, err := provider.Prepare(ctx, IsolationOpts{
			TaskID: "test-opensandbox-3",
		})
		if err != nil {
			t.Fatalf("Prepare failed: %v", err)
		}
		defer env.Cleanup()

		running, err := env.CommandRunner.Run(ctx, CommandRunOpts{
			Command: "sh",
			Args:    []string{"-c", "exit 42"},
		})
		if err != nil {
			t.Fatalf("Run failed: %v", err)
		}

		io.Copy(io.Discard, running.Stdout)
		err = running.Wait()
		if err == nil {
			t.Fatal("expected non-nil error for exit code 42")
		}
		if !strings.Contains(err.Error(), "42") {
			t.Errorf("expected error to mention exit code 42, got: %v", err)
		}
	})

	t.Run("EnvVarsAndWorkDir", func(t *testing.T) {
		env, err := provider.Prepare(ctx, IsolationOpts{
			TaskID: "test-opensandbox-4",
		})
		if err != nil {
			t.Fatalf("Prepare failed: %v", err)
		}
		defer env.Cleanup()

		running, err := env.CommandRunner.Run(ctx, CommandRunOpts{
			Command: "sh",
			Args:    []string{"-c", "echo $MY_VAR && pwd"},
			Dir:     "/tmp",
			Env:     map[string]string{"MY_VAR": "test-value"},
		})
		if err != nil {
			t.Fatalf("Run failed: %v", err)
		}

		var stdout bytes.Buffer
		io.Copy(&stdout, running.Stdout)
		running.Wait()

		output := stdout.String()
		if !strings.Contains(output, "test-value") {
			t.Errorf("expected output to contain 'test-value', got %q", output)
		}
		if !strings.Contains(output, "/tmp") {
			t.Errorf("expected output to contain '/tmp' (working dir), got %q", output)
		}
	})

	t.Run("CleanupIdempotent", func(t *testing.T) {
		env, err := provider.Prepare(ctx, IsolationOpts{
			TaskID: "test-opensandbox-5",
		})
		if err != nil {
			t.Fatalf("Prepare failed: %v", err)
		}

		// Call cleanup multiple times — should not panic
		env.Cleanup()
		env.Cleanup()
		env.Cleanup()
	})
}
