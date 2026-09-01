package docker

import (
	"context"
	"io"
	"strings"
	"testing"
)

func TestWithContainerRuntimeInjectsOnlyCreate(t *testing.T) {
	var calls [][]string
	backend := &Backend{
		run: func(_ context.Context, _, _ io.Writer, args ...string) error {
			calls = append(calls, append([]string(nil), args...))
			return nil
		},
	}
	if err := WithContainerRuntime("runsc")(backend); err != nil {
		t.Fatalf("WithContainerRuntime() error = %v", err)
	}

	if err := backend.run(context.Background(), io.Discard, io.Discard, "create", "--name", "demo", "alpine:3.22", "true"); err != nil {
		t.Fatalf("create runner error = %v", err)
	}
	if err := backend.run(context.Background(), io.Discard, io.Discard, "start", "demo"); err != nil {
		t.Fatalf("start runner error = %v", err)
	}

	if got := strings.Join(calls[0], " "); got != "create --runtime runsc --name demo alpine:3.22 true" {
		t.Fatalf("create args = %q", got)
	}
	if got := strings.Join(calls[1], " "); got != "start demo" {
		t.Fatalf("start args = %q", got)
	}
}

func TestWithContainerRuntimeRejectsInvalidName(t *testing.T) {
	backend := &Backend{run: runDocker}
	for _, name := range []string{"", "   ", "runsc\nother"} {
		if err := WithContainerRuntime(name)(backend); err == nil {
			t.Fatalf("WithContainerRuntime(%q) error = nil", name)
		}
	}
}
