package docker

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"

	sandbox "github.com/luojiyin1987/Agent-Sandbox-Runtime"
)

const outboundNetworkLabel = "agent-sandbox-runtime=execution"

// WithOutboundNetwork grants this backend permission to honor
// NetworkOutbound requests. The zero-value backend remains fail-closed: a
// request cannot grant itself network access without this trusted operator
// capability.
func WithOutboundNetwork() Option {
	return func(b *Backend) error {
		b.allowOutbound = true
		return nil
	}
}

// prepareNetwork returns the Docker network name that should be attached to a
// container plus a cleanup function. NetworkNone keeps Docker's strongest
// built-in isolation. NetworkOutbound gets a fresh user-defined bridge so a
// sandbox never falls back to Docker's shared default bridge.
func (b *Backend) prepareNetwork(ctx context.Context, mode sandbox.NetworkMode) (string, func(), error) {
	switch mode {
	case sandbox.NetworkNone:
		return "none", func() {}, nil
	case sandbox.NetworkOutbound:
		name, err := networkName()
		if err != nil {
			return "", func() {}, fmt.Errorf("generate Docker network name: %w", err)
		}

		cleanup := func() {
			b.removeNetwork(name)
		}

		var stdout bytes.Buffer
		var stderr bytes.Buffer
		if err := b.run(
			ctx,
			&stdout,
			&stderr,
			"network", "create",
			"--driver", "bridge",
			"--opt", "com.docker.network.bridge.enable_icc=false",
			"--label", outboundNetworkLabel,
			name,
		); err != nil {
			// Network creation is another unknown-result operation: the daemon may
			// have created the named network even when the client did not receive
			// the success response. Always attempt cleanup by the already-known
			// random name before returning the error.
			cleanup()
			return "", func() {}, fmt.Errorf("docker network create failed: %w", err)
		}
		if stdout.Len() == 0 {
			cleanup()
			return "", func() {}, fmt.Errorf("docker network create returned an empty network ID")
		}
		return name, cleanup, nil
	default:
		return "", func() {}, fmt.Errorf("%w: network mode %q", ErrUnsupportedPolicy, mode)
	}
}

func (b *Backend) removeNetwork(name string) {
	ctx, cancel := context.WithTimeout(context.Background(), cleanupTimeout)
	defer cancel()
	_ = b.run(ctx, io.Discard, io.Discard, "network", "rm", name)
}

func networkName() (string, error) {
	var random [8]byte
	if _, err := rand.Read(random[:]); err != nil {
		return "", err
	}
	return "agent-sandbox-net-" + hex.EncodeToString(random[:]), nil
}
