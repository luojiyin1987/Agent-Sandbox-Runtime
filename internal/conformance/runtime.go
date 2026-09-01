package conformance

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	sandbox "github.com/luojiyin1987/Agent-Sandbox-Runtime"
)

// Factory creates one runtime instance whose trusted workspace capability is
// rooted at workspaceRoot. Implementations should fail the test immediately if
// backend construction itself fails.
type Factory func(t *testing.T, workspaceRoot string) sandbox.Runtime

// Run executes backend-neutral behavioral checks against one Runtime factory.
// The suite intentionally avoids implementation details such as host cgroup
// paths or /proc seccomp fields that need not match across isolation engines.
func Run(t *testing.T, factory Factory) {
	t.Helper()

	t.Run("result-and-environment", func(t *testing.T) {
		runtime := factory(t, t.TempDir())
		result, err := runtime.Execute(context.Background(), sandbox.ExecRequest{
			Command: "sh",
			Args:    []string{"-c", `printf '%s:%s' "$GREETING" "$PWD"; printf warning >&2; exit 7`},
			Env:     map[string]string{"GREETING": "hello"},
			WorkDir: "/tmp",
			Filesystem: sandbox.FilesystemPolicy{
				TempDir: true,
			},
		})
		if err != nil {
			t.Fatalf("Execute() error = %v", err)
		}
		if result.ExitCode != 7 || result.Termination != sandbox.TerminationCompleted {
			t.Fatalf("result = %+v", result)
		}
		if got := string(result.Stdout); got != "hello:/tmp" {
			t.Fatalf("stdout = %q", got)
		}
		if got := string(result.Stderr); got != "warning" {
			t.Fatalf("stderr = %q", got)
		}
	})

	t.Run("timeout-is-terminal", func(t *testing.T) {
		runtime := factory(t, t.TempDir())
		result, err := runtime.Execute(context.Background(), sandbox.ExecRequest{
			Command: "sleep",
			Args:    []string{"30"},
			Timeout: 750 * time.Millisecond,
		})
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("Execute() error = %v, want DeadlineExceeded", err)
		}
		if result.Termination != sandbox.TerminationTimeout {
			t.Fatalf("Termination = %q, want %q", result.Termination, sandbox.TerminationTimeout)
		}
	})

	t.Run("output-is-bounded", func(t *testing.T) {
		runtime := factory(t, t.TempDir())
		result, err := runtime.Execute(context.Background(), sandbox.ExecRequest{
			Command: "sh",
			Args:    []string{"-c", "yes x | head -c 16384"},
			Resources: sandbox.ResourceLimits{
				MaxOutputBytes: 4096,
			},
		})
		if err != nil {
			t.Fatalf("Execute() error = %v", err)
		}
		if !result.OutputTruncated {
			t.Fatal("OutputTruncated = false")
		}
		if got := len(result.Stdout) + len(result.Stderr); got != 4096 {
			t.Fatalf("captured output = %d bytes, want 4096", got)
		}
	})

	t.Run("root-filesystem-is-read-only", func(t *testing.T) {
		runtime := factory(t, t.TempDir())
		result, err := runtime.Execute(context.Background(), sandbox.ExecRequest{
			Command: "sh",
			Args:    []string{"-c", "touch /agent-sandbox-conformance-blocked"},
		})
		if err != nil {
			t.Fatalf("Execute() error = %v", err)
		}
		if result.ExitCode == 0 {
			t.Fatal("read-only root unexpectedly allowed a write")
		}
	})

	t.Run("workspace-read-write", func(t *testing.T) {
		root := t.TempDir()
		workspace := filepath.Join(root, "job")
		if err := os.Mkdir(workspace, 0o755); err != nil {
			t.Fatalf("Mkdir() error = %v", err)
		}
		if err := os.WriteFile(filepath.Join(workspace, "input.txt"), []byte("seed"), 0o644); err != nil {
			t.Fatalf("WriteFile() error = %v", err)
		}

		runtime := factory(t, root)
		result, err := runtime.Execute(context.Background(), sandbox.ExecRequest{
			Command: "sh",
			Args:    []string{"-c", "cat input.txt; printf written > output.txt"},
			WorkDir: "/workspace",
			Filesystem: sandbox.FilesystemPolicy{
				WorkspacePath: "job",
			},
		})
		if err != nil {
			t.Fatalf("Execute() error = %v", err)
		}
		if result.ExitCode != 0 || string(result.Stdout) != "seed" {
			t.Fatalf("result = %+v", result)
		}
		written, err := os.ReadFile(filepath.Join(workspace, "output.txt"))
		if err != nil {
			t.Fatalf("ReadFile() error = %v", err)
		}
		if string(written) != "written" {
			t.Fatalf("workspace output = %q", written)
		}
	})

	t.Run("workspace-read-only", func(t *testing.T) {
		root := t.TempDir()
		workspace := filepath.Join(root, "job")
		if err := os.Mkdir(workspace, 0o755); err != nil {
			t.Fatalf("Mkdir() error = %v", err)
		}
		if err := os.WriteFile(filepath.Join(workspace, "input.txt"), []byte("seed"), 0o644); err != nil {
			t.Fatalf("WriteFile() error = %v", err)
		}

		runtime := factory(t, root)
		result, err := runtime.Execute(context.Background(), sandbox.ExecRequest{
			Command: "sh",
			Args:    []string{"-c", "cat /workspace/input.txt >/dev/null && touch /workspace/blocked"},
			Filesystem: sandbox.FilesystemPolicy{
				WorkspacePath:     "job",
				WorkspaceReadOnly: true,
			},
		})
		if err != nil {
			t.Fatalf("Execute() error = %v", err)
		}
		if result.ExitCode == 0 {
			t.Fatal("read-only workspace unexpectedly allowed a write")
		}
		if _, err := os.Stat(filepath.Join(workspace, "blocked")); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("read-only workspace created blocked file: %v", err)
		}
	})

	t.Run("tmpfs-enables-temporary-writes", func(t *testing.T) {
		runtime := factory(t, t.TempDir())
		result, err := runtime.Execute(context.Background(), sandbox.ExecRequest{
			Command: "sh",
			Args:    []string{"-c", "printf ok >/tmp/result && cat /tmp/result"},
			Filesystem: sandbox.FilesystemPolicy{
				TempDir: true,
			},
		})
		if err != nil {
			t.Fatalf("Execute() error = %v", err)
		}
		if result.ExitCode != 0 || string(result.Stdout) != "ok" {
			t.Fatalf("result = %+v", result)
		}
	})

	t.Run("network-none-has-no-default-route", func(t *testing.T) {
		runtime := factory(t, t.TempDir())
		result, err := runtime.Execute(context.Background(), sandbox.ExecRequest{
			Command: "sh",
			Args:    []string{"-c", `awk '$2 == "00000000" { found=1 } END { exit found ? 1 : 0 }' /proc/net/route`},
		})
		if err != nil {
			t.Fatalf("Execute() error = %v", err)
		}
		if result.ExitCode != 0 {
			t.Fatalf("network-none result = %+v", result)
		}
	})
}
