package docker

import (
	"context"
	"errors"
	"io"
	"sync"
	"testing"

	sandbox "github.com/luojiyin1987/Agent-Sandbox-Runtime"
)

type admissionRunner struct {
	startOnce sync.Once
	started   chan struct{}
	release   chan struct{}
}

func (r *admissionRunner) run(_ context.Context, stdout, _ io.Writer, args ...string) error {
	switch args[0] {
	case "create":
		_, _ = io.WriteString(stdout, "container-id\n")
	case "start":
		r.startOnce.Do(func() { close(r.started) })
		<-r.release
	case "inspect":
		_, _ = io.WriteString(stdout, "0 false\n")
	}
	return nil
}

func TestExecuteRejectsAboveDefaultConcurrency(t *testing.T) {
	runner := &admissionRunner{started: make(chan struct{}), release: make(chan struct{})}
	backend := &Backend{image: "alpine:3.22", run: runner.run}

	firstDone := make(chan error, 1)
	go func() {
		_, err := backend.Execute(context.Background(), sandbox.ExecRequest{Command: "true"})
		firstDone <- err
	}()
	<-runner.started

	_, err := backend.Execute(context.Background(), sandbox.ExecRequest{Command: "true"})
	if !errors.Is(err, sandbox.ErrTooManyConcurrent) {
		t.Fatalf("second Execute() error = %v, want ErrTooManyConcurrent", err)
	}
	if got := backend.Stats().AdmissionRejected; got != 1 {
		t.Fatalf("AdmissionRejected = %d, want 1", got)
	}

	close(runner.release)
	if err := <-firstDone; err != nil {
		t.Fatalf("first Execute() error = %v", err)
	}
	if _, err := backend.Execute(context.Background(), sandbox.ExecRequest{Command: "true"}); err != nil {
		t.Fatalf("Execute() after release error = %v", err)
	}
}

func TestAdmissionPoolEnforcesAggregateBudgets(t *testing.T) {
	pool := newAdmissionPool(2, sandbox.ResourceLimits{
		MaxMemoryBytes: 512 << 20,
		MaxProcesses:   100,
		MaxOutputBytes: 2 << 20,
		MilliCPUs:      1000,
	})
	first := sandbox.ResourceLimits{MaxMemoryBytes: 400 << 20, MaxProcesses: 50, MaxOutputBytes: 1 << 20, MilliCPUs: 500}
	second := sandbox.ResourceLimits{MaxMemoryBytes: 200 << 20, MaxProcesses: 50, MaxOutputBytes: 1 << 20, MilliCPUs: 500}

	if !pool.tryAcquire(first) {
		t.Fatal("first admission was rejected")
	}
	if pool.tryAcquire(second) {
		t.Fatal("aggregate memory budget allowed excess admission")
	}
	pool.release(first)
	if !pool.tryAcquire(second) {
		t.Fatal("admission remained blocked after release")
	}
	pool.release(second)
}

func TestAdmissionOptionsRejectInvalidLimits(t *testing.T) {
	tests := []Option{
		WithMaxConcurrentSandboxes(0),
		WithAggregateResourceLimits(sandbox.ResourceLimits{MaxMemoryBytes: -1}),
	}
	for _, option := range tests {
		if _, err := New(testImageDigest, option); !errors.Is(err, sandbox.ErrInvalidRequest) {
			t.Fatalf("New() error = %v, want ErrInvalidRequest", err)
		}
	}
}

func TestAdmissionOptionsConfigureBackend(t *testing.T) {
	aggregate := sandbox.ResourceLimits{
		MaxMemoryBytes: 1 << 30,
		MaxProcesses:   256,
		MaxOutputBytes: 8 << 20,
		MilliCPUs:      2000,
	}
	backend, err := New(
		testImageDigest,
		WithMaxConcurrentSandboxes(4),
		WithAggregateResourceLimits(aggregate),
	)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	if backend.maxConcurrent != 4 {
		t.Fatalf("maxConcurrent = %d, want 4", backend.maxConcurrent)
	}
	if backend.aggregateLimits != aggregate {
		t.Fatalf("aggregateLimits = %+v, want %+v", backend.aggregateLimits, aggregate)
	}
}
