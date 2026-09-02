package docker

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	sandbox "github.com/luojiyin1987/Agent-Sandbox-Runtime"
)

const (
	defaultTimeout         = 30 * time.Second
	defaultMemoryBytes     = int64(256 << 20)
	defaultProcesses       = 64
	defaultMilliCPUs       = int64(1000)
	defaultMaxOutputBytes  = int64(1 << 20)
	defaultTempDirBytes    = int64(64 << 20)
	minimumMemoryBytes     = int64(6 << 20)
	cleanupTimeout         = 5 * time.Second
	containerWorkspace     = "/workspace"
	containerTempDir       = "/tmp"
	executionResourceLabel = "agent-sandbox-runtime=execution"
)

var (
	// ErrUnsupportedPolicy is returned when the Docker backend cannot yet enforce
	// a requested policy. Unsupported policy is rejected rather than silently
	// downgraded.
	ErrUnsupportedPolicy = errors.New("docker backend does not support requested policy")
	// ErrCleanup marks a lifecycle failure where the runtime could not prove that
	// an execution-owned Docker resource was removed before Execute returned.
	ErrCleanup = errors.New("docker sandbox cleanup failed")
)

type commandRunner func(ctx context.Context, stdout, stderr io.Writer, args ...string) error
type cleanupFunc func() error

// Option configures trusted Docker backend state.
type Option func(*Backend) error

// WithWorkspaceRoot configures the trusted host directory from which requests
// may select workspace subdirectories. ExecRequest.Filesystem.WorkspacePath is
// resolved relative to this root and is always mounted at /workspace.
//
// Workspace mounts require the Docker daemon to run on the same host as this
// runtime process because path validation is performed against the local host
// filesystem before Docker receives the bind mount.
func WithWorkspaceRoot(root string) Option {
	return func(b *Backend) error {
		root = strings.TrimSpace(root)
		if root == "" {
			return fmt.Errorf("%w: workspace root is required", sandbox.ErrInvalidRequest)
		}

		absolute, err := filepath.Abs(root)
		if err != nil {
			return fmt.Errorf("%w: resolve workspace root: %v", sandbox.ErrInvalidRequest, err)
		}
		resolved, err := filepath.EvalSymlinks(absolute)
		if err != nil {
			return fmt.Errorf("%w: resolve workspace root: %v", sandbox.ErrInvalidRequest, err)
		}
		info, err := os.Stat(resolved)
		if err != nil {
			return fmt.Errorf("%w: stat workspace root: %v", sandbox.ErrInvalidRequest, err)
		}
		if !info.IsDir() {
			return fmt.Errorf("%w: workspace root must be a directory", sandbox.ErrInvalidRequest)
		}
		if strings.ContainsRune(resolved, ',') {
			return fmt.Errorf("%w: workspace root cannot contain a comma", sandbox.ErrInvalidRequest)
		}

		b.workspaceRoot = filepath.Clean(resolved)
		return nil
	}
}

// Backend executes sandbox requests in Docker containers.
//
// Resource limits, workspace mounts, a bounded /tmp tmpfs, network mode, and
// timeout are compiled per request. The container root remains read-only and
// every workload runs as the runtime process's effective UID/GID with all Linux
// capabilities dropped, no-new-privileges, and Docker's built-in seccomp
// profile.
type Backend struct {
	image             string
	workspaceRoot     string
	allowOutbound     bool
	allowMutableImage bool
	run               commandRunner
	maxConcurrent     int
	aggregateLimits   sandbox.ResourceLimits
	admissionOnce     sync.Once
	admission         *admissionPool
	logger            *slog.Logger
	cleanupFailures   atomic.Uint64
	admissionRejected atomic.Uint64
}

// Stats contains cumulative backend event counters.
type Stats struct {
	CleanupFailures   uint64
	AdmissionRejected uint64
}

var _ sandbox.Runtime = (*Backend)(nil)

// New creates a Docker backend pinned to a trusted container image. Image
// references must use an immutable sha256 digest unless a trusted operator
// explicitly opts into mutable references with WithMutableImageReference.
func New(image string, options ...Option) (*Backend, error) {
	image = strings.TrimSpace(image)
	if image == "" {
		return nil, fmt.Errorf("%w: docker image is required", sandbox.ErrInvalidRequest)
	}

	backend := &Backend{
		image:         image,
		run:           runDocker,
		maxConcurrent: defaultMaxConcurrentSandboxes,
	}
	for _, option := range options {
		if option == nil {
			continue
		}
		if err := option(backend); err != nil {
			return nil, err
		}
	}
	if err := validateImageReference(backend.image, backend.allowMutableImage); err != nil {
		return nil, err
	}
	return backend, nil
}

// Execute runs one request in a fresh Docker container and removes all
// execution-owned Docker resources before returning. A non-zero workload exit
// code is represented in ExecResult and is not itself a runtime error. Cleanup
// failures are returned because the runtime cannot otherwise prove terminal
// containment.
func (b *Backend) Execute(ctx context.Context, req sandbox.ExecRequest) (result sandbox.ExecResult, retErr error) {
	startedAt := time.Now()
	result = sandbox.ExecResult{
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
	if err := b.validateSupportedPolicy(req); err != nil {
		finish()
		return result, err
	}
	if err := validateResourceLimits(req.Resources); err != nil {
		finish()
		return result, err
	}

	workspacePath, err := b.resolveWorkspace(req.Filesystem)
	if err != nil {
		finish()
		return result, err
	}

	limits := effectiveResourceLimits(req.Resources)
	admission := b.admissionController()
	if !admission.tryAcquire(limits) {
		b.recordAdmissionRejection(limits)
		finish()
		return result, sandbox.ErrTooManyConcurrent
	}
	defer admission.release(limits)

	timeout := req.Timeout
	if timeout == 0 {
		timeout = defaultTimeout
	}
	execCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	dockerNetwork, cleanupNetwork, err := b.prepareNetwork(execCtx, req.EffectiveNetworkMode())
	if err != nil {
		finish()
		if contextErr := execCtx.Err(); contextErr != nil {
			return resultForContext(result, contextErr), errors.Join(contextErr, err)
		}
		return result, err
	}
	// Register network cleanup before container cleanup so defer LIFO ordering
	// always removes the container before removing its execution network.
	defer func() {
		applyCleanup(&result, &retErr, cleanupNetwork)
	}()

	name, err := containerName()
	if err != nil {
		finish()
		return result, fmt.Errorf("generate container name: %w", err)
	}
	// Register cleanup before create. A client-side create error can be an
	// unknown result: the daemon may have created the named container even if
	// the response never reached us. Removing by the already-known random name
	// makes that path fail closed.
	defer func() {
		applyCleanup(&result, &retErr, func() error { return b.removeContainer(name) })
	}()

	envFile, removeEnvFile, err := writeEnvironmentFile(req.Env)
	if err != nil {
		finish()
		return result, err
	}
	defer removeEnvFile()

	var createOut bytes.Buffer
	var createErr bytes.Buffer
	createRunErr := b.run(execCtx, &createOut, &createErr, createArgs(name, b.image, req, workspacePath, envFile, dockerNetwork)...)
	removeEnvFile()
	if createRunErr != nil {
		finish()
		if contextErr := execCtx.Err(); contextErr != nil {
			return resultForContext(result, contextErr), contextErr
		}
		return result, fmt.Errorf("docker create failed: %w", createRunErr)
	}

	containerID := strings.TrimSpace(createOut.String())
	if containerID == "" {
		finish()
		return result, errors.New("docker create returned an empty container ID")
	}

	capture := newOutputCapture(limits.MaxOutputBytes)
	startErr := b.run(execCtx, capture.stdoutWriter(), capture.stderrWriter(), "start", "--attach", name)
	result.Stdout, result.Stderr, result.OutputTruncated = capture.snapshot()
	finish()

	if contextErr := execCtx.Err(); contextErr != nil {
		return resultForContext(result, contextErr), contextErr
	}

	state, inspectErr := b.inspectState(name)
	if inspectErr != nil {
		if startErr != nil {
			return result, fmt.Errorf("docker start failed: %w", startErr)
		}
		return result, inspectErr
	}

	result.ExitCode = state.exitCode
	if state.oomKilled {
		result.Termination = sandbox.TerminationResourceLimit
	} else {
		result.Termination = sandbox.TerminationCompleted
	}
	if startErr != nil && state.exitCode == 0 {
		result.Termination = sandbox.TerminationRuntimeError
		return result, fmt.Errorf("docker start failed: %w", startErr)
	}

	return result, nil
}

func (b *Backend) admissionController() *admissionPool {
	b.admissionOnce.Do(func() {
		b.admission = newAdmissionPool(b.maxConcurrent, b.aggregateLimits)
	})
	return b.admission
}

// WithLogger enables structured backend event logs.
func WithLogger(logger *slog.Logger) Option {
	return func(b *Backend) error {
		if logger == nil {
			return fmt.Errorf("%w: logger is required", sandbox.ErrInvalidRequest)
		}
		b.logger = logger
		return nil
	}
}

// Stats returns a snapshot of cumulative backend counters.
func (b *Backend) Stats() Stats {
	return Stats{
		CleanupFailures:   b.cleanupFailures.Load(),
		AdmissionRejected: b.admissionRejected.Load(),
	}
}

func (b *Backend) recordAdmissionRejection(limits sandbox.ResourceLimits) {
	b.admissionRejected.Add(1)
	if b.logger != nil {
		b.logger.Warn(
			"Docker sandbox admission rejected",
			"memory_bytes", limits.MaxMemoryBytes,
			"processes", limits.MaxProcesses,
			"milli_cpus", limits.MilliCPUs,
		)
	}
}

func applyCleanup(result *sandbox.ExecResult, retErr *error, cleanup cleanupFunc) {
	if cleanup == nil {
		return
	}
	if err := cleanup(); err != nil {
		*retErr = errors.Join(*retErr, err)
		if result.Termination == sandbox.TerminationCompleted {
			result.Termination = sandbox.TerminationRuntimeError
		}
	}
}

func (b *Backend) validateSupportedPolicy(req sandbox.ExecRequest) error {
	switch req.EffectiveNetworkMode() {
	case sandbox.NetworkNone:
	case sandbox.NetworkOutbound:
		if !b.allowOutbound {
			return fmt.Errorf("%w: outbound networking is not enabled for this backend", ErrUnsupportedPolicy)
		}
	case sandbox.NetworkAllowlist:
		return fmt.Errorf("%w: network allowlist requires a destination-filtering backend", ErrUnsupportedPolicy)
	default:
		return fmt.Errorf("%w: network mode %q", ErrUnsupportedPolicy, req.EffectiveNetworkMode())
	}
	if req.EffectiveRootFilesystemMode() != sandbox.RootReadOnly {
		return fmt.Errorf("%w: root filesystem mode %q", ErrUnsupportedPolicy, req.EffectiveRootFilesystemMode())
	}
	return nil
}

func validateResourceLimits(limits sandbox.ResourceLimits) error {
	if limits.MaxMemoryBytes > 0 && limits.MaxMemoryBytes < minimumMemoryBytes {
		return fmt.Errorf("%w: max memory must be at least %d bytes for Docker", sandbox.ErrInvalidRequest, minimumMemoryBytes)
	}
	maxMilliCPUs := int64(runtime.NumCPU()) * 1000
	if limits.MilliCPUs > maxMilliCPUs {
		return fmt.Errorf("%w: milli-CPUs must not exceed node capacity %d", sandbox.ErrInvalidRequest, maxMilliCPUs)
	}
	return nil
}

func effectiveResourceLimits(limits sandbox.ResourceLimits) sandbox.ResourceLimits {
	if limits.MaxMemoryBytes == 0 {
		limits.MaxMemoryBytes = defaultMemoryBytes
	}
	if limits.MaxProcesses == 0 {
		limits.MaxProcesses = defaultProcesses
	}
	if limits.MaxOutputBytes == 0 {
		limits.MaxOutputBytes = defaultMaxOutputBytes
	}
	if limits.MilliCPUs == 0 {
		limits.MilliCPUs = defaultMilliCPUs
	}
	return limits
}

func validateEnvironment(env map[string]string) error {
	for key, value := range env {
		if key == "" || strings.ContainsAny(key, "=\x00\r\n") {
			return fmt.Errorf("%w: invalid environment variable name %q", sandbox.ErrInvalidRequest, key)
		}
		if strings.ContainsAny(value, "\x00\r\n") {
			return fmt.Errorf("%w: environment variable %q contains a control delimiter", sandbox.ErrInvalidRequest, key)
		}
	}
	return nil
}

func writeEnvironmentFile(env map[string]string) (string, func(), error) {
	if len(env) == 0 {
		return "", func() {}, nil
	}

	file, err := os.CreateTemp("", "agent-sandbox-env-*")
	if err != nil {
		return "", func() {}, fmt.Errorf("create Docker environment file: %w", err)
	}
	path := file.Name()
	var cleanupOnce sync.Once
	cleanup := func() {
		cleanupOnce.Do(func() {
			_ = os.Remove(path)
		})
	}

	keys := make([]string, 0, len(env))
	for key := range env {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		if _, err := fmt.Fprintf(file, "%s=%s\n", key, env[key]); err != nil {
			_ = file.Close()
			cleanup()
			return "", func() {}, fmt.Errorf("write Docker environment file: %w", err)
		}
	}
	if err := file.Close(); err != nil {
		cleanup()
		return "", func() {}, fmt.Errorf("close Docker environment file: %w", err)
	}
	return path, cleanup, nil
}

func (b *Backend) resolveWorkspace(policy sandbox.FilesystemPolicy) (string, error) {
	if policy.WorkspacePath == "" {
		return "", nil
	}
	if b.workspaceRoot == "" {
		return "", fmt.Errorf("%w: workspace root is not configured", ErrUnsupportedPolicy)
	}
	if strings.ContainsRune(policy.WorkspacePath, '\x00') || filepath.IsAbs(policy.WorkspacePath) {
		return "", fmt.Errorf("%w: workspace path must be relative to the configured workspace root", sandbox.ErrInvalidRequest)
	}

	clean := filepath.Clean(policy.WorkspacePath)
	if clean == ".." || strings.HasPrefix(clean, ".."+string(os.PathSeparator)) {
		return "", fmt.Errorf("%w: workspace path escapes the configured workspace root", sandbox.ErrInvalidRequest)
	}

	candidate := filepath.Join(b.workspaceRoot, clean)
	resolved, err := filepath.EvalSymlinks(candidate)
	if err != nil {
		return "", fmt.Errorf("%w: resolve workspace path: %v", sandbox.ErrInvalidRequest, err)
	}
	relative, err := filepath.Rel(b.workspaceRoot, resolved)
	if err != nil {
		return "", fmt.Errorf("%w: compare workspace path: %v", sandbox.ErrInvalidRequest, err)
	}
	if relative == ".." || strings.HasPrefix(relative, ".."+string(os.PathSeparator)) {
		return "", fmt.Errorf("%w: workspace symlink escapes the configured workspace root", sandbox.ErrInvalidRequest)
	}

	info, err := os.Stat(resolved)
	if err != nil {
		return "", fmt.Errorf("%w: stat workspace path: %v", sandbox.ErrInvalidRequest, err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("%w: workspace path must resolve to a directory", sandbox.ErrInvalidRequest)
	}
	if strings.ContainsRune(resolved, ',') {
		return "", fmt.Errorf("%w: resolved workspace path cannot contain a comma", sandbox.ErrInvalidRequest)
	}
	return filepath.Clean(resolved), nil
}

func createArgs(name, image string, req sandbox.ExecRequest, workspacePath, envFile, dockerNetwork string) []string {
	limits := effectiveResourceLimits(req.Resources)
	memoryLimit := strconv.FormatInt(limits.MaxMemoryBytes, 10)
	args := []string{
		"create",
		"--name", name,
		"--label", executionResourceLabel,
		"--network", dockerNetwork,
		"--log-driver", "none",
		"--read-only",
		"--user", fmt.Sprintf("%d:%d", os.Geteuid(), os.Getegid()),
		"--cap-drop", "ALL",
		"--security-opt", "no-new-privileges=true",
		"--security-opt", "seccomp=builtin",
		"--memory", memoryLimit,
		"--memory-swap", memoryLimit,
		"--pids-limit", strconv.Itoa(limits.MaxProcesses),
		"--cpus", fmt.Sprintf("%.3f", float64(limits.MilliCPUs)/1000),
	}

	if workspacePath != "" {
		mount := "type=bind,src=" + workspacePath + ",dst=" + containerWorkspace
		if req.Filesystem.WorkspaceReadOnly {
			mount += ",readonly,bind-propagation=rprivate,bind-recursive=readonly"
		}
		args = append(args, "--mount", mount)
	}
	if req.Filesystem.TempDir {
		args = append(args, "--mount", fmt.Sprintf(
			"type=tmpfs,dst=%s,tmpfs-size=%d,tmpfs-mode=1777",
			containerTempDir,
			defaultTempDirBytes,
		))
	}
	if req.WorkDir != "" {
		args = append(args, "--workdir", req.WorkDir)
	}
	if envFile != "" {
		args = append(args, "--env-file", envFile)
	}

	args = append(args, image, req.Command)
	args = append(args, req.Args...)
	return args
}

func runDocker(ctx context.Context, stdout, stderr io.Writer, args ...string) error {
	cmd := exec.CommandContext(ctx, "docker", args...)
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	return cmd.Run()
}

type containerState struct {
	exitCode  int
	oomKilled bool
}

func (b *Backend) inspectState(name string) (containerState, error) {
	ctx, cancel := context.WithTimeout(context.Background(), cleanupTimeout)
	defer cancel()

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	if err := b.run(ctx, &stdout, &stderr, "inspect", "--format", "{{.State.ExitCode}} {{.State.OOMKilled}}", name); err != nil {
		return containerState{}, fmt.Errorf("docker inspect failed: %w", err)
	}

	fields := strings.Fields(stdout.String())
	if len(fields) < 2 {
		return containerState{}, fmt.Errorf("parse docker state: expected exit code and OOM flag, got %q", strings.TrimSpace(stdout.String()))
	}
	exitCode, err := strconv.Atoi(fields[0])
	if err != nil {
		return containerState{}, fmt.Errorf("parse docker exit code: %w", err)
	}
	oomKilled, err := strconv.ParseBool(fields[1])
	if err != nil {
		return containerState{}, fmt.Errorf("parse docker OOM flag: %w", err)
	}
	return containerState{exitCode: exitCode, oomKilled: oomKilled}, nil
}

func (b *Backend) removeContainer(name string) error {
	return b.removeDockerResource("container", name, "rm", "--force", name)
}

func (b *Backend) removeDockerResource(resourceType, name string, args ...string) error {
	startedAt := time.Now()
	ctx, cancel := context.WithTimeout(context.Background(), cleanupTimeout)
	defer cancel()

	var stderr bytes.Buffer
	if err := b.run(ctx, io.Discard, &stderr, args...); err != nil {
		if dockerResourceMissing(stderr.String()) {
			return nil
		}
		cleanupErr := fmt.Errorf("%w: remove %s %q: %v%s", ErrCleanup, resourceType, name, err, dockerErrorSuffix(stderr.String()))
		b.cleanupFailures.Add(1)
		if b.logger != nil {
			b.logger.Error(
				"Docker resource cleanup failed",
				"resource_type", resourceType,
				"resource_name", name,
				"duration", time.Since(startedAt),
				"error", cleanupErr,
			)
		}
		return cleanupErr
	}
	return nil
}

func dockerResourceMissing(stderr string) bool {
	message := strings.ToLower(stderr)
	if strings.Contains(message, "no such container") || strings.Contains(message, "no such network") {
		return true
	}
	return strings.Contains(message, "network ") && strings.Contains(message, " not found")
}

func dockerErrorSuffix(stderr string) string {
	message := strings.TrimSpace(stderr)
	if message == "" {
		return ""
	}
	return ": " + message
}

func containerName() (string, error) {
	return generateRandomName("agent-sandbox-")
}

func generateRandomName(prefix string) (string, error) {
	var random [8]byte
	if _, err := rand.Read(random[:]); err != nil {
		return "", err
	}
	return prefix + hex.EncodeToString(random[:]), nil
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
		written := int64(len(p))
		if written >= w.capture.remaining {
			w.capture.remaining = 0
		} else {
			w.capture.remaining -= written
		}
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
