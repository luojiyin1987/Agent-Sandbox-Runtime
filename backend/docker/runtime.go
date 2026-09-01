package docker

import (
	"context"
	"fmt"
	"io"
	"strings"

	sandbox "github.com/luojiyin1987/Agent-Sandbox-Runtime"
)

// WithContainerRuntime selects a trusted Docker OCI runtime for container
// creation. This is backend/operator configuration, not request policy: an
// ExecRequest cannot choose or override the runtime used for its workload.
//
// The Docker backend normally leaves runtime selection to the daemon. The
// gVisor backend uses this option to force the registered "runsc" runtime.
func WithContainerRuntime(name string) Option {
	return func(b *Backend) error {
		name = strings.TrimSpace(name)
		if name == "" || strings.ContainsAny(name, "\x00\r\n") {
			return fmt.Errorf("%w: container runtime name is invalid", sandbox.ErrInvalidRequest)
		}

		previous := b.run
		b.run = func(ctx context.Context, stdout, stderr io.Writer, args ...string) error {
			if len(args) != 0 && args[0] == "create" {
				withRuntime := make([]string, 0, len(args)+2)
				withRuntime = append(withRuntime, "create", "--runtime", name)
				withRuntime = append(withRuntime, args[1:]...)
				args = withRuntime
			}
			return previous(ctx, stdout, stderr, args...)
		}
		return nil
	}
}
