package docker

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	sandbox "github.com/luojiyin1987/Agent-Sandbox-Runtime"
)

const (
	defaultTimeout        = 30 * time.Second
	defaultMemoryBytes    = int64(256 << 20)
	defaultProcesses      = 64
	defaultMilliCPUs      = int64(1000)
	defaultMaxOutputBytes = int64(1 << 20)
	cleanupTimeout        = 5 * time.Second
)

// ErrUnsupportedPolicy is returned when PR2's baseline Docker backend cannot
// yet enforce a requested policy. Unsupported policy is rejected rather than
// silently downgraded.
var ErrUnsupportedPolicy = errors.New("docker backend does not support requested policy")

type commandRunner func(ctx context.Context, env []string, stdout, stderr io.Writer, args ...string) error

// Backend executes sandbox requests in Docker containers.
//
// The PR2 backend intentionally exposes only the fail-closed baseline policy.
// Custom resource limits, writable filesystems, workspace mounts, temporary
// filesystems, and network access are rejected until their policy compilers are
// implemented in later changes.
type Backend struct {
	image string
	run   commandRunner
}

var _ sandbox.Runtime = (*Backend)(nil)

// New creates a Docker backend pinned to a trusted container image.
func New(image string) (*Backend, error) {
	if strings.TrimSpace(image) == "" {
		return nil, fmt.Errorf("%w: docker image is required", sandbox.ErrInvalidRequest)
	}

	return &Backend{
		image: strings.TrimSpace(image),
		run:   runDocker,
	}, nil
}

// Execute runs one request in a fresh Docker container and removes the
// container before returning. A non-zero workload exit code is represented in
// ExecResult and is not itself a runtime error.
func (b *Backend) Execute(ctx context.Context, req sandbox.ExecRequest) (sandbox.ExecResult, error) {
	startedAt := time.Now()
	result := sandbox.ExecResult{
		ExitCode:    -1,
		StartedAt:   startedAt,
		Termination: sandbox.TerminationRuntimeError,
	}

	finish := func() {
		result.Duration = time.Since(startedAt)
	}

	if err := req.Validate(); err != nil {
		finish()
		return result, err
	}
	if err := validateEnvironment(req.Env); err != nil {
		finish()
		return result, err
	}
	if err := validateSupportedPolicy(req); err != nil {
		finish()
		return result, err
	}

	timeout := req.Timeout
	if timeout == 0 {
		timeout = defaultTimeout
	}
	execCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	name, err := containerName()
	if err != nil {
		finish()
		return result, fmt.Errorf("generate container name: %w", err)
	}
	// Register cleanup before create. A client-side create error can be an
	// unknown result: the daemon may have created the named container even if
	// the response never reached us. Removing by the already-known random name
	// makes that path fail closed.
	defer b.removeContainer(name)

	var createOut bytes.Buffer
	var createErr bytes.Buffer
	if err := b.run(execCtx, dockerEnvironment(req.Env), &createOut, &createErr, createArgs(name, b.image, req)...); err != nil {
		finish()
		if contextErr := execCtx.Err(); contextErr != nil {
			return resultForContext(result, contextErr), contextErr
		}
		return result, fmt.Errorf("docker create failed: %w", err)
	}

	containerID := strings.TrimSpace(createOut.String())
	if containerID == "" {
		finish()
		return result, errors.New("docker create returned an empty container ID")
	}

	capture := newOutputCapture(defaultMaxOutputBytes)
	startErr := b.run(execCtx, nil, capture.stdoutWriter(), capture.stderrWriter(), "start", "--attach", name)
	result.Stdout, result.Stderr, result.OutputTruncated = capture.snapshot()
	finish()

	if contextErr := execCtx.Err(); contextErr != nil {
		return resultForContext(result, contextErr), contextErr
	}

	exitCode, inspectErr := b.inspectExitCode(name)
	if inspectErr != nil {
		if startErr != nil {
			return result, fmt.Errorf("docker start failed: %w", startErr)
		}
		return result, inspectErr
	}

	result.ExitCode = exitCode
	result.Termination = sandbox.TerminationCompleted
	if startErr != nil && exitCode == 0 {
		result.Termination = sandbox.TerminationRuntimeError
		return result, fmt.Errorf("docker start failed: %w", startErr)
	}

	return result, nil
}

func validateSupportedPolicy(req sandbox.ExecRequest) error {
	if req.EffectiveNetworkMode() != sandbox.NetworkNone {
		return fmt.Errorf("%w: network mode %q", ErrUnsupportedPolicy, req.EffectiveNetworkMode())
	}
	if req.EffectiveRootFilesystemMode() != sandbox.RootReadOnly {
		return fmt.Errorf("%w: root filesystem mode %q", ErrUnsupportedPolicy, req.EffectiveRootFilesystemMode())
	}
	if req.Filesystem.WorkspacePath != "" || req.Filesystem.WorkspaceReadOnly || req.Filesystem.TempDir {
		return fmt.Errorf("%w: custom filesystem policy", ErrUnsupportedPolicy)
	}
	if req.Resources != (sandbox.ResourceLimits{}) {
		return fmt.Errorf("%w: custom resource limits", ErrUnsupportedPolicy)
	}
	return nil
}

func validateEnvironment(env map[string]string) error {
	for key, value := range env {
		if key == "" || strings.ContainsAny(key, "=\x00") {
			return fmt.Errorf("%w: invalid environment variable name %q", sandbox.ErrInvalidRequest, key)
		}
		if strings.ContainsRune(value, '\x00') {
			return fmt.Errorf("%w: environment variable %q contains NUL", sandbox.ErrInvalidRequest, key)
		}
	}
	return nil
}

func createArgs(name, image string, req sandbox.ExecRequest) []string {
	args := []string{
		"create",
		"--name", name,
		"--network", "none",
		"--read-only",
		"--memory", strconv.FormatInt(defaultMemoryBytes, 10),
		"--pids-limit", strconv.Itoa(defaultProcesses),
		"--cpus", fmt.Sprintf("%.3f", float64(defaultMilliCPUs)/1000),
	}

	if req.WorkDir != "" {
		args = append(args, "--workdir", req.WorkDir)
	}

	keys := make([]string, 0, len(req.Env))
	for key := range req.Env {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		args = append(args, "--env", key)
	}

	args = append(args, image, req.Command)
	args = append(args, req.Args...)
	return args
}

func dockerEnvironment(env map[string]string) []string {
	if len(env) == 0 {
		return nil
	}

	keys := make([]string, 0, len(env))
	for key := range env {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	values := append([]string(nil), os.Environ()...)
	for _, key := range keys {
		values = append(values, key+"="+env[key])
	}
	return values
}

func runDocker(ctx context.Context, env []string, stdout, stderr io.Writer, args ...string) error {
	cmd := exec.CommandContext(ctx, "docker", args...)
	if env != nil {
		cmd.Env = env
	}
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	return cmd.Run()
}

func (b *Backend) inspectExitCode(name string) (int, error) {
	ctx, cancel := context.WithTimeout(context.Background(), cleanupTimeout)
	defer cancel()

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	if err := b.run(ctx, nil, &stdout, &stderr, "inspect", "--format", "{{.State.ExitCode}}", name); err != nil {
		return 0, fmt.Errorf("docker inspect failed: %w", err)
	}

	exitCode, err := strconv.Atoi(strings.TrimSpace(stdout.String()))
	if err != nil {
		return 0, fmt.Errorf("parse docker exit code: %w", err)
	}
	return exitCode, nil
}

func (b *Backend) removeContainer(name string) {
	ctx, cancel := context.WithTimeout(context.Background(), cleanupTimeout)
	defer cancel()
	_ = b.run(ctx, nil, io.Discard, io.Discard, "rm", "--force", name)
}

func containerName() (string, error) {
	var random [8]byte
	if _, err := rand.Read(random[:]); err != nil {
		return "", err
	}
	return "agent-sandbox-" + hex.EncodeToString(random[:]), nil
}

func resultForContext(result sandbox.ExecResult, err error) sandbox.ExecResult {
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		result.Termination = sandbox.TerminationTimeout
	case errors.Is(err, context.Canceled):
		result.Termination = sandbox.TerminationCancelled
	default:
		result.Termination = sandbox.TerminationRuntimeError
	}
	return result
}

type outputCapture struct {
	mu        sync.Mutex
	remaining int64
	stdout    bytes.Buffer
	stderr    bytes.Buffer
	truncated bool
}

type captureWriter struct {
	capture *outputCapture
	stderr  bool
}

func newOutputCapture(limit int64) *outputCapture {
	return &outputCapture{remaining: limit}
}

func (c *outputCapture) stdoutWriter() io.Writer {
	return captureWriter{capture: c}
}

func (c *outputCapture) stderrWriter() io.Writer {
	return captureWriter{capture: c, stderr: true}
}

func (w captureWriter) Write(p []byte) (int, error) {
	w.capture.mu.Lock()
	defer w.capture.mu.Unlock()

	originalLen := len(p)
	if int64(len(p)) > w.capture.remaining {
		allowed := w.capture.remaining
		if allowed < 0 {
			allowed = 0
		}
		p = p[:int(allowed)]
		w.capture.truncated = true
	}

	if len(p) != 0 {
		if w.stderr {
			_, _ = w.capture.stderr.Write(p)
		} else {
			_, _ = w.capture.stdout.Write(p)
		}
		w.capture.remaining -= int64(len(p))
	}
	if len(p) < originalLen {
		w.capture.truncated = true
	}
	return originalLen, nil
}

func (c *outputCapture) snapshot() (stdout, stderr []byte, truncated bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return bytes.Clone(c.stdout.Bytes()), bytes.Clone(c.stderr.Bytes()), c.truncated
}
