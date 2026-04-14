package executor

import (
	"context"
	"os"
	"os/exec"
)

// LocalCommandRunner executes commands via os/exec on the local machine.
// This is the default runner and preserves all existing behavior.
type LocalCommandRunner struct{}

// Run starts a local subprocess with the given options.
func (r *LocalCommandRunner) Run(ctx context.Context, opts CommandRunOpts) (*RunningCommand, error) {
	cmd := exec.CommandContext(ctx, opts.Command, opts.Args...)
	cmd.Dir = opts.Dir

	if len(opts.Env) > 0 {
		env := os.Environ()
		for k, v := range opts.Env {
			env = append(env, k+"="+v)
		}
		cmd.Env = env
	}

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}

	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, err
	}

	if err := cmd.Start(); err != nil {
		return nil, err
	}

	return &RunningCommand{
		Stdout: stdout,
		Stderr: stderr,
		Wait:   cmd.Wait,
		Kill: func() error {
			if cmd.Process != nil {
				return cmd.Process.Kill()
			}
			return nil
		},
		PID: cmd.Process.Pid,
	}, nil
}
