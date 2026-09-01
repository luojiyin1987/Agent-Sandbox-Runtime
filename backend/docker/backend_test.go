package docker

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	sandbox "github.com/luojiyin1987/Agent-Sandbox-Runtime"
)

type runnerCall struct {
	args []string
}

type fakeRunner struct {
	mu             sync.Mutex
	calls          []runnerCall
	exitCode       string
	oomKilled      bool
	startOut       string
	startErr       string
	createError    error
	blockStart     bool
	envFileContent string
}

func (f *fakeRunner) run(ctx context.Context, stdout, stderr io.Writer, args ...string) error {
	f.mu.Lock()
	f.calls = append(f.calls, runnerCall{args: slices.Clone(args)})
	f.mu.Unlock()

	switch args[0] {
	case "create":
		if envFile, ok := argValue(args, "--env-file"); ok {
			content, err := os.ReadFile(envFile)
			if err != nil {
				return err
			}
			f.mu.Lock()
			f.envFileContent = string(content)
			f.mu.Unlock()
		}
		if f.createError != nil {
			return f.createError
		}
		_, _ = io.WriteString(stdout, "container-id\n")
	case "start":
		if f.blockStart {
			<-ctx.Done()
			return ctx.Err()
		}
		_, _ = io.WriteString(stdout, f.startOut)
		_, _ = io.WriteString(stderr, f.startErr)
	case "inspect":
		_, _ = io.WriteString(stdout, f.exitCode+" "+boolString(f.oomKilled)+"\n")
	}
	return nil
}

func argValue(args []string, name string) (string, bool) {
	for index := 0; index+1 < len(args); index++ {
		if args[index] == name {
			return args[index+1], true
		}
	}
	return "", false
}

func boolString(value bool) string {
	if value {
		return "true"
	}
	return "false"
}

func (f *fakeRunner) snapshotCalls() []runnerCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.calls)
}

func (f *fakeRunner) snapshotEnvFileContent() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.envFileContent
}

func TestExecutePreservesWorkloadResult(t *testing.T) {
	fake := &fakeRunner{exitCode: "7", startOut: "hello", startErr: "warning"}
	backend := &Backend{image: "alpine:3.22", run: fake.run}

	result, err := backend.Execute(context.Background(), sandbox.ExecRequest{
		Command: "sh",
		Args:    []string{"-c", "exit 7"},
		Env: map[string]string{
			"TOKEN":       "secret-value",
			"DOCKER_HOST": "container-only",
		},
		WorkDir: "/tmp",
	})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if result.ExitCode != 7 || result.Termination != sandbox.TerminationCompleted {
		t.Fatalf("Execute() result = %+v", result)
	}
	if string(result.Stdout) != "hello" || string(result.Stderr) != "warning" {
		t.Fatalf("unexpected output: stdout=%q stderr=%q", result.Stdout, result.Stderr)
	}

	calls := fake.snapshotCalls()
	if len(calls) != 4 {
		t.Fatalf("docker calls = %d, want 4", len(calls))
	}
	create := calls[0]
	joined := strings.Join(create.args, " ")
	for _, want := range []string{
		"--network none",
		"--read-only",
		"--memory 268435456",
		"--pids-limit 64",
		"--cpus 1.000",
		"--workdir /tmp",
		"--env-file",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("docker create args %q missing %q", joined, want)
		}
	}
	for _, secret := range []string{"secret-value", "container-only"} {
		if strings.Contains(joined, secret) {
			t.Fatalf("environment value leaked into docker argv: %q", joined)
		}
	}
	envFile := fake.snapshotEnvFileContent()
	for _, want := range []string{"DOCKER_HOST=container-only\n", "TOKEN=secret-value\n"} {
		if !strings.Contains(envFile, want) {
			t.Fatalf("environment file %q missing %q", envFile, want)
		}
	}
	if calls[len(calls)-1].args[0] != "rm" {
		t.Fatalf("last docker call = %q, want rm", calls[len(calls)-1].args[0])
	}
}

func TestExecuteRejectsEnvironmentFileDelimiters(t *testing.T) {
	fake := &fakeRunner{exitCode: "0"}
	backend := &Backend{image: "alpine:3.22", run: fake.run}

	_, err := backend.Execute(context.Background(), sandbox.ExecRequest{
		Command: "true",
		Env:     map[string]string{"TOKEN": "one\ntwo"},
	})
	if !errors.Is(err, sandbox.ErrInvalidRequest) {
		t.Fatalf("Execute() error = %v, want ErrInvalidRequest", err)
	}
	if calls := fake.snapshotCalls(); len(calls) != 0 {
		t.Fatalf("docker was called before environment validation: %+v", calls)
	}
}

func TestExecuteCompilesPartialResourceLimits(t *testing.T) {
	fake := &fakeRunner{
		exitCode: "0",
		startOut: strings.Repeat("x", 8192),
	}
	backend := &Backend{image: "alpine:3.22", run: fake.run}

	result, err := backend.Execute(context.Background(), sandbox.ExecRequest{
		Command: "true",
		Resources: sandbox.ResourceLimits{
			MaxMemoryBytes: 64 << 20,
			MaxProcesses:   16,
			MaxOutputBytes: 4096,
			MilliCPUs:      500,
		},
	})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if !result.OutputTruncated {
		t.Fatal("OutputTruncated = false, want true")
	}
	if got := len(result.Stdout) + len(result.Stderr); got != 4096 {
		t.Fatalf("captured output = %d bytes, want 4096", got)
	}

	create := fake.snapshotCalls()[0]
	joined := strings.Join(create.args, " ")
	for _, want := range []string{
		"--memory 67108864",
		"--pids-limit 16",
		"--cpus 0.500",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("docker create args %q missing %q", joined, want)
		}
	}
}

func TestExecuteKeepsSafeDefaultsForOmittedResourceFields(t *testing.T) {
	fake := &fakeRunner{exitCode: "0"}
	backend := &Backend{image: "alpine:3.22", run: fake.run}

	_, err := backend.Execute(context.Background(), sandbox.ExecRequest{
		Command: "true",
		Resources: sandbox.ResourceLimits{
			MaxMemoryBytes: 128 << 20,
		},
	})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}

	joined := strings.Join(fake.snapshotCalls()[0].args, " ")
	for _, want := range []string{
		"--memory 134217728",
		"--pids-limit 64",
		"--cpus 1.000",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("docker create args %q missing %q", joined, want)
		}
	}
}

func TestExecuteRejectsTooSmallDockerMemoryBeforeDocker(t *testing.T) {
	fake := &fakeRunner{exitCode: "0"}
	backend := &Backend{image: "alpine:3.22", run: fake.run}

	_, err := backend.Execute(context.Background(), sandbox.ExecRequest{
		Command: "true",
		Resources: sandbox.ResourceLimits{
			MaxMemoryBytes: minimumMemoryBytes - 1,
		},
	})
	if !errors.Is(err, sandbox.ErrInvalidRequest) {
		t.Fatalf("Execute() error = %v, want ErrInvalidRequest", err)
	}
	if calls := fake.snapshotCalls(); len(calls) != 0 {
		t.Fatalf("docker was called before resource validation: %+v", calls)
	}
}

func TestExecuteClassifiesOOMAsResourceLimit(t *testing.T) {
	fake := &fakeRunner{exitCode: "137", oomKilled: true}
	backend := &Backend{image: "alpine:3.22", run: fake.run}

	result, err := backend.Execute(context.Background(), sandbox.ExecRequest{
		Command: "memory-hog",
		Resources: sandbox.ResourceLimits{
			MaxMemoryBytes: 32 << 20,
		},
	})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if result.ExitCode != 137 || result.Termination != sandbox.TerminationResourceLimit {
		t.Fatalf("Execute() result = %+v", result)
	}
}

func TestExecuteCompilesFilesystemMounts(t *testing.T) {
	root := t.TempDir()
	workspace := filepath.Join(root, "job")
	if err := os.Mkdir(workspace, 0o755); err != nil {
		t.Fatalf("Mkdir() error = %v", err)
	}

	fake := &fakeRunner{exitCode: "0"}
	backend, err := New(testImageDigest, WithWorkspaceRoot(root))
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	backend.run = fake.run

	_, err = backend.Execute(context.Background(), sandbox.ExecRequest{
		Command: "true",
		WorkDir: containerWorkspace,
		Filesystem: sandbox.FilesystemPolicy{
			WorkspacePath:     "job",
			WorkspaceReadOnly: true,
			TempDir:           true,
		},
	})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}

	joined := strings.Join(fake.snapshotCalls()[0].args, " ")
	for _, want := range []string{
		"--mount type=bind,src=" + workspace + ",dst=/workspace,readonly,bind-propagation=rprivate,bind-recursive=readonly",
		"--mount type=tmpfs,dst=/tmp,tmpfs-size=67108864,tmpfs-mode=1777",
		"--workdir /workspace",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("docker create args %q missing %q", joined, want)
		}
	}
}

func TestExecuteCompilesWritableWorkspaceWithoutReadonlyFlag(t *testing.T) {
	root := t.TempDir()
	workspace := filepath.Join(root, "job")
	if err := os.Mkdir(workspace, 0o755); err != nil {
		t.Fatalf("Mkdir() error = %v", err)
	}

	fake := &fakeRunner{exitCode: "0"}
	backend, err := New(testImageDigest, WithWorkspaceRoot(root))
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	backend.run = fake.run

	_, err = backend.Execute(context.Background(), sandbox.ExecRequest{
		Command: "true",
		Filesystem: sandbox.FilesystemPolicy{
			WorkspacePath: "job",
		},
	})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}

	mount, ok := argValue(fake.snapshotCalls()[0].args, "--mount")
	if !ok {
		t.Fatal("docker create args missing --mount")
	}
	if mount != "type=bind,src="+workspace+",dst=/workspace" {
		t.Fatalf("workspace mount = %q", mount)
	}
}

func TestExecuteRejectsWorkspaceWithoutTrustedRoot(t *testing.T) {
	fake := &fakeRunner{exitCode: "0"}
	backend := &Backend{image: "alpine:3.22", run: fake.run}

	_, err := backend.Execute(context.Background(), sandbox.ExecRequest{
		Command: "true",
		Filesystem: sandbox.FilesystemPolicy{
			WorkspacePath: "job",
		},
	})
	if !errors.Is(err, ErrUnsupportedPolicy) {
		t.Fatalf("Execute() error = %v, want ErrUnsupportedPolicy", err)
	}
	if calls := fake.snapshotCalls(); len(calls) != 0 {
		t.Fatalf("docker was called before workspace rejection: %+v", calls)
	}
}

func TestExecuteRejectsWorkspaceEscapes(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(root, "escape")); err != nil {
		t.Fatalf("Symlink() error = %v", err)
	}

	backend, err := New(testImageDigest, WithWorkspaceRoot(root))
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	fake := &fakeRunner{exitCode: "0"}
	backend.run = fake.run

	for _, workspacePath := range []string{"../outside", outside, "escape"} {
		t.Run(workspacePath, func(t *testing.T) {
			_, err := backend.Execute(context.Background(), sandbox.ExecRequest{
				Command: "true",
				Filesystem: sandbox.FilesystemPolicy{
					WorkspacePath: workspacePath,
				},
			})
			if !errors.Is(err, sandbox.ErrInvalidRequest) {
				t.Fatalf("Execute() error = %v, want ErrInvalidRequest", err)
			}
		})
	}
	if calls := fake.snapshotCalls(); len(calls) != 0 {
		t.Fatalf("docker was called before workspace validation: %+v", calls)
	}
}

func TestExecuteRejectsReadonlyWorkspaceWithoutPath(t *testing.T) {
	fake := &fakeRunner{exitCode: "0"}
	backend := &Backend{image: "alpine:3.22", run: fake.run}

	_, err := backend.Execute(context.Background(), sandbox.ExecRequest{
		Command: "true",
		Filesystem: sandbox.FilesystemPolicy{
			WorkspaceReadOnly: true,
		},
	})
	if !errors.Is(err, sandbox.ErrInvalidRequest) {
		t.Fatalf("Execute() error = %v, want ErrInvalidRequest", err)
	}
	if calls := fake.snapshotCalls(); len(calls) != 0 {
		t.Fatalf("docker was called before filesystem validation: %+v", calls)
	}
}

func TestExecuteRejectsUnsupportedPolicyBeforeDocker(t *testing.T) {
	tests := []sandbox.ExecRequest{
		{Command: "true", Network: sandbox.NetworkPolicy{Mode: sandbox.NetworkOutbound}},
		{Command: "true", Filesystem: sandbox.FilesystemPolicy{Root: sandbox.RootReadWrite}},
	}

	for _, req := range tests {
		fake := &fakeRunner{exitCode: "0"}
		backend := &Backend{image: "alpine:3.22", run: fake.run}

		_, err := backend.Execute(context.Background(), req)
		if !errors.Is(err, ErrUnsupportedPolicy) {
			t.Fatalf("Execute() error = %v, want ErrUnsupportedPolicy", err)
		}
		if calls := fake.snapshotCalls(); len(calls) != 0 {
			t.Fatalf("docker was called before policy rejection: %+v", calls)
		}
	}
}

func TestExecuteCreateFailureStillAttemptsCleanup(t *testing.T) {
	fake := &fakeRunner{createError: errors.New("connection reset")}
	backend := &Backend{image: "alpine:3.22", run: fake.run}

	_, err := backend.Execute(context.Background(), sandbox.ExecRequest{Command: "true"})
	if err == nil {
		t.Fatal("Execute() error = nil, want docker create failure")
	}

	calls := fake.snapshotCalls()
	if len(calls) != 2 || calls[0].args[0] != "create" || calls[1].args[0] != "rm" {
		t.Fatalf("create failure cleanup calls = %+v, want create then rm", calls)
	}
}

func TestExecuteTimeoutStillRemovesContainer(t *testing.T) {
	fake := &fakeRunner{exitCode: "0", blockStart: true}
	backend := &Backend{image: "alpine:3.22", run: fake.run}

	result, err := backend.Execute(context.Background(), sandbox.ExecRequest{
		Command: "sleep",
		Args:    []string{"60"},
		Timeout: 10 * time.Millisecond,
	})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Execute() error = %v, want DeadlineExceeded", err)
	}
	if result.Termination != sandbox.TerminationTimeout {
		t.Fatalf("Termination = %q, want %q", result.Termination, sandbox.TerminationTimeout)
	}

	calls := fake.snapshotCalls()
	if len(calls) < 3 || calls[len(calls)-1].args[0] != "rm" {
		t.Fatalf("timeout did not force container cleanup: %+v", calls)
	}
}

func TestExecuteBoundsCapturedOutput(t *testing.T) {
	fake := &fakeRunner{
		exitCode: "0",
		startOut: strings.Repeat("x", int(defaultMaxOutputBytes)+128),
	}
	backend := &Backend{image: "alpine:3.22", run: fake.run}

	result, err := backend.Execute(context.Background(), sandbox.ExecRequest{Command: "true"})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if !result.OutputTruncated {
		t.Fatal("OutputTruncated = false, want true")
	}
	if got := len(result.Stdout) + len(result.Stderr); got != int(defaultMaxOutputBytes) {
		t.Fatalf("captured output = %d bytes, want %d", got, defaultMaxOutputBytes)
	}
}

func TestDockerBackendIntegration(t *testing.T) {
	if os.Getenv("SANDBOX_DOCKER_INTEGRATION") != "1" {
		t.Skip("set SANDBOX_DOCKER_INTEGRATION=1 to run Docker integration test")
	}

	workspaceRoot := t.TempDir()
	workspace := filepath.Join(workspaceRoot, "job")
	if err := os.Mkdir(workspace, 0o755); err != nil {
		t.Fatalf("Mkdir() error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "input.txt"), []byte("seed"), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	backend, err := New(testImageDigest, WithWorkspaceRoot(workspaceRoot))
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	result, err := backend.Execute(context.Background(), sandbox.ExecRequest{
		Command: "sh",
		Args:    []string{"-c", `printf '%s:%s:%s' "$GREETING" "$DOCKER_HOST" "$PWD"; printf 'warning' >&2; exit 7`},
		Env: map[string]string{
			"GREETING":    "hello",
			"DOCKER_HOST": "container-only",
		},
		WorkDir: "/tmp",
	})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if result.ExitCode != 7 || result.Termination != sandbox.TerminationCompleted {
		t.Fatalf("Execute() result = %+v", result)
	}
	if string(result.Stdout) != "hello:container-only:/tmp" || string(result.Stderr) != "warning" {
		t.Fatalf("unexpected output: stdout=%q stderr=%q", result.Stdout, result.Stderr)
	}

	readOnlyResult, err := backend.Execute(context.Background(), sandbox.ExecRequest{
		Command: "sh",
		Args:    []string{"-c", "touch /agent-sandbox-should-not-write"},
	})
	if err != nil {
		t.Fatalf("read-only Execute() error = %v", err)
	}
	if readOnlyResult.ExitCode == 0 {
		t.Fatal("read-only root filesystem unexpectedly allowed a write")
	}

	resourceResult, err := backend.Execute(context.Background(), sandbox.ExecRequest{
		Command: "sh",
		Args:    []string{"-c", `printf 'memory='; cat /sys/fs/cgroup/memory.max; printf 'pids='; cat /sys/fs/cgroup/pids.max; printf 'cpu='; cat /sys/fs/cgroup/cpu.max`},
		Resources: sandbox.ResourceLimits{
			MaxMemoryBytes: 64 << 20,
			MaxProcesses:   16,
			MilliCPUs:      500,
		},
	})
	if err != nil {
		t.Fatalf("resource-limited Execute() error = %v", err)
	}
	resourceOutput := string(resourceResult.Stdout)
	for _, want := range []string{"memory=67108864\n", "pids=16\n", "cpu=50000 100000\n"} {
		if !strings.Contains(resourceOutput, want) {
			t.Fatalf("resource output %q missing %q", resourceOutput, want)
		}
	}

	outputResult, err := backend.Execute(context.Background(), sandbox.ExecRequest{
		Command: "sh",
		Args:    []string{"-c", "yes x | head -c 65536"},
		Resources: sandbox.ResourceLimits{
			MaxOutputBytes: 4096,
		},
	})
	if err != nil {
		t.Fatalf("output-limited Execute() error = %v", err)
	}
	if !outputResult.OutputTruncated || len(outputResult.Stdout)+len(outputResult.Stderr) != 4096 {
		t.Fatalf("output-limited result = %+v", outputResult)
	}

	workspaceResult, err := backend.Execute(context.Background(), sandbox.ExecRequest{
		Command: "sh",
		Args:    []string{"-c", "cat input.txt; printf written > output.txt"},
		WorkDir: containerWorkspace,
		Filesystem: sandbox.FilesystemPolicy{
			WorkspacePath: "job",
		},
	})
	if err != nil {
		t.Fatalf("writable workspace Execute() error = %v", err)
	}
	if workspaceResult.ExitCode != 0 || string(workspaceResult.Stdout) != "seed" {
		t.Fatalf("writable workspace result = %+v", workspaceResult)
	}
	written, err := os.ReadFile(filepath.Join(workspace, "output.txt"))
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	if string(written) != "written" {
		t.Fatalf("workspace output = %q", written)
	}

	readonlyWorkspaceResult, err := backend.Execute(context.Background(), sandbox.ExecRequest{
		Command: "sh",
		Args:    []string{"-c", "cat /workspace/input.txt >/dev/null && touch /workspace/blocked"},
		Filesystem: sandbox.FilesystemPolicy{
			WorkspacePath:     "job",
			WorkspaceReadOnly: true,
		},
	})
	if err != nil {
		t.Fatalf("read-only workspace Execute() error = %v", err)
	}
	if readonlyWorkspaceResult.ExitCode == 0 {
		t.Fatal("read-only workspace unexpectedly allowed a write")
	}
	if _, err := os.Stat(filepath.Join(workspace, "blocked")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("read-only workspace created blocked file: %v", err)
	}

	plainTmpResult, err := backend.Execute(context.Background(), sandbox.ExecRequest{
		Command: "sh",
		Args:    []string{"-c", "touch /tmp/blocked"},
	})
	if err != nil {
		t.Fatalf("plain /tmp Execute() error = %v", err)
	}
	if plainTmpResult.ExitCode == 0 {
		t.Fatal("read-only root unexpectedly allowed /tmp write without tmpfs")
	}

	tmpfsResult, err := backend.Execute(context.Background(), sandbox.ExecRequest{
		Command: "sh",
		Args:    []string{"-c", "grep ' /tmp tmpfs ' /proc/mounts >/dev/null && printf ok >/tmp/result && cat /tmp/result"},
		Filesystem: sandbox.FilesystemPolicy{
			TempDir: true,
		},
	})
	if err != nil {
		t.Fatalf("tmpfs Execute() error = %v", err)
	}
	if tmpfsResult.ExitCode != 0 || string(tmpfsResult.Stdout) != "ok" {
		t.Fatalf("tmpfs result = %+v", tmpfsResult)
	}
}
