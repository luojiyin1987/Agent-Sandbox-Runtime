package docker

import (
	"bytes"
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

type networkFakeRunner struct {
	mu                 sync.Mutex
	calls              []runnerCall
	networkCreateError error
	blockStart         bool
}

func (f *networkFakeRunner) run(ctx context.Context, stdout, stderr io.Writer, args ...string) error {
	f.mu.Lock()
	f.calls = append(f.calls, runnerCall{args: slices.Clone(args)})
	f.mu.Unlock()

	switch args[0] {
	case "network":
		if len(args) > 1 && args[1] == "create" {
			if f.networkCreateError != nil {
				return f.networkCreateError
			}
			_, _ = io.WriteString(stdout, "network-id\n")
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
	}
	return nil
}

func (f *networkFakeRunner) snapshotCalls() []runnerCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.calls)
}

func newOutboundTestBackend(t *testing.T, fake *networkFakeRunner) *Backend {
	t.Helper()
	backend, err := New("alpine:3.22", WithOutboundNetwork())
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	backend.run = fake.run
	return backend
}

func TestExecuteCompilesOutboundNetwork(t *testing.T) {
	fake := &networkFakeRunner{}
	backend := newOutboundTestBackend(t, fake)

	_, err := backend.Execute(context.Background(), sandbox.ExecRequest{
		Command: "true",
		Network: sandbox.NetworkPolicy{Mode: sandbox.NetworkOutbound},
	})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}

	calls := fake.snapshotCalls()
	if len(calls) != 6 {
		t.Fatalf("docker calls = %d, want 6: %+v", len(calls), calls)
	}
	if len(calls[0].args) < 2 || calls[0].args[0] != "network" || calls[0].args[1] != "create" {
		t.Fatalf("first Docker call = %+v, want network create", calls[0].args)
	}

	networkCreate := strings.Join(calls[0].args, " ")
	for _, want := range []string{
		"network create",
		"--driver bridge",
		"--opt com.docker.network.bridge.enable_icc=false",
		"--label " + outboundNetworkLabel,
	} {
		if !strings.Contains(networkCreate, want) {
			t.Errorf("network create args %q missing %q", networkCreate, want)
		}
	}
	networkName := calls[0].args[len(calls[0].args)-1]
	if !strings.HasPrefix(networkName, "agent-sandbox-net-") {
		t.Fatalf("network name = %q", networkName)
	}

	attachedNetwork, ok := argValue(calls[1].args, "--network")
	if !ok || attachedNetwork != networkName {
		t.Fatalf("container network = %q, want %q", attachedNetwork, networkName)
	}
	if calls[len(calls)-2].args[0] != "rm" {
		t.Fatalf("penultimate Docker call = %+v, want container rm", calls[len(calls)-2].args)
	}
	last := calls[len(calls)-1].args
	if len(last) != 3 || last[0] != "network" || last[1] != "rm" || last[2] != networkName {
		t.Fatalf("last Docker call = %+v, want network rm %q", last, networkName)
	}
}

func TestExecuteOutboundRequiresTrustedCapability(t *testing.T) {
	fake := &networkFakeRunner{}
	backend := &Backend{image: "alpine:3.22", run: fake.run}

	_, err := backend.Execute(context.Background(), sandbox.ExecRequest{
		Command: "true",
		Network: sandbox.NetworkPolicy{Mode: sandbox.NetworkOutbound},
	})
	if !errors.Is(err, ErrUnsupportedPolicy) {
		t.Fatalf("Execute() error = %v, want ErrUnsupportedPolicy", err)
	}
	if calls := fake.snapshotCalls(); len(calls) != 0 {
		t.Fatalf("Docker called before outbound capability rejection: %+v", calls)
	}
}

func TestExecuteAllowlistRemainsUnsupportedWithOutboundCapability(t *testing.T) {
	fake := &networkFakeRunner{}
	backend := newOutboundTestBackend(t, fake)

	_, err := backend.Execute(context.Background(), sandbox.ExecRequest{
		Command: "true",
		Network: sandbox.NetworkPolicy{
			Mode:  sandbox.NetworkAllowlist,
			Allow: []string{"example.com:443"},
		},
	})
	if !errors.Is(err, ErrUnsupportedPolicy) {
		t.Fatalf("Execute() error = %v, want ErrUnsupportedPolicy", err)
	}
	if calls := fake.snapshotCalls(); len(calls) != 0 {
		t.Fatalf("Docker called before allowlist rejection: %+v", calls)
	}
}

func TestExecuteOutboundNetworkCreateFailureStillAttemptsCleanup(t *testing.T) {
	fake := &networkFakeRunner{networkCreateError: errors.New("connection reset")}
	backend := newOutboundTestBackend(t, fake)

	_, err := backend.Execute(context.Background(), sandbox.ExecRequest{
		Command: "true",
		Network: sandbox.NetworkPolicy{Mode: sandbox.NetworkOutbound},
	})
	if err == nil {
		t.Fatal("Execute() error = nil, want Docker network create failure")
	}

	calls := fake.snapshotCalls()
	if len(calls) != 2 {
		t.Fatalf("Docker calls = %+v, want network create then network rm", calls)
	}
	if calls[0].args[0] != "network" || calls[0].args[1] != "create" || calls[1].args[0] != "network" || calls[1].args[1] != "rm" {
		t.Fatalf("network create failure cleanup calls = %+v", calls)
	}
	created := calls[0].args[len(calls[0].args)-1]
	if calls[1].args[2] != created {
		t.Fatalf("cleanup network = %q, want %q", calls[1].args[2], created)
	}
}

func TestExecuteOutboundTimeoutCleansContainerBeforeNetwork(t *testing.T) {
	fake := &networkFakeRunner{blockStart: true}
	backend := newOutboundTestBackend(t, fake)

	result, err := backend.Execute(context.Background(), sandbox.ExecRequest{
		Command: "sleep",
		Args:    []string{"60"},
		Timeout: 10 * time.Millisecond,
		Network: sandbox.NetworkPolicy{Mode: sandbox.NetworkOutbound},
	})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Execute() error = %v, want DeadlineExceeded", err)
	}
	if result.Termination != sandbox.TerminationTimeout {
		t.Fatalf("Termination = %q, want %q", result.Termination, sandbox.TerminationTimeout)
	}

	calls := fake.snapshotCalls()
	if len(calls) < 5 {
		t.Fatalf("Docker calls = %+v", calls)
	}
	if calls[len(calls)-2].args[0] != "rm" {
		t.Fatalf("container cleanup was not before network cleanup: %+v", calls)
	}
	last := calls[len(calls)-1].args
	if len(last) < 2 || last[0] != "network" || last[1] != "rm" {
		t.Fatalf("last Docker call = %+v, want network rm", last)
	}
}

func TestDockerBackendIntegrationOutboundNetwork(t *testing.T) {
	if os.Getenv("SANDBOX_DOCKER_INTEGRATION") != "1" {
		t.Skip("set SANDBOX_DOCKER_INTEGRATION=1 to run Docker integration test")
	}

	backend, err := New("alpine:3.22", WithOutboundNetwork())
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	noneResult, err := backend.Execute(context.Background(), sandbox.ExecRequest{
		Command: "sh",
		Args:    []string{"-c", `awk '$2=="00000000"{found=1} END{exit found}' /proc/net/route`},
	})
	if err != nil {
		t.Fatalf("NetworkNone Execute() error = %v", err)
	}
	if noneResult.ExitCode != 0 {
		t.Fatalf("NetworkNone unexpectedly had a default route: %+v", noneResult)
	}

	outboundResult, err := backend.Execute(context.Background(), sandbox.ExecRequest{
		Command: "sh",
		Args:    []string{"-c", `awk '$2=="00000000"{found=1} END{exit !found}' /proc/net/route`},
		Network: sandbox.NetworkPolicy{Mode: sandbox.NetworkOutbound},
	})
	if err != nil {
		t.Fatalf("NetworkOutbound Execute() error = %v", err)
	}
	if outboundResult.ExitCode != 0 {
		t.Fatalf("NetworkOutbound had no default route: %+v", outboundResult)
	}

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	if err := runDocker(
		context.Background(),
		&stdout,
		&stderr,
		"network", "ls",
		"--filter", "label="+outboundNetworkLabel,
		"--format", "{{.Name}}",
	); err != nil {
		t.Fatalf("docker network ls failed: %v (%s)", err, strings.TrimSpace(stderr.String()))
	}
	if leaked := strings.TrimSpace(stdout.String()); leaked != "" {
		t.Fatalf("execution networks leaked after cleanup: %q", leaked)
	}
}
