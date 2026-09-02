package docker

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

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

func waitForSignal(t *testing.T, signal <-chan struct{}) {
	t.Helper()
	select {
	case <-signal:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out while waiting for execution signal")
	}
}

func waitForExecution(t *testing.T, result <-chan error) error {
	t.Helper()
	select {
	case err := <-result:
		return err
	case <-time.After(2 * time.Second):
		t.Fatal("timed out while waiting for execution result")
		return nil
	}
}

func TestExecuteRejectsAboveDefaultConcurrency(t *testing.T) {
	runner := &admissionRunner{started: make(chan struct{}), release: make(chan struct{})}
	backend := &Backend{image: "alpine:3.22", run: runner.run}

	firstDone := make(chan error, 1)
	go func() {
		_, err := backend.Execute(context.Background(), sandbox.ExecRequest{Command: "true"})
		firstDone <- err
	}()
	waitForSignal(t, runner.started)

	stats := backend.Stats()
	if stats.ActiveExecutions != 1 || stats.ReservedMemoryBytes != defaultMemoryBytes || stats.ReservedProcesses != defaultProcesses {
		t.Fatalf("active Stats() = %+v", stats)
	}
	_, err := backend.Execute(context.Background(), sandbox.ExecRequest{Command: "true"})
	if !errors.Is(err, sandbox.ErrTooManyConcurrent) {
		t.Fatalf("second Execute() error = %v, want ErrTooManyConcurrent", err)
	}
	if got := backend.Stats().AdmissionRejected; got != 1 {
		t.Fatalf("AdmissionRejected = %d, want 1", got)
	}

	close(runner.release)
	if err := waitForExecution(t, firstDone); err != nil {
		t.Fatalf("first Execute() error = %v", err)
	}
	if stats := backend.Stats(); stats.ActiveExecutions != 0 || stats.ReservedMemoryBytes != 0 || stats.ReservedProcesses != 0 || stats.ReservedOutputBytes != 0 || stats.ReservedMilliCPUs != 0 {
		t.Fatalf("released Stats() = %+v", stats)
	}
	if _, err := backend.Execute(context.Background(), sandbox.ExecRequest{Command: "true"}); err != nil {
		t.Fatalf("Execute() after release error = %v", err)
	}
}

func TestAdmissionPoolEnforcesEachAggregateBudget(t *testing.T) {
	tests := []struct {
		name   string
		first  sandbox.ResourceLimits
		second sandbox.ResourceLimits
		reason string
	}{
		{name: "memory", first: sandbox.ResourceLimits{MaxMemoryBytes: 60}, second: sandbox.ResourceLimits{MaxMemoryBytes: 60}, reason: "memory"},
		{name: "processes", first: sandbox.ResourceLimits{MaxProcesses: 60}, second: sandbox.ResourceLimits{MaxProcesses: 60}, reason: "processes"},
		{name: "output", first: sandbox.ResourceLimits{MaxOutputBytes: 60}, second: sandbox.ResourceLimits{MaxOutputBytes: 60}, reason: "output"},
		{name: "CPU", first: sandbox.ResourceLimits{MilliCPUs: 60}, second: sandbox.ResourceLimits{MilliCPUs: 60}, reason: "cpu"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pool := newAdmissionPool(sandbox.AdmissionLimits{
				MaxConcurrent:  2,
				MaxMemoryBytes: 100,
				MaxProcesses:   100,
				MaxOutputBytes: 100,
				MilliCPUs:      100,
			})
			if decision := pool.tryAcquire(tt.first); decision.status != admissionAccepted {
				t.Fatalf("first admission = %+v, want accepted", decision)
			}
			decision := pool.tryAcquire(tt.second)
			if decision.status != admissionBusy || decision.reason != tt.reason {
				t.Fatalf("second admission = %+v, want busy %q", decision, tt.reason)
			}
			pool.release(tt.first)
			if decision := pool.tryAcquire(tt.second); decision.status != admissionAccepted {
				t.Fatalf("admission after release = %+v, want accepted", decision)
			}
			pool.release(tt.second)
		})
	}
}

func TestAdmissionPoolClassifiesOversizedRequests(t *testing.T) {
	limits := sandbox.AdmissionLimits{
		MaxConcurrent:  1,
		MaxMemoryBytes: 100,
		MaxProcesses:   100,
		MaxOutputBytes: 100,
		MilliCPUs:      100,
	}
	tests := []struct {
		request sandbox.ResourceLimits
		reason  string
	}{
		{request: sandbox.ResourceLimits{MaxMemoryBytes: 101}, reason: "memory"},
		{request: sandbox.ResourceLimits{MaxProcesses: 101}, reason: "processes"},
		{request: sandbox.ResourceLimits{MaxOutputBytes: 101}, reason: "output"},
		{request: sandbox.ResourceLimits{MilliCPUs: 101}, reason: "cpu"},
	}
	for _, tt := range tests {
		decision := newAdmissionPool(limits).tryAcquire(tt.request)
		if decision.status != admissionRequestTooLarge || decision.reason != tt.reason {
			t.Fatalf("admission = %+v, want request-too-large %q", decision, tt.reason)
		}
	}
}

func TestExecuteRejectsRequestAboveTrustedLimits(t *testing.T) {
	var logs bytes.Buffer
	backend, err := New(testImageDigest, WithLogger(slog.New(slog.NewJSONHandler(&logs, nil))))
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	_, err = backend.Execute(context.Background(), sandbox.ExecRequest{
		Command: "true",
		Resources: sandbox.ResourceLimits{
			MaxMemoryBytes: defaultMemoryBytes + 1,
		},
	})
	if !errors.Is(err, sandbox.ErrResourceLimitExceeded) {
		t.Fatalf("Execute() error = %v, want ErrResourceLimitExceeded", err)
	}
	for _, want := range []string{`"reason":"memory"`, `"permanent":true`, `"output_bytes":1048576`} {
		if !strings.Contains(logs.String(), want) {
			t.Fatalf("admission log %q missing %q", logs.String(), want)
		}
	}
}

func TestAdmissionReleasesAfterCreateFailure(t *testing.T) {
	fake := &fakeRunner{createError: errors.New("create failed")}
	backend := &Backend{image: "alpine:3.22", run: fake.run}
	_, _ = backend.Execute(context.Background(), sandbox.ExecRequest{Command: "true"})
	if stats := backend.Stats(); stats.ActiveExecutions != 0 || stats.ReservedMemoryBytes != 0 || stats.ReservedProcesses != 0 || stats.ReservedOutputBytes != 0 || stats.ReservedMilliCPUs != 0 {
		t.Fatalf("Stats() after create failure = %+v", stats)
	}
}

func TestAdmissionOptionsRejectInvalidLimits(t *testing.T) {
	tests := []sandbox.AdmissionLimits{
		{MaxConcurrent: -1},
		{MaxMemoryBytes: -1},
		{MilliCPUs: int64(runtime.NumCPU())*1000 + 1},
	}
	for _, limits := range tests {
		if _, err := New(testImageDigest, WithAdmissionLimits(limits)); !errors.Is(err, sandbox.ErrInvalidRequest) {
			t.Fatalf("New() error = %v, want ErrInvalidRequest", err)
		}
	}
}

func TestAdmissionOptionsConfigureBackend(t *testing.T) {
	limits := sandbox.AdmissionLimits{
		MaxConcurrent:  4,
		MaxMemoryBytes: 1 << 30,
		MaxProcesses:   256,
		MaxOutputBytes: 8 << 20,
		MilliCPUs:      1000,
	}
	backend, err := New(testImageDigest, WithAdmissionLimits(limits))
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	if backend.admissionLimits != limits {
		t.Fatalf("admissionLimits = %+v, want %+v", backend.admissionLimits, limits)
	}
}

func TestAdmissionOptionsApplySafeDefaults(t *testing.T) {
	backend, err := New(testImageDigest, WithAdmissionLimits(sandbox.AdmissionLimits{}))
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	if backend.admissionLimits != defaultAdmissionLimits() {
		t.Fatalf("admissionLimits = %+v, want defaults", backend.admissionLimits)
	}
}
