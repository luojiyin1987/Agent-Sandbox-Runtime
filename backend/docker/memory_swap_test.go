package docker

import (
	"context"
	"os"
	"strings"
	"testing"

	sandbox "github.com/luojiyin1987/Agent-Sandbox-Runtime"
)

func TestExecuteDisablesSwapForMemoryCeiling(t *testing.T) {
	tests := []struct {
		name      string
		resources sandbox.ResourceLimits
		wantBytes string
	}{
		{
			name:      "safe default",
			resources: sandbox.ResourceLimits{},
			wantBytes: "268435456",
		},
		{
			name: "explicit limit",
			resources: sandbox.ResourceLimits{
				MaxMemoryBytes: 64 << 20,
			},
			wantBytes: "67108864",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fake := &fakeRunner{exitCode: "0"}
			backend := &Backend{image: "alpine:3.22", run: fake.run}

			_, err := backend.Execute(context.Background(), sandbox.ExecRequest{
				Command:   "true",
				Resources: tt.resources,
			})
			if err != nil {
				t.Fatalf("Execute() error = %v", err)
			}

			create := fake.snapshotCalls()[0].args
			memory, ok := argValue(create, "--memory")
			if !ok {
				t.Fatal("docker create args missing --memory")
			}
			memorySwap, ok := argValue(create, "--memory-swap")
			if !ok {
				t.Fatal("docker create args missing --memory-swap")
			}
			if memory != tt.wantBytes || memorySwap != tt.wantBytes {
				t.Fatalf("memory flags = --memory %s --memory-swap %s, want both %s", memory, memorySwap, tt.wantBytes)
			}
		})
	}
}

func TestDockerMemorySwapIntegration(t *testing.T) {
	if os.Getenv("SANDBOX_DOCKER_INTEGRATION") != "1" {
		t.Skip("set SANDBOX_DOCKER_INTEGRATION=1 to run Docker integration test")
	}

	backend, err := New("alpine:3.22")
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	result, err := backend.Execute(context.Background(), sandbox.ExecRequest{
		Command: "sh",
		Args: []string{"-c", `printf 'memory='; cat /sys/fs/cgroup/memory.max; printf 'swap='; cat /sys/fs/cgroup/memory.swap.max`},
		Resources: sandbox.ResourceLimits{
			MaxMemoryBytes: 64 << 20,
		},
	})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if result.ExitCode != 0 {
		t.Fatalf("Execute() result = %+v", result)
	}

	output := string(result.Stdout)
	for _, want := range []string{"memory=67108864\n", "swap=0\n"} {
		if !strings.Contains(output, want) {
			t.Fatalf("cgroup output %q missing %q", output, want)
		}
	}
}
