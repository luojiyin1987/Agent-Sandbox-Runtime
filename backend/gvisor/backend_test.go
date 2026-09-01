package gvisor

import (
	"errors"
	"os"
	"testing"

	sandbox "github.com/luojiyin1987/Agent-Sandbox-Runtime"
	"github.com/luojiyin1987/Agent-Sandbox-Runtime/internal/conformance"
)

const testImageDigest = "alpine:3.22@sha256:14358309a308569c32bdc37e2e0e9694be33a9d99e68afb0f5ff33cc1f695dce"

func TestNewRejectsInvalidWorkspaceRoot(t *testing.T) {
	if _, err := New(testImageDigest, WithWorkspaceRoot("")); err == nil {
		t.Fatal("New() error = nil, want invalid workspace root")
	}
}

func TestNewRejectsMutableImageReferenceByDefault(t *testing.T) {
	_, err := New("alpine:3.22")
	if !errors.Is(err, sandbox.ErrInvalidRequest) {
		t.Fatalf("New() error = %v, want ErrInvalidRequest", err)
	}
}

func TestNewAllowsTrustedMutableImageReference(t *testing.T) {
	if _, err := New("alpine:3.22", WithMutableImageReference()); err != nil {
		t.Fatalf("New() error = %v", err)
	}
}

func TestGVisorConformanceIntegration(t *testing.T) {
	if os.Getenv("SANDBOX_GVISOR_INTEGRATION") != "1" {
		t.Skip("set SANDBOX_GVISOR_INTEGRATION=1 to run gVisor conformance")
	}

	conformance.Run(t, func(t *testing.T, workspaceRoot string) sandbox.Runtime {
		t.Helper()
		backend, err := New(testImageDigest, WithWorkspaceRoot(workspaceRoot))
		if err != nil {
			t.Fatalf("New() error = %v", err)
		}
		return backend
	})
}
