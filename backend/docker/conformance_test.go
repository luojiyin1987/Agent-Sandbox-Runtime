package docker

import (
	"os"
	"testing"

	sandbox "github.com/luojiyin1987/Agent-Sandbox-Runtime"
	"github.com/luojiyin1987/Agent-Sandbox-Runtime/internal/conformance"
)

func TestDockerConformanceIntegration(t *testing.T) {
	if os.Getenv("SANDBOX_DOCKER_INTEGRATION") != "1" {
		t.Skip("set SANDBOX_DOCKER_INTEGRATION=1 to run Docker conformance")
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
