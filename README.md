# Agent Sandbox Runtime

A policy-driven runtime for executing untrusted Agent tool workloads under explicit resource, filesystem, network, and process boundaries.

> Status: Docker and gVisor (`runsc`) execution backends share one runtime contract and backend-neutral conformance suite. Docker provides resource limits, filesystem isolation, opt-in outbound networking, and mandatory process hardening; gVisor reuses the same control plane while moving workload syscall handling behind its userspace application kernel. A standalone Landlock experiment remains separate from the backend contract. Destination allowlists remain fail-closed.

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
      +-- process hardening
      +-- timeout / cancellation
      |
      v
Execution Backend
      +-- Docker   (runc / daemon default)
      +-- gVisor   (runsc application kernel)
             |
             +-- shared control-plane policy compilation
             +-- shared backend-neutral conformance

Standalone experiments
      +-- Landlock filesystem write confinement
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
- Docker process identity: runtime process effective UID/GID
- Docker process privileges: all capabilities dropped, no-new-privileges enabled, built-in seccomp forced

Invalid or unenforceable policy must be rejected instead of silently downgraded.

See [THREAT_MODEL.md](THREAT_MODEL.md) for the security boundary and non-goals.

## Docker backend

The Docker backend uses one fresh container per execution and requires the Docker CLI to be available to the runtime process.

```go
runtime, err := dockerbackend.New(
    "alpine:3.22@sha256:14358309a308569c32bdc37e2e0e9694be33a9d99e68afb0f5ff33cc1f695dce",
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

### Image identity

Docker and gVisor backends require an immutable `sha256` image reference by default:

```text
name@sha256:<64 lowercase hex>
```

A human-readable tag may be retained before the digest, for example `alpine:3.22@sha256:...`, but the digest is authoritative. Mutable references such as `alpine:3.22` or `alpine:latest` are rejected so a registry-side tag update cannot silently change the sandbox image without a runtime configuration change.

For development or compatibility workflows, a trusted operator may explicitly opt out with `dockerbackend.WithMutableImageReference()` or `gvisor.WithMutableImageReference()`. This is intentionally not the default production boundary.

Digest pinning provides **configuration integrity**, not provenance or authenticity. It does not verify who built or signed the image and does not replace registry trust, signature verification, SBOMs, or provenance policy.

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

### Process hardening

Every Docker workload gets the same non-optional baseline:

```text
--user <runtime-euid>:<runtime-egid>
--cap-drop ALL
--security-opt no-new-privileges=true
--security-opt seccomp=builtin
```

The numeric process identity deliberately matches the trusted runtime process's effective UID/GID. This preserves ordinary Unix DAC semantics for writable bind-mounted workspaces without depending on root's `CAP_DAC_OVERRIDE`: the sandbox can write files the runtime identity could write, but dropping capabilities does not accidentally make a normal user-owned workspace unusable.

These controls are deliberately not request-configurable. An untrusted workload may request resources, workspace access, or an operator-enabled network mode, but it cannot choose a different container user, ask the backend to add Linux capabilities, disable `no-new-privileges`, use `--privileged`, or run with `seccomp=unconfined`.

Forcing `seccomp=builtin` is intentional: it prevents a daemon configured with a custom or unconfined default from silently weakening this runtime. The project does not vendor and fork Docker's default seccomp JSON because doing so could lag behind Docker security updates; custom syscall policy can be explored only when it preserves or tightens the current built-in baseline.

Docker integration tests inspect the workload identity and `/proc/self/status` and require:

```text
uid/gid     = runtime effective uid/gid
CapEff      = 0000000000000000
NoNewPrivs  = 1
Seccomp     = 2
```

They also verify privileged `mknod` and `mount` attempts fail. These checks prove the workload-visible state rather than only checking generated CLI arguments.

## gVisor backend

The gVisor backend implements the same `sandbox.Runtime` interface but forces Docker to use the registered `runsc` OCI runtime:

```go
runtime, err := gvisor.New(
    "alpine:3.22@sha256:14358309a308569c32bdc37e2e0e9694be33a9d99e68afb0f5ff33cc1f695dce",
    gvisor.WithWorkspaceRoot("/srv/agent-workspaces/session-123"),
)
```

`backend/gvisor` deliberately does not copy the Docker lifecycle implementation. It reuses request validation, cleanup, resource flags, workspace validation, filesystem policy, network policy, environment transport, image-reference policy, and output/timeout semantics, then adds trusted `--runtime runsc` selection.

This matters because gVisor is not merely another seccomp profile. `runsc` places a userspace application kernel between the workload and the host kernel, reducing direct host-kernel syscall exposure while preserving the OCI/Docker integration model.

The shared conformance suite in `internal/conformance` runs against both Docker and gVisor and verifies observable behavior rather than implementation internals:

- result, environment, and working-directory semantics
- timeout termination
- bounded output
- read-only root
- writable and read-only workspaces
- writable `/tmp` when requested
- no default route under the zero-value network policy

Docker-specific assertions such as host cgroup file values and workload-visible `/proc/self/status` seccomp fields remain Docker-specific. In particular, loading OCI seccomp filters inside a gVisor sandbox is controlled by runsc's `--oci-seccomp` runtime configuration; it is not treated as a cross-backend `/proc` invariant.

CI pins gVisor `release-20260817.0`, verifies the release archive SHA256 before installation, registers `runsc` with Docker, performs a real `--runtime=runsc` preflight using the pinned sandbox-image digest, and runs the full shared conformance suite.

See [backend/gvisor/README.md](backend/gvisor/README.md) for the backend-specific boundary.

## Landlock experiment

`experiments/landlock` evaluates whether Linux Landlock can add a second filesystem-write boundary without changing mount topology or weakening the existing Docker controls.

```sh
go run ./experiments/landlock probe
go run ./experiments/landlock demo
```

The experiment probes the Landlock ABI at runtime. It requires ABI 3+ for the write-confinement demo so `WRITE_FILE` and `TRUNCATE` can be handled together. It grants filesystem mutation rights only beneath one temporary `allowed/` hierarchy, verifies creation and truncation are blocked under a sibling `denied/` hierarchy, and deliberately leaves reads unhandled to make the narrow scope explicit.

The Go threading result is especially important:

- ABI 8+ can use `LANDLOCK_RESTRICT_SELF_TSYNC` and reports `process-tsync`, meaning the Landlock domain is applied atomically to all threads of the process.
- ABI 3-7 reports `thread-locked`; the demo pins the executing goroutine to one OS thread, but sibling Go runtime threads remain outside that Landlock domain. This is an experiment only, not a production whole-process guarantee.

Landlock is a stackable LSM and only removes rights. It complements Unix DAC, seccomp, and mount namespaces; it does not replace the read-only root, trusted workspace mount boundary, or other Docker isolation. File descriptors opened before enforcement keep their prior access properties, and current Landlock does not restrict every metadata operation such as `chmod`, `chown`, `setxattr`, and `utime`.

See [experiments/landlock/README.md](experiments/landlock/README.md) for the detailed boundary and rationale. No Docker backend request or security guarantee depends on this experiment yet.

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
6. ✅ syscall / capability baseline (runtime UID/GID + `cap-drop ALL` + no-new-privileges + built-in seccomp)
7. ✅ Linux Landlock capability + confinement experiment (standalone; not backend enforcement)
8. ✅ gVisor backend + shared backend-neutral conformance suite

## Development

Requires Go 1.26 or newer. Docker, gVisor, and Landlock integration tests are opt-in locally.

```sh
gofmt -w .
go vet ./...
go test -race ./...

SANDBOX_DOCKER_INTEGRATION=1 go test -race ./backend/docker -run 'TestDocker.*Integration' -count=1

# Requires a Docker runtime registered as "runsc"
SANDBOX_GVISOR_INTEGRATION=1 go test -race ./backend/gvisor -run TestGVisorConformanceIntegration -count=1 -v

# Linux kernel Landlock probe + standalone demo
go run ./experiments/landlock probe
go run ./experiments/landlock demo
SANDBOX_LANDLOCK_INTEGRATION=1 go test -race ./experiments/landlock -run TestLandlockConfinementIntegration -count=1
```
