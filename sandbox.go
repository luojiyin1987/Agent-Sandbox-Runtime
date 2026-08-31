package sandbox

import (
	"context"
	"errors"
	"fmt"
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
	RootReadOnly RootFilesystemMode = "read-only"
	RootReadWrite RootFilesystemMode = "read-write"
)

// FilesystemPolicy controls the sandbox root and optional workspace.
// An empty Root value is equivalent to RootReadOnly.
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
	ExitCode    int
	Stdout      []byte
	Stderr      []byte
	StartedAt   time.Time
	Duration    time.Duration
	Termination TerminationReason
}

var ErrInvalidRequest = errors.New("invalid sandbox request")

// Validate rejects ambiguous or unsafe request shapes before a backend sees them.
func (r ExecRequest) Validate() error {
	if r.Command == "" {
		return fmt.Errorf("%w: command is required", ErrInvalidRequest)
	}
	if r.Timeout < 0 {
		return fmt.Errorf("%w: timeout must not be negative", ErrInvalidRequest)
	}
	if r.Resources.MaxMemoryBytes < 0 || r.Resources.MaxProcesses < 0 || r.Resources.MaxOutputBytes < 0 || r.Resources.MilliCPUs < 0 {
		return fmt.Errorf("%w: resource limits must not be negative", ErrInvalidRequest)
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
