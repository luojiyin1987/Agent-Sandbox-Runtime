package docker

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	sandbox "github.com/luojiyin1987/Agent-Sandbox-Runtime"
)

func TestDockerFilesystemRecursiveReadonlyIntegration(t *testing.T) {
	if os.Getenv("SANDBOX_DOCKER_INTEGRATION") != "1" {
		t.Skip("set SANDBOX_DOCKER_INTEGRATION=1 to run Docker integration test")
	}

	workspaceRoot := t.TempDir()
	workspace := filepath.Join(workspaceRoot, "job")
	nested := filepath.Join(workspace, "nested")
	nestedSource := t.TempDir()
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(nestedSource, "seed.txt"), []byte("seed"), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	if output, err := runPrivilegedHostCommand("mount", "--bind", nestedSource, nested); err != nil {
		t.Fatalf("bind mount nested workspace: %v: %s", err, output)
	}
	defer func() {
		if output, err := runPrivilegedHostCommand("umount", nested); err != nil {
			t.Errorf("unmount nested workspace: %v: %s", err, output)
		}
	}()

	backend, err := New(testImageDigest, WithWorkspaceRoot(workspaceRoot))
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	result, err := backend.Execute(context.Background(), sandbox.ExecRequest{
		Command: "sh",
		Args: []string{
			"-c",
			`cat /workspace/nested/seed.txt >/dev/null; read_status=$?; touch /workspace/nested/blocked; write_status=$?; test "$read_status" -eq 0 && test "$write_status" -ne 0`,
		},
		Filesystem: sandbox.FilesystemPolicy{
			WorkspacePath:     "job",
			WorkspaceReadOnly: true,
		},
	})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if result.ExitCode != 0 {
		t.Fatalf("recursive read-only verification failed: %+v", result)
	}
	if _, err := os.Stat(filepath.Join(nestedSource, "blocked")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("recursive read-only workspace created nested blocked file: %v", err)
	}
}

func TestDockerTmpfsCapacityIntegration(t *testing.T) {
	if os.Getenv("SANDBOX_DOCKER_INTEGRATION") != "1" {
		t.Skip("set SANDBOX_DOCKER_INTEGRATION=1 to run Docker integration test")
	}

	backend, err := New(testImageDigest)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	result, err := backend.Execute(context.Background(), sandbox.ExecRequest{
		Command: "sh",
		Args: []string{
			"-c",
			`df -kP /tmp | awk 'NR == 2 { print $2 }'`,
		},
		Filesystem: sandbox.FilesystemPolicy{
			TempDir: true,
		},
	})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if result.ExitCode != 0 {
		t.Fatalf("tmpfs capacity command result = %+v", result)
	}

	blocksKiB, err := strconv.ParseInt(strings.TrimSpace(string(result.Stdout)), 10, 64)
	if err != nil {
		t.Fatalf("parse /tmp capacity %q: %v", result.Stdout, err)
	}
	wantKiB := defaultTempDirBytes / 1024
	if blocksKiB != wantKiB {
		t.Fatalf("/tmp capacity = %d KiB, want %d KiB", blocksKiB, wantKiB)
	}
}

func runPrivilegedHostCommand(name string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if os.Geteuid() == 0 {
		return exec.CommandContext(ctx, name, args...).CombinedOutput()
	}
	if _, err := exec.LookPath("sudo"); err != nil {
		return nil, fmt.Errorf("sudo is required for host mount setup: %w", err)
	}
	privilegedArgs := append([]string{"-n", name}, args...)
	return exec.CommandContext(ctx, "sudo", privilegedArgs...).CombinedOutput()
}
