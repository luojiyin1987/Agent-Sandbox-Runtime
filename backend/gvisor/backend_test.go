package gvisor

import (
	"os"
	"testing"

	sandbox "github.com/luojiyin1987/Agent-Sandbox-Runtime"
	"github.com/luojiyin1987/Agent-Sandbox-Runtime/internal/conformance"
)

func TestNewRejectsInvalidWorkspaceRoot(t *testing.T) {
	if _, err := New("alpine:3.22", WithWorkspaceRoot("")); err == nil {
		t.Fatal("New() error = nil, want invalid workspace root")
	}
}

func TestGVisorConformanceIntegration(t *testing.T) {
	if os.Getenv("SANDBOX_GVISOR_INTEGRATION") != "1" {
		t.Skip("set SANDBOX_GVISOR_INTEGRATION=1 to run gVisor conformance")
	}

	conformance.Run(t, func(t *testing.T, workspaceRoot string) sandbox.Runtime {
		t.Helper()
		backend, err := New("alpine:3.22", WithWorkspaceRoot(workspaceRoot))
		if err != nil {
			t.Fatalf("New() error = %v", err)
		}
		return backend
	})
}
