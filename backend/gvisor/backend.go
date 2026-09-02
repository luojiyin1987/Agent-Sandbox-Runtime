package gvisor

import (
	"context"
	"log/slog"

	sandbox "github.com/luojiyin1987/Agent-Sandbox-Runtime"
	dockerbackend "github.com/luojiyin1987/Agent-Sandbox-Runtime/backend/docker"
)

const runtimeName = "runsc"

type config struct {
	dockerOptions []dockerbackend.Option
}

// Option configures trusted gVisor backend state.
type Option func(*config) error

// WithWorkspaceRoot grants the same trusted workspace-root capability as the
// Docker backend. The selected host subtree is still validated by the shared
// Docker control-plane implementation before it is passed to runsc.
func WithWorkspaceRoot(root string) Option {
	return func(cfg *config) error {
		cfg.dockerOptions = append(cfg.dockerOptions, dockerbackend.WithWorkspaceRoot(root))
		return nil
	}
}

// WithOutboundNetwork grants this backend permission to honor broad outbound
// networking requests. Destination allowlists remain unsupported and fail
// closed exactly as they do in the Docker backend.
func WithOutboundNetwork() Option {
	return func(cfg *config) error {
		cfg.dockerOptions = append(cfg.dockerOptions, dockerbackend.WithOutboundNetwork())
		return nil
	}
}

// WithMutableImageReference explicitly allows a trusted operator to configure
// a mutable image tag. Production callers should keep the default immutable
// sha256 digest requirement.
func WithMutableImageReference() Option {
	return func(cfg *config) error {
		cfg.dockerOptions = append(cfg.dockerOptions, dockerbackend.WithMutableImageReference())
		return nil
	}
}

// WithAdmissionLimits sets trusted totals across active executions.
func WithAdmissionLimits(limits sandbox.AdmissionLimits) Option {
	return func(cfg *config) error {
		cfg.dockerOptions = append(cfg.dockerOptions, dockerbackend.WithAdmissionLimits(limits))
		return nil
	}
}

// WithLogger enables structured backend event logs.
func WithLogger(logger *slog.Logger) Option {
	return func(cfg *config) error {
		cfg.dockerOptions = append(cfg.dockerOptions, dockerbackend.WithLogger(logger))
		return nil
	}
}

// Backend executes the common sandbox contract through Docker while forcing
// Docker to use the registered gVisor runsc OCI runtime.
type Backend struct {
	delegate *dockerbackend.Backend
}

var _ sandbox.Runtime = (*Backend)(nil)

// New creates a gVisor backend pinned to a trusted container image. The host
// must already have a Docker runtime registered under the name "runsc".
func New(image string, options ...Option) (*Backend, error) {
	cfg := config{}
	for _, option := range options {
		if option != nil {
			if err := option(&cfg); err != nil {
				return nil, err
			}
		}
	}

	// Add runtime selection last so gVisor.New always forces runsc regardless
	// of future trusted Docker options added above.
	cfg.dockerOptions = append(cfg.dockerOptions, dockerbackend.WithContainerRuntime(runtimeName))

	delegate, err := dockerbackend.New(image, cfg.dockerOptions...)
	if err != nil {
		return nil, err
	}
	return &Backend{delegate: delegate}, nil
}

// Stats returns counters and current resource reservations.
func (b *Backend) Stats() dockerbackend.Stats {
	return b.delegate.Stats()
}

// Execute runs one sandbox request inside the gVisor application kernel while
// preserving the backend-neutral result and termination contract.
func (b *Backend) Execute(ctx context.Context, req sandbox.ExecRequest) (sandbox.ExecResult, error) {
	return b.delegate.Execute(ctx, req)
}
