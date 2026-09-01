//go:build linux

package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"golang.org/x/sys/unix"
)

const landlockTestChildEnv = "AGENT_SANDBOX_LANDLOCK_TEST_CHILD"

func TestWriteAccessMaskTracksABI(t *testing.T) {
	abi1 := writeAccessMask(1)
	if abi1&uint64(unix.LANDLOCK_ACCESS_FS_REFER) != 0 {
		t.Fatal("ABI 1 mask unexpectedly includes REFER")
	}
	if abi1&uint64(unix.LANDLOCK_ACCESS_FS_TRUNCATE) != 0 {
		t.Fatal("ABI 1 mask unexpectedly includes TRUNCATE")
	}

	abi2 := writeAccessMask(2)
	if abi2&uint64(unix.LANDLOCK_ACCESS_FS_REFER) == 0 {
		t.Fatal("ABI 2 mask is missing REFER")
	}
	if abi2&uint64(unix.LANDLOCK_ACCESS_FS_TRUNCATE) != 0 {
		t.Fatal("ABI 2 mask unexpectedly includes TRUNCATE")
	}

	abi3 := writeAccessMask(3)
	if abi3&uint64(unix.LANDLOCK_ACCESS_FS_REFER) == 0 {
		t.Fatal("ABI 3 mask is missing REFER")
	}
	if abi3&uint64(unix.LANDLOCK_ACCESS_FS_TRUNCATE) == 0 {
		t.Fatal("ABI 3 mask is missing TRUNCATE")
	}
}

func TestLandlockConfinementIntegration(t *testing.T) {
	probe := probePlatform()
	if !probe.Available {
		t.Skipf("Landlock unavailable: %s", probe.Reason)
	}
	if !probe.WriteConfinement {
		t.Skipf("Landlock ABI %d is below required ABI %d", probe.ABI, minimumWriteConfinementABI)
	}

	root := t.TempDir()
	allowed := filepath.Join(root, "allowed")
	denied := filepath.Join(root, "denied")
	if err := os.MkdirAll(allowed, 0o700); err != nil {
		t.Fatalf("MkdirAll(allowed) error = %v", err)
	}
	if err := os.MkdirAll(denied, 0o700); err != nil {
		t.Fatalf("MkdirAll(denied) error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(denied, "seed.txt"), []byte("seed"), 0o600); err != nil {
		t.Fatalf("WriteFile(seed) error = %v", err)
	}

	cmd := exec.Command(os.Args[0], "-test.run=TestLandlockChildHelper")
	cmd.Env = append(os.Environ(),
		landlockTestChildEnv+"=1",
		childAllowedPathEnv+"="+allowed,
		childDeniedPathEnv+"="+denied,
	)
	output, err := cmd.Output()
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			t.Fatalf("Landlock helper error = %v, stderr = %s", err, exitErr.Stderr)
		}
		t.Fatalf("Landlock helper error = %v", err)
	}

	var result demoResult
	if err := json.Unmarshal(output, &result); err != nil {
		t.Fatalf("Unmarshal(%q) error = %v", output, err)
	}
	if !result.AllowedWriteSucceeded || !result.DeniedCreateBlocked || !result.DeniedTruncateBlocked || !result.DeniedPathReadSucceeded {
		t.Fatalf("Landlock demo result = %+v", result)
	}
	if probe.ABI >= 8 && result.Enforcement != "process-tsync" {
		t.Fatalf("enforcement = %q, want process-tsync for ABI %d", result.Enforcement, probe.ABI)
	}
	if probe.ABI < 8 && result.Enforcement != "thread-locked" {
		t.Fatalf("enforcement = %q, want thread-locked for ABI %d", result.Enforcement, probe.ABI)
	}
}

func TestLandlockChildHelper(t *testing.T) {
	if os.Getenv(landlockTestChildEnv) != "1" {
		return
	}

	result, err := runChildPlatform()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	if err := json.NewEncoder(os.Stdout).Encode(result); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	os.Exit(0)
}
