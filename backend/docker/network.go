package docker

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"

	sandbox "github.com/luojiyin1987/Agent-Sandbox-Runtime"
)

const outboundNetworkLabel = executionResourceLabel

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
func (b *Backend) prepareNetwork(ctx context.Context, mode sandbox.NetworkMode) (string, cleanupFunc, error) {
	noCleanup := cleanupFunc(func() error { return nil })
	switch mode {
	case sandbox.NetworkNone:
		return "none", noCleanup, nil
	case sandbox.NetworkOutbound:
		name, err := networkName()
		if err != nil {
			return "", noCleanup, fmt.Errorf("generate Docker network name: %w", err)
		}

		cleanup := cleanupFunc(func() error {
			return b.removeNetwork(name)
		})

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
			createErr := fmt.Errorf("docker network create failed: %w", err)
			// Network creation is another unknown-result operation: the daemon may
			// have created the named network even when the client did not receive
			// the success response. Cleanup failure must remain observable because
			// otherwise the runtime cannot prove that the network is absent.
			return "", noCleanup, errors.Join(createErr, cleanup())
		}
		if stdout.Len() == 0 {
			return "", noCleanup, errors.Join(
				fmt.Errorf("docker network create returned an empty network ID"),
				cleanup(),
			)
		}
		return name, cleanup, nil
	default:
		return "", noCleanup, fmt.Errorf("%w: network mode %q", ErrUnsupportedPolicy, mode)
	}
}

func (b *Backend) removeNetwork(name string) error {
	ctx, cancel := context.WithTimeout(context.Background(), cleanupTimeout)
	defer cancel()

	var stderr bytes.Buffer
	if err := b.run(ctx, io.Discard, &stderr, "network", "rm", name); err != nil {
		if dockerResourceMissing(stderr.String()) {
			return nil
		}
		return fmt.Errorf("%w: remove network %q: %v%s", ErrCleanup, name, err, dockerErrorSuffix(stderr.String()))
	}
	return nil
}

func networkName() (string, error) {
	var random [8]byte
	if _, err := rand.Read(random[:]); err != nil {
		return "", err
	}
	return "agent-sandbox-net-" + hex.EncodeToString(random[:]), nil
}
