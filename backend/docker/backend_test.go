package docker

import (
	"context"
	"errors"
	"io"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	sandbox "github.com/luojiyin1987/Agent-Sandbox-Runtime"
)

type runnerCall struct {
	args []string
	env  []string
}

type fakeRunner struct {
	mu          sync.Mutex
	calls       []runnerCall
	exitCode    string
	startOut    string
	startErr    string
	createError error
	blockStart  bool
}

func (f *fakeRunner) run(ctx context.Context, env []string, stdout, stderr io.Writer, args ...string) error {
	f.mu.Lock()
	f.calls = append(f.calls, runnerCall{
		args: slices.Clone(args),
		env:  slices.Clone(env),
	})
	f.mu.Unlock()

	switch args[0] {
	case "create":
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
		_, _ = io.WriteString(stdout, f.exitCode+"\n")
	}
	return nil
}

func (f *fakeRunner) snapshotCalls() []runnerCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.calls)
}

func TestExecutePreservesWorkloadResult(t *testing.T) {
	fake := &fakeRunner{exitCode: "7", startOut: "hello", startErr: "warning"}
	backend := &Backend{image: "alpine:3.22", run: fake.run}

	result, err := backend.Execute(context.Background(), sandbox.ExecRequest{
		Command: "sh",
		Args:    []string{"-c", "exit 7"},
		Env: map[string]string{
			"TOKEN": "secret-value",
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
		"--env TOKEN",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("docker create args %q missing %q", joined, want)
		}
	}
	if strings.Contains(joined, "secret-value") {
		t.Fatalf("environment value leaked into docker argv: %q", joined)
	}
	if !slices.Contains(create.env, "TOKEN=secret-value") {
		t.Fatalf("docker process environment does not contain request value")
	}
	if calls[len(calls)-1].args[0] != "rm" {
		t.Fatalf("last docker call = %q, want rm", calls[len(calls)-1].args[0])
	}
}

func TestExecuteRejectsUnsupportedPolicyBeforeDocker(t *testing.T) {
	fake := &fakeRunner{exitCode: "0"}
	backend := &Backend{image: "alpine:3.22", run: fake.run}

	_, err := backend.Execute(context.Background(), sandbox.ExecRequest{
		Command: "true",
		Network: sandbox.NetworkPolicy{Mode: sandbox.NetworkOutbound},
	})
	if !errors.Is(err, ErrUnsupportedPolicy) {
		t.Fatalf("Execute() error = %v, want ErrUnsupportedPolicy", err)
	}
	if calls := fake.snapshotCalls(); len(calls) != 0 {
		t.Fatalf("docker was called before policy rejection: %+v", calls)
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

	backend, err := New("alpine:3.22")
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	result, err := backend.Execute(context.Background(), sandbox.ExecRequest{
		Command: "sh",
		Args:    []string{"-c", `printf '%s:%s' "$GREETING" "$PWD"; printf 'warning' >&2; exit 7`},
		Env: map[string]string{
			"GREETING": "hello",
		},
		WorkDir: "/tmp",
	})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if result.ExitCode != 7 || result.Termination != sandbox.TerminationCompleted {
		t.Fatalf("Execute() result = %+v", result)
	}
	if string(result.Stdout) != "hello:/tmp" || string(result.Stderr) != "warning" {
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
}
