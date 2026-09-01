//go:build linux

package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"unsafe"

	"golang.org/x/sys/unix"
)

const (
	childAllowedPathEnv = "AGENT_SANDBOX_LANDLOCK_ALLOWED"
	childDeniedPathEnv  = "AGENT_SANDBOX_LANDLOCK_DENIED"
)

type rulesetAttr struct {
	HandledAccessFS uint64
}

type pathBeneathAttr struct {
	AllowedAccess uint64
	ParentFD      int32
	_             uint32
}

func probePlatform() probeResult {
	abi, err := landlockABI()
	if err != nil {
		result := probeResult{Reason: err.Error()}
		switch {
		case errors.Is(err, unix.ENOSYS):
			result.Reason = "Landlock is not supported by the running kernel"
		case errors.Is(err, unix.EOPNOTSUPP):
			result.Reason = "Landlock is supported but disabled"
		}
		return result
	}

	return probeResult{
		Available:        true,
		ABI:              abi,
		WriteConfinement: abi >= minimumWriteConfinementABI,
		ProcessWideTSYNC: abi >= 8,
	}
}

func demoPlatform() (demoResult, error) {
	probe := probePlatform()
	if !probe.Available {
		return demoResult{Probe: probe}, fmt.Errorf("Landlock unavailable: %s", probe.Reason)
	}
	if !probe.WriteConfinement {
		return demoResult{Probe: probe}, fmt.Errorf("Landlock ABI %d is too old for write+truncate confinement; need ABI >= %d", probe.ABI, minimumWriteConfinementABI)
	}

	root, err := os.MkdirTemp("", "agent-sandbox-landlock-*")
	if err != nil {
		return demoResult{Probe: probe}, fmt.Errorf("create experiment root: %w", err)
	}
	defer os.RemoveAll(root)

	allowed := filepath.Join(root, "allowed")
	denied := filepath.Join(root, "denied")
	if err := os.MkdirAll(allowed, 0o700); err != nil {
		return demoResult{Probe: probe}, fmt.Errorf("create allowed directory: %w", err)
	}
	if err := os.MkdirAll(denied, 0o700); err != nil {
		return demoResult{Probe: probe}, fmt.Errorf("create denied directory: %w", err)
	}
	if err := os.WriteFile(filepath.Join(denied, "seed.txt"), []byte("seed"), 0o600); err != nil {
		return demoResult{Probe: probe}, fmt.Errorf("seed denied directory: %w", err)
	}

	cmd := exec.Command(os.Args[0], "__child")
	cmd.Env = append(os.Environ(),
		childAllowedPathEnv+"="+allowed,
		childDeniedPathEnv+"="+denied,
	)
	output, err := cmd.Output()
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return demoResult{Probe: probe}, fmt.Errorf("Landlock child failed: %w: %s", err, exitErr.Stderr)
		}
		return demoResult{Probe: probe}, fmt.Errorf("start Landlock child: %w", err)
	}

	var result demoResult
	if err := json.Unmarshal(output, &result); err != nil {
		return demoResult{Probe: probe}, fmt.Errorf("decode Landlock child result: %w", err)
	}
	return result, nil
}

func runChildPlatform() (demoResult, error) {
	allowed := os.Getenv(childAllowedPathEnv)
	denied := os.Getenv(childDeniedPathEnv)
	if allowed == "" || denied == "" {
		return demoResult{}, errors.New("Landlock child paths are not configured")
	}
	return runConfinementCheck(allowed, denied)
}

func runConfinementCheck(allowed, denied string) (demoResult, error) {
	probe := probePlatform()
	result := demoResult{Probe: probe}
	if !probe.Available {
		return result, fmt.Errorf("Landlock unavailable: %s", probe.Reason)
	}
	if !probe.WriteConfinement {
		return result, fmt.Errorf("Landlock ABI %d is too old for the experiment", probe.ABI)
	}

	enforcement, err := restrictWrites(allowed, probe.ABI)
	if err != nil {
		return result, err
	}
	result.Enforcement = enforcement

	if err := os.WriteFile(filepath.Join(allowed, "created.txt"), []byte("allowed"), 0o600); err != nil {
		return result, fmt.Errorf("allowed write failed: %w", err)
	}
	result.AllowedWriteSucceeded = true

	err = os.WriteFile(filepath.Join(denied, "created.txt"), []byte("blocked"), 0o600)
	result.DeniedCreateBlocked = errors.Is(err, os.ErrPermission)
	if !result.DeniedCreateBlocked {
		return result, fmt.Errorf("denied-path file creation was not blocked: %v", err)
	}

	err = os.WriteFile(filepath.Join(denied, "seed.txt"), []byte("changed"), 0o600)
	result.DeniedTruncateBlocked = errors.Is(err, os.ErrPermission)
	if !result.DeniedTruncateBlocked {
		return result, fmt.Errorf("denied-path truncate was not blocked: %v", err)
	}

	content, err := os.ReadFile(filepath.Join(denied, "seed.txt"))
	result.DeniedPathReadSucceeded = err == nil && string(content) == "seed"
	if !result.DeniedPathReadSucceeded {
		return result, fmt.Errorf("denied-path read unexpectedly failed: %w", err)
	}

	return result, nil
}

func restrictWrites(allowedPath string, abi int) (string, error) {
	if abi < minimumWriteConfinementABI {
		return "", fmt.Errorf("Landlock ABI %d does not support truncate confinement", abi)
	}

	access := writeAccessMask(abi)
	attr := rulesetAttr{HandledAccessFS: access}
	rulesetFD, err := landlockCreateRuleset(&attr)
	if err != nil {
		return "", fmt.Errorf("create Landlock ruleset: %w", err)
	}
	defer unix.Close(rulesetFD)

	pathFD, err := unix.Open(allowedPath, unix.O_PATH|unix.O_CLOEXEC, 0)
	if err != nil {
		return "", fmt.Errorf("open allowed path: %w", err)
	}
	defer unix.Close(pathFD)

	rule := pathBeneathAttr{
		AllowedAccess: access,
		ParentFD:      int32(pathFD),
	}
	if err := landlockAddPathRule(rulesetFD, &rule); err != nil {
		return "", fmt.Errorf("add Landlock path rule: %w", err)
	}

	lockedThread := abi < 8
	if lockedThread {
		runtime.LockOSThread()
	}
	succeeded := false
	defer func() {
		if lockedThread && !succeeded {
			runtime.UnlockOSThread()
		}
	}()

	if err := unix.Prctl(unix.PR_SET_NO_NEW_PRIVS, 1, 0, 0, 0); err != nil {
		return "", fmt.Errorf("set no_new_privs: %w", err)
	}

	flags := uint32(0)
	enforcement := "thread-locked"
	if abi >= 8 {
		flags = uint32(unix.LANDLOCK_RESTRICT_SELF_TSYNC)
		enforcement = "process-tsync"
	}
	if err := landlockRestrictSelf(rulesetFD, flags); err != nil {
		return "", fmt.Errorf("enforce Landlock ruleset: %w", err)
	}

	succeeded = true
	return enforcement, nil
}

func writeAccessMask(abi int) uint64 {
	access := uint64(
		unix.LANDLOCK_ACCESS_FS_WRITE_FILE |
			unix.LANDLOCK_ACCESS_FS_REMOVE_DIR |
			unix.LANDLOCK_ACCESS_FS_REMOVE_FILE |
			unix.LANDLOCK_ACCESS_FS_MAKE_CHAR |
			unix.LANDLOCK_ACCESS_FS_MAKE_DIR |
			unix.LANDLOCK_ACCESS_FS_MAKE_REG |
			unix.LANDLOCK_ACCESS_FS_MAKE_SOCK |
			unix.LANDLOCK_ACCESS_FS_MAKE_FIFO |
			unix.LANDLOCK_ACCESS_FS_MAKE_BLOCK |
			unix.LANDLOCK_ACCESS_FS_MAKE_SYM,
	)
	if abi >= 2 {
		access |= uint64(unix.LANDLOCK_ACCESS_FS_REFER)
	}
	if abi >= 3 {
		access |= uint64(unix.LANDLOCK_ACCESS_FS_TRUNCATE)
	}
	return access
}

func landlockABI() (int, error) {
	value, _, errno := unix.Syscall(
		unix.SYS_LANDLOCK_CREATE_RULESET,
		0,
		0,
		uintptr(unix.LANDLOCK_CREATE_RULESET_VERSION),
	)
	if errno != 0 {
		return 0, errno
	}
	return int(value), nil
}

func landlockCreateRuleset(attr *rulesetAttr) (int, error) {
	value, _, errno := unix.Syscall(
		unix.SYS_LANDLOCK_CREATE_RULESET,
		uintptr(unsafe.Pointer(attr)),
		unsafe.Sizeof(*attr),
		0,
	)
	runtime.KeepAlive(attr)
	if errno != 0 {
		return -1, errno
	}
	return int(value), nil
}

func landlockAddPathRule(rulesetFD int, attr *pathBeneathAttr) error {
	_, _, errno := unix.Syscall6(
		unix.SYS_LANDLOCK_ADD_RULE,
		uintptr(rulesetFD),
		uintptr(unix.LANDLOCK_RULE_PATH_BENEATH),
		uintptr(unsafe.Pointer(attr)),
		0,
		0,
		0,
	)
	runtime.KeepAlive(attr)
	if errno != 0 {
		return errno
	}
	return nil
}

func landlockRestrictSelf(rulesetFD int, flags uint32) error {
	_, _, errno := unix.Syscall(
		unix.SYS_LANDLOCK_RESTRICT_SELF,
		uintptr(rulesetFD),
		uintptr(flags),
		0,
	)
	if errno != 0 {
		return errno
	}
	return nil
}
