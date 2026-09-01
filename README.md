# Agent Sandbox Runtime

A policy-driven runtime for executing untrusted Agent tool workloads under explicit resource, filesystem, network, and process boundaries.

> Status: Docker execution backend with per-request resource limits, filesystem isolation, and opt-in outbound networking. Destination allowlists remain fail-closed.

## Why

Agent tools routinely execute model-generated shell commands and code. A useful runtime needs more than `docker run`: it needs one backend-neutral contract for what a workload is allowed to do, deterministic termination semantics, and conformance tests proving those boundaries are enforced.

```text
Agent / Harness
      |
      v
Sandbox Runtime API
      |
      +-- resource policy
      +-- filesystem policy
      +-- network policy
      +-- timeout / cancellation
      |
      v
Execution Backend
      +-- Docker   (resource + filesystem + network modes)
      +-- gVisor   (planned)
```

## Contract

```go
runtime.Execute(ctx, sandbox.ExecRequest{
    Command: "sh",
    Args: []string{"-c", "echo hello"},
})
```

The zero-value policy is intentionally fail-closed:

- network: `none`
- root filesystem: `read-only`
- omitted resource limits: backend-defined **safe defaults**, never unlimited

Invalid or unenforceable policy must be rejected instead of silently downgraded.

See [THREAT_MODEL.md](THREAT_MODEL.md) for the security boundary and non-goals.

## Docker backend

The Docker backend uses one fresh container per execution and requires the Docker CLI to be available to the runtime process.

```go
runtime, err := dockerbackend.New(
    "alpine:3.22",
    dockerbackend.WithWorkspaceRoot("/srv/agent-workspaces/session-123"),
    dockerbackend.WithOutboundNetwork(),
)
if err != nil {
    panic(err)
}

result, err := runtime.Execute(context.Background(), sandbox.ExecRequest{
    Command: "sh",
    Args:    []string{"-c", "cat input.txt; printf done > output.txt"},
    WorkDir: "/workspace",
    Timeout: 5 * time.Second,
    Resources: sandbox.ResourceLimits{
        MaxMemoryBytes: 128 << 20,
        MaxProcesses:   32,
        MaxOutputBytes: 256 << 10,
        MilliCPUs:      500,
    },
    Filesystem: sandbox.FilesystemPolicy{
        WorkspacePath: ".",
        TempDir:       true,
    },
    Network: sandbox.NetworkPolicy{
        Mode: sandbox.NetworkOutbound,
    },
})
```

### Resource limits

Resource fields are independent overrides. A zero field keeps the Docker backend's safe default for that dimension:

| Resource | Zero-value default | Docker enforcement |
| --- | ---: | --- |
| memory | 256 MiB | `--memory` |
| processes | 64 | `--pids-limit` |
| CPU | 1000 millicores / 1 core | `--cpus` |
| captured stdout + stderr | 1 MiB | in-process bounded capture |
| timeout | 30 seconds | execution context + forced cleanup |

Docker requires an explicit memory limit to be at least 6 MiB; smaller non-zero values are rejected before container creation. An OOM-killed container is classified as `TerminationResourceLimit`. Non-zero application exit codes remain ordinary completed workload results.

### Filesystem isolation

The container root filesystem remains read-only. A writable host workspace is opt-in and is constrained by a trusted backend configuration:

- `WithWorkspaceRoot(root)` defines the **maximum host filesystem scope** the runtime may expose.
- `Filesystem.WorkspacePath` is relative to that root; absolute paths and `..` traversal are rejected.
- the selected path is canonicalized with symlinks resolved and must remain inside the configured root.
- the workspace is always mounted at `/workspace`; Agent input cannot choose a container mount target.
- `WorkspaceReadOnly=true` adds a recursively read-only bind mount. If the host kernel cannot enforce recursive read-only semantics, Docker fails rather than silently downgrading the mount.
- `TempDir=true` adds a 64 MiB tmpfs at `/tmp`; tmpfs usage also counts against the container memory limit.

Configure the workspace root as narrowly as possible, ideally one root per Agent/session trust domain. Everything below the configured root is part of the granted host filesystem capability.

Workspace bind mounts currently require the Docker daemon to run on the same host as the runtime process because path and symlink validation happen on the local host filesystem before the bind mount is sent to Docker. Remote Docker contexts are not a supported filesystem-isolation configuration.

Explicit writable container roots are still rejected with `docker.ErrUnsupportedPolicy`.

### Network isolation

Networking has two independent gates: a trusted backend capability and the per-request policy.

- the zero-value backend does **not** grant outbound networking.
- `WithOutboundNetwork()` is a trusted operator capability that permits this backend to honor `NetworkOutbound` requests.
- `NetworkNone` remains the default and uses Docker's `none` driver, which leaves the workload without a default route.
- `NetworkOutbound` creates a fresh user-defined bridge for that execution, disables inter-container communication on the bridge, attaches only the sandbox container, and removes the bridge after container cleanup.
- the shared Docker default `bridge` is never used for sandbox outbound mode.
- network creation is treated as an unknown-result operation: a client-side create failure still triggers cleanup by the already-known random network name.

`NetworkOutbound` means broad routed egress according to the Docker host's routes and firewall. It is **not** an internet-only policy: host-gateway or LAN destinations may be reachable if the host networking permits them.

`NetworkAllowlist` remains unsupported and returns `docker.ErrUnsupportedPolicy` before Docker is called, even when `WithOutboundNetwork()` is enabled. Docker bridge creation alone does not enforce destination filtering. A trustworthy allowlist needs an operator-controlled firewall or egress proxy with explicit DNS/address semantics; it must not silently degrade to unrestricted outbound access.

### Environment boundary

Request environment variables are container data, not Docker control-plane configuration. They are written to a mode-`0600` temporary env file and passed with `docker create --env-file`; the file is deleted immediately after the create call returns.

This keeps values such as `DOCKER_HOST`, `DOCKER_CONTEXT`, `DOCKER_CONFIG`, proxy settings, and secrets out of the Docker CLI process environment and out of its argv. Newline, carriage-return, and NUL delimiters are rejected because they cannot be represented safely in the env-file transport.

The backend also continues to enforce timeout/cancellation cleanup and bounded combined stdout/stderr capture with `ExecResult.OutputTruncated`.

## Roadmap

1. ✅ runtime contract and threat model
2. ✅ Docker execution backend baseline
3. ✅ resource and output limits
4. ✅ filesystem isolation
5. ✅ network isolation (`none` + opt-in broad outbound; allowlist remains fail-closed)
6. syscall / capability policy
7. Linux Landlock experiments
8. gVisor backend and shared conformance suite

## Development

Requires Go 1.26 or newer. Docker integration tests are opt-in locally.

```sh
gofmt -w .
go vet ./...
go test -race ./...

SANDBOX_DOCKER_INTEGRATION=1 go test -race ./backend/docker -run TestDockerBackendIntegration -count=1
```
