package gvisor

import (
	"context"

	sandbox "github.com/luojiyin1987/Agent-Sandbox-Runtime"
	dockerbackend "github.com/luojiyin1987/Agent-Sandbox-Runtime/backend/docker"
)

const runtimeName = "runsc"

type config struct {
	workspaceRoot     *string
	allowOutbound     bool
	allowMutableImage bool
}

// Option configures trusted gVisor backend state.
type Option func(*config)

// WithWorkspaceRoot grants the same trusted workspace-root capability as the
// Docker backend. The selected host subtree is still validated by the shared
// Docker control-plane implementation before it is passed to runsc.
func WithWorkspaceRoot(root string) Option {
	return func(cfg *config) {
		cfg.workspaceRoot = &root
	}
}

// WithOutboundNetwork grants this backend permission to honor broad outbound
// networking requests. Destination allowlists remain unsupported and fail
// closed exactly as they do in the Docker backend.
func WithOutboundNetwork() Option {
	return func(cfg *config) {
		cfg.allowOutbound = true
	}
}

// WithMutableImageReference explicitly allows a trusted operator to configure
// a mutable image tag. Production callers should keep the default immutable
// sha256 digest requirement.
func WithMutableImageReference() Option {
	return func(cfg *config) {
		cfg.allowMutableImage = true
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
			option(&cfg)
		}
	}

	dockerOptions := make([]dockerbackend.Option, 0, 4)
	if cfg.workspaceRoot != nil {
		dockerOptions = append(dockerOptions, dockerbackend.WithWorkspaceRoot(*cfg.workspaceRoot))
	}
	if cfg.allowOutbound {
		dockerOptions = append(dockerOptions, dockerbackend.WithOutboundNetwork())
	}
	if cfg.allowMutableImage {
		dockerOptions = append(dockerOptions, dockerbackend.WithMutableImageReference())
	}
	// Add runtime selection last so gVisor.New always forces runsc regardless
	// of future trusted Docker options added above.
	dockerOptions = append(dockerOptions, dockerbackend.WithContainerRuntime(runtimeName))

	delegate, err := dockerbackend.New(image, dockerOptions...)
	if err != nil {
		return nil, err
	}
	return &Backend{delegate: delegate}, nil
}

// Execute runs one sandbox request inside the gVisor application kernel while
// preserving the backend-neutral result and termination contract.
func (b *Backend) Execute(ctx context.Context, req sandbox.ExecRequest) (sandbox.ExecResult, error) {
	return b.delegate.Execute(ctx, req)
}
