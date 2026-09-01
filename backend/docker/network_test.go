package docker

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	sandbox "github.com/luojiyin1987/Agent-Sandbox-Runtime"
)

func TestExecuteCompilesOutboundNetwork(t *testing.T) {
	fake := &fakeRunner{exitCode: "0"}
	backend := &Backend{image: "alpine:3.22", run: fake.run}

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

func TestExecuteOutboundNetworkCreateFailureStillAttemptsCleanup(t *testing.T) {
	fake := &fakeRunner{
		exitCode:          "0",
		networkCreateError: errors.New("connection reset"),
	}
	backend := &Backend{image: "alpine:3.22", run: fake.run}

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
	fake := &fakeRunner{exitCode: "0", blockStart: true}
	backend := &Backend{image: "alpine:3.22", run: fake.run}

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
