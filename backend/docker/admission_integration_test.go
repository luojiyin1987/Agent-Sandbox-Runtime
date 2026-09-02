package docker

import (
	"bytes"
	"context"
	"errors"
	"maps"
	"os"
	"strings"
	"testing"
	"time"

	sandbox "github.com/luojiyin1987/Agent-Sandbox-Runtime"
)

// TestDockerAdmissionControlIntegration proves admission limits against a real Docker daemon.
func TestDockerAdmissionControlIntegration(t *testing.T) {
	if os.Getenv("SANDBOX_DOCKER_INTEGRATION") != "1" {
		t.Skip("set SANDBOX_DOCKER_INTEGRATION=1 to run Docker integration test")
	}

	type outcome struct {
		result sandbox.ExecResult
		err    error
	}

	dockerIDs := func(t *testing.T, args ...string) map[string]struct{} {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		var stdout bytes.Buffer
		var stderr bytes.Buffer
		if err := runDocker(ctx, &stdout, &stderr, args...); err != nil {
			t.Fatalf("docker %s failed: %v (%s)", strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
		}

		ids := make(map[string]struct{})
		for _, id := range strings.Fields(stdout.String()) {
			ids[id] = struct{}{}
		}
		return ids
	}

	containerIDs := func(t *testing.T, runningOnly bool) map[string]struct{} {
		t.Helper()
		args := []string{"ps", "-q"}
		if !runningOnly {
			args = []string{"ps", "-aq"}
		}
		args = append(args, "--filter", "label="+executionResourceLabel)
		return dockerIDs(t, args...)
	}

	networkIDs := func(t *testing.T) map[string]struct{} {
		t.Helper()
		return dockerIDs(t, "network", "ls", "-q", "--filter", "label="+outboundNetworkLabel)
	}

	waitForRunningDelta := func(t *testing.T, baseline map[string]struct{}) {
		t.Helper()
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			current := containerIDs(t, true)
			added := 0
			for id := range current {
				if _, ok := baseline[id]; !ok {
					added++
				}
			}
			if added == 1 {
				return
			}
			if added > 1 {
				t.Fatalf("unexpected execution containers: baseline=%v current=%v", baseline, current)
			}
			time.Sleep(50 * time.Millisecond)
		}
		t.Fatal("timed out waiting for the admitted Docker execution to start")
	}

	waitForOutcome := func(t *testing.T, done <-chan outcome) outcome {
		t.Helper()
		select {
		case got := <-done:
			return got
		case <-time.After(10 * time.Second):
			t.Fatal("timed out waiting for Docker execution cleanup")
			return outcome{}
		}
	}

	startExecution := func(ctx context.Context, backend *Backend, resources sandbox.ResourceLimits) <-chan outcome {
		done := make(chan outcome, 1)
		go func() {
			result, err := backend.Execute(ctx, sandbox.ExecRequest{
				Command:   "sleep",
				Args:      []string{"30"},
				Timeout:   20 * time.Second,
				Resources: resources,
			})
			done <- outcome{result: result, err: err}
		}()
		return done
	}

	t.Run("concurrency ceiling rejects before Docker side effects", func(t *testing.T) {
		baselineContainers := containerIDs(t, false)
		baselineRunning := containerIDs(t, true)
		baselineNetworks := networkIDs(t)

		resources := sandbox.ResourceLimits{
			MaxMemoryBytes: 64 << 20,
			MaxProcesses:   16,
			MaxOutputBytes: 256 << 10,
			MilliCPUs:      250,
		}
		backend, err := New(
			testImageDigest,
			WithOutboundNetwork(),
			WithAdmissionLimits(sandbox.AdmissionLimits{
				MaxConcurrent:  1,
				MaxMemoryBytes: 128 << 20,
				MaxProcesses:   64,
				MaxOutputBytes: 1 << 20,
				MilliCPUs:      1000,
			}),
		)
		if err != nil {
			t.Fatalf("New() error = %v", err)
		}

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		firstDone := startExecution(ctx, backend, resources)
		waitForRunningDelta(t, baselineRunning)

		containersBeforeReject := containerIDs(t, false)
		networksBeforeReject := networkIDs(t)
		_, err = backend.Execute(context.Background(), sandbox.ExecRequest{
			Command:   "true",
			Resources: resources,
			Network:   sandbox.NetworkPolicy{Mode: sandbox.NetworkOutbound},
		})
		if !errors.Is(err, sandbox.ErrTooManyConcurrent) {
			t.Fatalf("second Execute() error = %v, want ErrTooManyConcurrent", err)
		}
		if !maps.Equal(containerIDs(t, false), containersBeforeReject) {
			t.Fatal("admission rejection changed the execution-container set")
		}
		if !maps.Equal(networkIDs(t), networksBeforeReject) {
			t.Fatal("admission rejection changed the execution-network set")
		}
		if stats := backend.Stats(); stats.ActiveExecutions != 1 || stats.AdmissionRejected != 1 {
			t.Fatalf("Stats() while first execution is active = %+v", stats)
		}

		cancel()
		first := waitForOutcome(t, firstDone)
		if !errors.Is(first.err, context.Canceled) || first.result.Termination != sandbox.TerminationCancelled {
			t.Fatalf("cancelled first execution = result %+v, error %v", first.result, first.err)
		}
		if !maps.Equal(containerIDs(t, false), baselineContainers) {
			t.Fatal("execution containers did not return to the baseline after cleanup")
		}
		if !maps.Equal(networkIDs(t), baselineNetworks) {
			t.Fatal("execution networks did not return to the baseline after cleanup")
		}
	})

	t.Run("aggregate memory budget rejects concurrent amplification", func(t *testing.T) {
		baselineContainers := containerIDs(t, false)
		baselineRunning := containerIDs(t, true)
		baselineNetworks := networkIDs(t)

		firstResources := sandbox.ResourceLimits{
			MaxMemoryBytes: 96 << 20,
			MaxProcesses:   32,
			MaxOutputBytes: 256 << 10,
			MilliCPUs:      250,
		}
		secondResources := sandbox.ResourceLimits{
			MaxMemoryBytes: 64 << 20,
			MaxProcesses:   32,
			MaxOutputBytes: 256 << 10,
			MilliCPUs:      250,
		}
		backend, err := New(
			testImageDigest,
			WithOutboundNetwork(),
			WithAdmissionLimits(sandbox.AdmissionLimits{
				MaxConcurrent:  2,
				MaxMemoryBytes: 128 << 20,
				MaxProcesses:   128,
				MaxOutputBytes: 2 << 20,
				MilliCPUs:      1000,
			}),
		)
		if err != nil {
			t.Fatalf("New() error = %v", err)
		}

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		firstDone := startExecution(ctx, backend, firstResources)
		waitForRunningDelta(t, baselineRunning)

		containersBeforeReject := containerIDs(t, false)
		networksBeforeReject := networkIDs(t)
		_, err = backend.Execute(context.Background(), sandbox.ExecRequest{
			Command:   "true",
			Resources: secondResources,
			Network:   sandbox.NetworkPolicy{Mode: sandbox.NetworkOutbound},
		})
		if !errors.Is(err, sandbox.ErrTooManyConcurrent) || !strings.Contains(err.Error(), "memory capacity is exhausted") {
			t.Fatalf("second Execute() error = %v, want temporary memory admission rejection", err)
		}
		if !maps.Equal(containerIDs(t, false), containersBeforeReject) {
			t.Fatal("memory admission rejection changed the execution-container set")
		}
		if !maps.Equal(networkIDs(t), networksBeforeReject) {
			t.Fatal("memory admission rejection changed the execution-network set")
		}

		cancel()
		first := waitForOutcome(t, firstDone)
		if !errors.Is(first.err, context.Canceled) || first.result.Termination != sandbox.TerminationCancelled {
			t.Fatalf("cancelled first execution = result %+v, error %v", first.result, first.err)
		}

		result, err := backend.Execute(context.Background(), sandbox.ExecRequest{
			Command:   "true",
			Resources: secondResources,
		})
		if err != nil || result.ExitCode != 0 {
			t.Fatalf("Execute() after reservation release = result %+v, error %v", result, err)
		}
		if stats := backend.Stats(); stats.ActiveExecutions != 0 || stats.ReservedMemoryBytes != 0 {
			t.Fatalf("Stats() after reservation release = %+v", stats)
		}
		if !maps.Equal(containerIDs(t, false), baselineContainers) {
			t.Fatal("execution containers did not return to the baseline after release")
		}
		if !maps.Equal(networkIDs(t), baselineNetworks) {
			t.Fatal("execution networks did not return to the baseline after release")
		}
	})
}
