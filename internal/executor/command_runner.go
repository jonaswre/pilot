package executor

import (
	"context"
	"io"
)

// CommandRunner abstracts how a subprocess is executed.
// Local runners use os/exec; sandbox runners use remote execution (e.g., OpenSandbox ExecdClient).
// This enables the same Backend (claude-code, qwen-code) to run in any isolation environment.
type CommandRunner interface {
	// Run starts a command and returns handles for its I/O and lifecycle.
	// The command runs in the given working directory with the specified environment.
	Run(ctx context.Context, opts CommandRunOpts) (*RunningCommand, error)
}

// CommandRunOpts describes a command to execute.
type CommandRunOpts struct {
	// Command is the binary name or path (e.g., "claude", "qwen")
	Command string

	// Args are the command-line arguments
	Args []string

	// Dir is the working directory for the command
	Dir string

	// Env contains additional environment variables (merged with existing environment).
	// For local execution, these are appended to os.Environ().
	// For sandbox execution, these are passed to the sandbox command runner.
	Env map[string]string
}

// RunningCommand represents a started command with access to its I/O and lifecycle controls.
type RunningCommand struct {
	// Stdout provides the command's standard output stream
	Stdout io.ReadCloser

	// Stderr provides the command's standard error stream
	Stderr io.ReadCloser

	// Wait blocks until the command exits and returns the exit error (if any)
	Wait func() error

	// Kill forcibly terminates the command
	Kill func() error

	// PID is the process ID (local) or sandbox-side process ID (remote), used for logging
	PID int
}
