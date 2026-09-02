package docker

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	sandbox "github.com/luojiyin1987/Agent-Sandbox-Runtime"
)

func FuzzResolveWorkspace(f *testing.F) {
	root, err := os.MkdirTemp("", "sandbox-workspace-fuzz-*")
	if err != nil {
		f.Fatalf("MkdirTemp() error = %v", err)
	}
	f.Cleanup(func() { _ = os.RemoveAll(root) })
	if err := os.Mkdir(filepath.Join(root, "safe"), 0o755); err != nil {
		f.Fatalf("Mkdir() error = %v", err)
	}
	outside, err := os.MkdirTemp("", "sandbox-workspace-outside-*")
	if err != nil {
		f.Fatalf("MkdirTemp() error = %v", err)
	}
	f.Cleanup(func() { _ = os.RemoveAll(outside) })
	if err := os.Symlink(outside, filepath.Join(root, "escape")); err != nil {
		f.Fatalf("Symlink() error = %v", err)
	}

	for _, seed := range []string{"", ".", "safe", "..", "../outside", outside, "escape", "bad\x00path"} {
		f.Add(seed)
	}
	backend := &Backend{workspaceRoot: root}
	f.Fuzz(func(t *testing.T, selector string) {
		resolved, err := backend.resolveWorkspace(sandbox.FilesystemPolicy{WorkspacePath: selector})
		if err != nil {
			if !errors.Is(err, sandbox.ErrInvalidRequest) {
				t.Fatalf("resolveWorkspace() error = %v, want ErrInvalidRequest", err)
			}
			return
		}
		if selector == "" {
			if resolved != "" {
				t.Fatalf("empty selector resolved to %q", resolved)
			}
			return
		}

		relative, err := filepath.Rel(root, resolved)
		if err != nil {
			t.Fatalf("Rel() error = %v", err)
		}
		if relative == ".." || filepath.IsAbs(relative) || strings.HasPrefix(relative, ".."+string(os.PathSeparator)) {
			t.Fatalf("resolved path %q escaped root %q", resolved, root)
		}
		info, err := os.Stat(resolved)
		if err != nil || !info.IsDir() {
			t.Fatalf("resolved path %q is not a directory: %v", resolved, err)
		}
	})
}
