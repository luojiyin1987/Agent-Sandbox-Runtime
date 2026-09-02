package sandbox

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Runtime executes an untrusted workload under a sandbox policy.
// Implementations must preserve the contract documented by ExecRequest and ExecResult.
type Runtime interface {
	Execute(ctx context.Context, req ExecRequest) (ExecResult, error)
}

// ExecRequest describes one isolated execution.
type ExecRequest struct {
	Command string
	Args    []string
	Env     map[string]string
	WorkDir string

	Timeout    time.Duration
	Resources  ResourceLimits
	Network    NetworkPolicy
	Filesystem FilesystemPolicy
}

// ResourceLimits are backend-neutral upper bounds. A zero value means the
// backend's safe default; it must never mean "unlimited".
type ResourceLimits struct {
	// MaxMemoryBytes is the maximum resident set size in bytes.
	// Zero uses the backend default (256 MiB for Docker).
	MaxMemoryBytes int64
	// MaxProcesses is the maximum number of processes (PIDs) in the container.
	// Zero uses the backend default (64 for Docker).
	MaxProcesses int
	// MaxOutputBytes caps the combined captured stdout and stderr in bytes.
	// Excess bytes are discarded and OutputTruncated is set. Zero uses 1 MiB.
	MaxOutputBytes int64
	// MilliCPUs is the CPU quota in thousandths of a CPU core (1000 = 1 core).
	// Zero uses the backend default (1000 for Docker).
	MilliCPUs int64
}

// AdmissionLimits are trusted totals across active executions.
// Zero fields use backend defaults and never disable enforcement.
type AdmissionLimits struct {
	MaxConcurrent  int
	MaxMemoryBytes int64
	MaxProcesses   int
	MaxOutputBytes int64
	MilliCPUs      int64
}

// NetworkMode describes network access granted to a workload.
type NetworkMode string

const (
	// NetworkNone is the fail-closed default.
	NetworkNone NetworkMode = "none"
	// NetworkOutbound allows outbound networking subject to backend policy.
	NetworkOutbound NetworkMode = "outbound"
	// NetworkAllowlist allows only destinations listed in NetworkPolicy.Allow.
	NetworkAllowlist NetworkMode = "allowlist"
)

// NetworkPolicy controls network access. An empty Mode is equivalent to NetworkNone.
type NetworkPolicy struct {
	Mode  NetworkMode
	Allow []string
}

// RootFilesystemMode controls mutability of the sandbox root filesystem.
type RootFilesystemMode string

const (
	// RootReadOnly is the fail-closed default.
	RootReadOnly  RootFilesystemMode = "read-only"
	RootReadWrite RootFilesystemMode = "read-write"
)

// FilesystemPolicy controls the sandbox root and optional workspace.
// An empty Root value is equivalent to RootReadOnly. WorkspacePath is a
// backend-relative selector inside a trusted workspace root; it must never be
// interpreted as permission to mount an arbitrary host path.
type FilesystemPolicy struct {
	Root              RootFilesystemMode
	WorkspacePath     string
	WorkspaceReadOnly bool
	TempDir           bool
}

// TerminationReason classifies why an execution stopped without exposing
// backend-specific or sensitive error text.
type TerminationReason string

const (
	TerminationCompleted     TerminationReason = "completed"
	TerminationTimeout       TerminationReason = "timeout"
	TerminationCancelled     TerminationReason = "cancelled"
	TerminationResourceLimit TerminationReason = "resource_limit"
	TerminationRuntimeError  TerminationReason = "runtime_error"
)

// ExecResult contains bounded output and execution metadata.
type ExecResult struct {
	// ExitCode is the workload process exit code. It is -1 before the workload
	// starts; a non-negative value is always a completed workload result, not
	// a runtime error.
	ExitCode int
	// Stdout is the captured standard output, bounded by MaxOutputBytes.
	Stdout []byte
	// Stderr is the captured standard error, bounded by MaxOutputBytes.
	Stderr []byte
	// OutputTruncated is true when stdout or stderr exceeded MaxOutputBytes
	// and excess bytes were discarded.
	OutputTruncated bool
	// StartedAt records when the workload execution began.
	StartedAt time.Time
	// Duration is the wall-clock time from start to finish.
	Duration time.Duration
	// Termination classifies why the execution stopped.
	Termination TerminationReason
}

var (
	ErrInvalidRequest        = errors.New("invalid sandbox request")
	ErrTooManyConcurrent     = errors.New("too many concurrent sandbox executions")
	ErrResourceLimitExceeded = errors.New("sandbox request exceeds trusted resource limits")
)

// Validate rejects ambiguous or unsafe request shapes before a backend sees them.
func (r ExecRequest) Validate() error {
	if r.Command == "" {
		return fmt.Errorf("%w: command is required", ErrInvalidRequest)
	}
	for index, arg := range r.Args {
		if strings.ContainsRune(arg, '\x00') {
			return fmt.Errorf("%w: argument %d contains a NUL byte", ErrInvalidRequest, index)
		}
	}
	if r.Timeout < 0 {
		return fmt.Errorf("%w: timeout must not be negative", ErrInvalidRequest)
	}
	if r.Resources.MaxMemoryBytes < 0 || r.Resources.MaxProcesses < 0 || r.Resources.MaxOutputBytes < 0 || r.Resources.MilliCPUs < 0 {
		return fmt.Errorf("%w: resource limits must not be negative", ErrInvalidRequest)
	}
	if r.Filesystem.WorkspaceReadOnly && r.Filesystem.WorkspacePath == "" {
		return fmt.Errorf("%w: workspace read-only requires a workspace path", ErrInvalidRequest)
	}

	switch r.Network.Mode {
	case "", NetworkNone, NetworkOutbound:
		if len(r.Network.Allow) != 0 {
			return fmt.Errorf("%w: network allowlist requires allowlist mode", ErrInvalidRequest)
		}
	case NetworkAllowlist:
		if len(r.Network.Allow) == 0 {
			return fmt.Errorf("%w: allowlist mode requires at least one destination", ErrInvalidRequest)
		}
	default:
		return fmt.Errorf("%w: unsupported network mode %q", ErrInvalidRequest, r.Network.Mode)
	}

	switch r.Filesystem.Root {
	case "", RootReadOnly, RootReadWrite:
	default:
		return fmt.Errorf("%w: unsupported root filesystem mode %q", ErrInvalidRequest, r.Filesystem.Root)
	}

	return nil
}

// EffectiveNetworkMode returns the fail-closed network mode for the request.
func (r ExecRequest) EffectiveNetworkMode() NetworkMode {
	if r.Network.Mode == "" {
		return NetworkNone
	}
	return r.Network.Mode
}

// EffectiveRootFilesystemMode returns the fail-closed root mode for the request.
func (r ExecRequest) EffectiveRootFilesystemMode() RootFilesystemMode {
	if r.Filesystem.Root == "" {
		return RootReadOnly
	}
	return r.Filesystem.Root
}
