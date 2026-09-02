package docker

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	sandbox "github.com/luojiyin1987/Agent-Sandbox-Runtime"
)

type cleanupFailureRunner struct {
	mu                 sync.Mutex
	calls              []runnerCall
	blockStart         bool
	containerRemoveErr error
	networkRemoveErr   error
}

func (f *cleanupFailureRunner) run(ctx context.Context, stdout, stderr io.Writer, args ...string) error {
	f.mu.Lock()
	f.calls = append(f.calls, runnerCall{args: slices.Clone(args)})
	f.mu.Unlock()

	switch args[0] {
	case "network":
		if len(args) > 1 && args[1] == "create" {
			_, _ = io.WriteString(stdout, "network-id\n")
			return nil
		}
		if len(args) > 1 && args[1] == "rm" && f.networkRemoveErr != nil {
			_, _ = io.WriteString(stderr, "daemon refused network removal")
			return f.networkRemoveErr
		}
	case "create":
		_, _ = io.WriteString(stdout, "container-id\n")
	case "start":
		if f.blockStart {
			<-ctx.Done()
			return ctx.Err()
		}
	case "inspect":
		_, _ = io.WriteString(stdout, "0 false\n")
	case "rm":
		if f.containerRemoveErr != nil {
			_, _ = io.WriteString(stderr, "daemon refused container removal")
			return f.containerRemoveErr
		}
	}
	return nil
}

func (f *cleanupFailureRunner) snapshotCalls() []runnerCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.calls)
}

func TestExecuteReturnsContainerCleanupFailure(t *testing.T) {
	fake := &cleanupFailureRunner{containerRemoveErr: errors.New("remove failed")}
	backend := &Backend{image: "alpine:3.22", run: fake.run}

	result, err := backend.Execute(context.Background(), sandbox.ExecRequest{Command: "true"})
	if !errors.Is(err, ErrCleanup) {
		t.Fatalf("Execute() error = %v, want ErrCleanup", err)
	}
	if result.Termination != sandbox.TerminationRuntimeError {
		t.Fatalf("Termination = %q, want %q", result.Termination, sandbox.TerminationRuntimeError)
	}
	if !strings.Contains(err.Error(), "daemon refused container removal") {
		t.Fatalf("Execute() error = %q, want Docker cleanup diagnostic", err)
	}
	if got := backend.Stats().CleanupFailures; got != 1 {
		t.Fatalf("CleanupFailures = %d, want 1", got)
	}
}

func TestCleanupFailureWritesStructuredLog(t *testing.T) {
	var logs bytes.Buffer
	fake := &cleanupFailureRunner{containerRemoveErr: errors.New("remove failed")}
	backend := &Backend{
		image:  "alpine:3.22",
		run:    fake.run,
		logger: slog.New(slog.NewJSONHandler(&logs, nil)),
	}

	_, err := backend.Execute(context.Background(), sandbox.ExecRequest{Command: "true"})
	if !errors.Is(err, ErrCleanup) {
		t.Fatalf("Execute() error = %v, want ErrCleanup", err)
	}
	for _, want := range []string{
		`"msg":"Docker resource cleanup failed"`,
		`"resource_type":"container"`,
		`"resource_name":"agent-sandbox-`,
	} {
		if !strings.Contains(logs.String(), want) {
			t.Fatalf("structured log %q missing %q", logs.String(), want)
		}
	}
}

func TestExecutePreservesTimeoutWhenCleanupAlsoFails(t *testing.T) {
	fake := &cleanupFailureRunner{
		blockStart:         true,
		containerRemoveErr: errors.New("remove failed"),
	}
	backend := &Backend{image: "alpine:3.22", run: fake.run}

	result, err := backend.Execute(context.Background(), sandbox.ExecRequest{
		Command: "sleep",
		Args:    []string{"60"},
		Timeout: 10 * time.Millisecond,
	})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Execute() error = %v, want DeadlineExceeded", err)
	}
	if !errors.Is(err, ErrCleanup) {
		t.Fatalf("Execute() error = %v, want joined ErrCleanup", err)
	}
	if result.Termination != sandbox.TerminationTimeout {
		t.Fatalf("Termination = %q, want %q", result.Termination, sandbox.TerminationTimeout)
	}
}

func TestExecuteReturnsNetworkCleanupFailureAfterContainerCleanup(t *testing.T) {
	fake := &cleanupFailureRunner{networkRemoveErr: errors.New("network remove failed")}
	backend, err := New(testImageDigest, WithOutboundNetwork())
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	backend.run = fake.run

	result, err := backend.Execute(context.Background(), sandbox.ExecRequest{
		Command: "true",
		Network: sandbox.NetworkPolicy{Mode: sandbox.NetworkOutbound},
	})
	if !errors.Is(err, ErrCleanup) {
		t.Fatalf("Execute() error = %v, want ErrCleanup", err)
	}
	if result.Termination != sandbox.TerminationRuntimeError {
		t.Fatalf("Termination = %q, want %q", result.Termination, sandbox.TerminationRuntimeError)
	}

	calls := fake.snapshotCalls()
	if len(calls) < 2 || calls[len(calls)-2].args[0] != "rm" {
		t.Fatalf("container cleanup was not attempted before network cleanup: %+v", calls)
	}
	last := calls[len(calls)-1].args
	if len(last) < 2 || last[0] != "network" || last[1] != "rm" {
		t.Fatalf("last cleanup call = %+v, want network rm", last)
	}
}

func TestRemoveTreatsAlreadyMissingResourcesAsClean(t *testing.T) {
	backend := &Backend{run: func(ctx context.Context, stdout, stderr io.Writer, args ...string) error {
		if args[0] == "rm" {
			_, _ = io.WriteString(stderr, "Error response from daemon: No such container: gone")
		} else {
			_, _ = io.WriteString(stderr, "Error response from daemon: network gone not found")
		}
		return errors.New("exit status 1")
	}}

	if err := backend.removeContainer("gone"); err != nil {
		t.Fatalf("removeContainer() error = %v, want nil for already absent resource", err)
	}
	if err := backend.removeNetwork("gone"); err != nil {
		t.Fatalf("removeNetwork() error = %v, want nil for already absent resource", err)
	}
}

func TestCreateArgsLabelsExecutionOwnedContainer(t *testing.T) {
	args := createArgs(
		"sandbox",
		"alpine:3.22",
		sandbox.ExecRequest{Command: "true"},
		"",
		"",
		"none",
	)
	label, ok := argValue(args, "--label")
	if !ok || label != executionResourceLabel {
		t.Fatalf("container label = %q, ok=%v, want %q", label, ok, executionResourceLabel)
	}
}
