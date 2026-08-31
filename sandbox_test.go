package sandbox

import (
	"errors"
	"testing"
	"time"
)

func TestExecRequestSafeDefaults(t *testing.T) {
	req := ExecRequest{Command: "true"}
	if err := req.Validate(); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
	if got := req.EffectiveNetworkMode(); got != NetworkNone {
		t.Fatalf("EffectiveNetworkMode() = %q, want %q", got, NetworkNone)
	}
	if got := req.EffectiveRootFilesystemMode(); got != RootReadOnly {
		t.Fatalf("EffectiveRootFilesystemMode() = %q, want %q", got, RootReadOnly)
	}
}

func TestExecRequestValidation(t *testing.T) {
	tests := []struct {
		name string
		req  ExecRequest
	}{
		{name: "missing command", req: ExecRequest{}},
		{name: "negative timeout", req: ExecRequest{Command: "true", Timeout: -time.Second}},
		{name: "negative memory", req: ExecRequest{Command: "true", Resources: ResourceLimits{MaxMemoryBytes: -1}}},
		{name: "allow entries without mode", req: ExecRequest{Command: "true", Network: NetworkPolicy{Allow: []string{"example.com"}}}},
		{name: "empty allowlist", req: ExecRequest{Command: "true", Network: NetworkPolicy{Mode: NetworkAllowlist}}},
		{name: "unknown network mode", req: ExecRequest{Command: "true", Network: NetworkPolicy{Mode: "host"}}},
		{name: "unknown filesystem mode", req: ExecRequest{Command: "true", Filesystem: FilesystemPolicy{Root: "host"}}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := tt.req.Validate(); !errors.Is(err, ErrInvalidRequest) {
				t.Fatalf("Validate() error = %v, want ErrInvalidRequest", err)
			}
		})
	}
}

func TestExecRequestAllowsExplicitPolicies(t *testing.T) {
	req := ExecRequest{
		Command: "sh",
		Args:    []string{"-c", "echo ok"},
		Timeout: 5 * time.Second,
		Resources: ResourceLimits{
			MaxMemoryBytes: 128 << 20,
			MaxProcesses:   32,
			MaxOutputBytes: 1 << 20,
			MilliCPUs:      500,
		},
		Network: NetworkPolicy{
			Mode:  NetworkAllowlist,
			Allow: []string{"api.example.com:443"},
		},
		Filesystem: FilesystemPolicy{
			Root:              RootReadOnly,
			WorkspacePath:     "/workspace",
			WorkspaceReadOnly: false,
			TempDir:           true,
		},
	}

	if err := req.Validate(); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
}
