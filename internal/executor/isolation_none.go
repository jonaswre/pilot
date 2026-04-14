package executor

import "context"

// NoneIsolationProvider runs execution directly in the project directory
// with no filesystem or process isolation. This is the default when no
// isolation is configured.
type NoneIsolationProvider struct{}

// Name returns "none".
func (p *NoneIsolationProvider) Name() string { return IsolationTypeNone }

// IsAvailable always returns true — no isolation requires no setup.
func (p *NoneIsolationProvider) IsAvailable() bool { return true }

// Prepare returns the original project path and a local command runner.
func (p *NoneIsolationProvider) Prepare(_ context.Context, opts IsolationOpts) (*IsolatedEnvironment, error) {
	return &IsolatedEnvironment{
		WorkDir:       opts.ProjectPath,
		CommandRunner: &LocalCommandRunner{},
		Cleanup:       func() {}, // no-op
	}, nil
}
