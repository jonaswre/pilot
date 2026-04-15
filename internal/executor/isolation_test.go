package executor

import (
	"testing"
)

func TestNewIsolationProvider(t *testing.T) {
	t.Run("nil config returns none", func(t *testing.T) {
		p := NewIsolationProvider(nil, nil)
		if p.Name() != IsolationTypeNone {
			t.Errorf("expected none, got %s", p.Name())
		}
	})

	t.Run("empty config returns none", func(t *testing.T) {
		p := NewIsolationProvider(&BackendConfig{}, nil)
		if p.Name() != IsolationTypeNone {
			t.Errorf("expected none, got %s", p.Name())
		}
	})

	t.Run("legacy use_worktree=true returns worktree provider", func(t *testing.T) {
		config := &BackendConfig{UseWorktree: true}
		mgr := &WorktreeManager{}
		p := NewIsolationProvider(config, mgr)
		if p.Name() != IsolationTypeWorktree {
			t.Errorf("expected worktree, got %s", p.Name())
		}
	})

	t.Run("legacy use_worktree=false returns none", func(t *testing.T) {
		config := &BackendConfig{UseWorktree: false}
		p := NewIsolationProvider(config, nil)
		if p.Name() != IsolationTypeNone {
			t.Errorf("expected none, got %s", p.Name())
		}
	})

	t.Run("explicit isolation type worktree returns worktree provider", func(t *testing.T) {
		config := &BackendConfig{
			Isolation: &IsolationConfig{Type: IsolationTypeWorktree},
		}
		mgr := &WorktreeManager{}
		p := NewIsolationProvider(config, mgr)
		if p.Name() != IsolationTypeWorktree {
			t.Errorf("expected worktree, got %s", p.Name())
		}
	})

	t.Run("explicit isolation type opensandbox returns opensandbox provider", func(t *testing.T) {
		config := &BackendConfig{
			Isolation: &IsolationConfig{
				Type: IsolationTypeOpenSandbox,
				OpenSandbox: &OpenSandboxIsolationConfig{
					ServerURL: "http://localhost:8080",
				},
			},
		}
		p := NewIsolationProvider(config, nil)
		if p.Name() != IsolationTypeOpenSandbox {
			t.Errorf("expected opensandbox, got %s", p.Name())
		}
	})

	t.Run("explicit isolation type none returns none", func(t *testing.T) {
		config := &BackendConfig{
			Isolation: &IsolationConfig{Type: IsolationTypeNone},
		}
		p := NewIsolationProvider(config, nil)
		if p.Name() != IsolationTypeNone {
			t.Errorf("expected none, got %s", p.Name())
		}
	})

	t.Run("explicit isolation overrides legacy use_worktree", func(t *testing.T) {
		// Even with use_worktree=true, explicit isolation takes precedence
		config := &BackendConfig{
			UseWorktree: true,
			Isolation:   &IsolationConfig{Type: IsolationTypeNone},
		}
		p := NewIsolationProvider(config, nil)
		if p.Name() != IsolationTypeNone {
			t.Errorf("expected none (explicit overrides legacy), got %s", p.Name())
		}
	})

	t.Run("opensandbox without config returns none", func(t *testing.T) {
		config := &BackendConfig{
			Isolation: &IsolationConfig{
				Type:        IsolationTypeOpenSandbox,
				OpenSandbox: nil,
			},
		}
		p := NewIsolationProvider(config, nil)
		if p.Name() != IsolationTypeNone {
			t.Errorf("expected none (no opensandbox config), got %s", p.Name())
		}
	})

	t.Run("unknown type returns none", func(t *testing.T) {
		config := &BackendConfig{
			Isolation: &IsolationConfig{Type: "firecracker"},
		}
		p := NewIsolationProvider(config, nil)
		if p.Name() != IsolationTypeNone {
			t.Errorf("expected none for unknown type, got %s", p.Name())
		}
	})
}

func TestNoneIsolationProvider(t *testing.T) {
	p := &NoneIsolationProvider{}

	if p.Name() != IsolationTypeNone {
		t.Errorf("expected none, got %s", p.Name())
	}
	if !p.IsAvailable() {
		t.Error("none provider should always be available")
	}
}
