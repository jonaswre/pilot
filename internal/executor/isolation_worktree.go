package executor

import (
	"context"
	"fmt"
	"log/slog"
)

// WorktreeIsolationProvider wraps the existing WorktreeManager to provide
// filesystem isolation via git worktrees. Commands run locally via LocalCommandRunner.
type WorktreeIsolationProvider struct {
	manager *WorktreeManager
}

// NewWorktreeIsolationProvider creates a worktree isolation provider.
// If manager is nil, Prepare will create ad-hoc worktrees (no pooling).
func NewWorktreeIsolationProvider(manager *WorktreeManager) *WorktreeIsolationProvider {
	return &WorktreeIsolationProvider{manager: manager}
}

// Name returns "worktree".
func (p *WorktreeIsolationProvider) Name() string { return IsolationTypeWorktree }

// IsAvailable returns true if the provider has a configured WorktreeManager.
func (p *WorktreeIsolationProvider) IsAvailable() bool { return p.manager != nil }

// Prepare creates an isolated worktree for the task.
// Uses the WorktreeManager's pool if available, otherwise creates a one-off worktree.
func (p *WorktreeIsolationProvider) Prepare(ctx context.Context, opts IsolationOpts) (*IsolatedEnvironment, error) {
	if p.manager == nil {
		return nil, fmt.Errorf("worktree isolation requires a WorktreeManager")
	}

	slog.Info("Preparing worktree isolation",
		slog.String("task_id", opts.TaskID),
		slog.String("branch", opts.Branch),
	)

	var worktreePath string
	var cleanup func()

	// Use pool if available, otherwise fall back to direct creation
	if p.manager.PoolSize() > 0 {
		slog.Debug("Using worktree pool",
			slog.Int("pool_available", p.manager.PoolAvailable()),
		)
		result, err := p.manager.Acquire(ctx, opts.TaskID, opts.Branch, opts.BaseBranch)
		if err != nil {
			return nil, fmt.Errorf("failed to acquire pooled worktree: %w", err)
		}
		worktreePath = result.Path
		cleanup = result.Cleanup
	} else {
		path, cleanupFn, err := CreateWorktreeWithBranch(
			ctx, p.manager.RepoPath(), opts.TaskID, opts.Branch, opts.BaseBranch)
		if err != nil {
			return nil, fmt.Errorf("failed to create worktree: %w", err)
		}
		worktreePath = path
		cleanup = cleanupFn
	}

	slog.Info("Worktree isolation ready",
		slog.String("task_id", opts.TaskID),
		slog.String("worktree", worktreePath),
	)

	return &IsolatedEnvironment{
		WorkDir:       worktreePath,
		CommandRunner: &LocalCommandRunner{},
		Cleanup:       cleanup,
	}, nil
}
