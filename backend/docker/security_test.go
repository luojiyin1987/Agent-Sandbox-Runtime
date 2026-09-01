package docker

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"

	sandbox "github.com/luojiyin1987/Agent-Sandbox-Runtime"
)

func TestCreateArgsAlwaysHardensProcessPrivileges(t *testing.T) {
	args := createArgs(
		"agent-sandbox-test",
		"alpine:3.22",
		sandbox.ExecRequest{Command: "true"},
		"",
		"",
		"none",
	)
	joined := strings.Join(args, " ")

	for _, want := range []string{
		fmt.Sprintf("--user %d:%d", os.Geteuid(), os.Getegid()),
		"--cap-drop ALL",
		"--security-opt no-new-privileges=true",
		"--security-opt seccomp=builtin",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("docker create args %q missing %q", joined, want)
		}
	}

	for _, forbidden := range []string{
		"--privileged",
		"--cap-add",
		"seccomp=unconfined",
	} {
		if strings.Contains(joined, forbidden) {
			t.Errorf("docker create args %q unexpectedly contain %q", joined, forbidden)
		}
	}
}

func TestDockerProcessHardeningIntegration(t *testing.T) {
	if os.Getenv("SANDBOX_DOCKER_INTEGRATION") != "1" {
		t.Skip("set SANDBOX_DOCKER_INTEGRATION=1 to run Docker integration test")
	}

	backend, err := New(testImageDigest)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	statusResult, err := backend.Execute(context.Background(), sandbox.ExecRequest{
		Command: "sh",
		Args: []string{"-c", `printf 'uid=%s gid=%s ' "$(id -u)" "$(id -g)"; awk '
/^CapEff:/ { cap=$2 }
/^NoNewPrivs:/ { nnp=$2 }
/^Seccomp:/ { seccomp=$2 }
END { printf "cap=%s nnp=%s seccomp=%s", cap, nnp, seccomp }
' /proc/self/status`},
	})
	if err != nil {
		t.Fatalf("status Execute() error = %v", err)
	}
	if statusResult.ExitCode != 0 {
		t.Fatalf("status result = %+v", statusResult)
	}
	wantStatus := fmt.Sprintf(
		"uid=%d gid=%d cap=0000000000000000 nnp=1 seccomp=2",
		os.Geteuid(),
		os.Getegid(),
	)
	if got := string(statusResult.Stdout); got != wantStatus {
		t.Fatalf("process security state = %q, want %q", got, wantStatus)
	}

	mknodResult, err := backend.Execute(context.Background(), sandbox.ExecRequest{
		Command: "sh",
		Args:    []string{"-c", "mknod /tmp/blocked c 1 3"},
		Filesystem: sandbox.FilesystemPolicy{
			TempDir: true,
		},
	})
	if err != nil {
		t.Fatalf("mknod Execute() error = %v", err)
	}
	if mknodResult.ExitCode == 0 {
		t.Fatal("mknod unexpectedly succeeded with all capabilities dropped")
	}

	mountResult, err := backend.Execute(context.Background(), sandbox.ExecRequest{
		Command: "sh",
		Args:    []string{"-c", "mkdir /tmp/mnt && mount -t tmpfs tmpfs /tmp/mnt"},
		Filesystem: sandbox.FilesystemPolicy{
			TempDir: true,
		},
	})
	if err != nil {
		t.Fatalf("mount Execute() error = %v", err)
	}
	if mountResult.ExitCode == 0 {
		t.Fatal("mount unexpectedly succeeded without CAP_SYS_ADMIN")
	}
}
