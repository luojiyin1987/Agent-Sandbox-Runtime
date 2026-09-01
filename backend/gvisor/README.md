# gVisor backend

The gVisor backend implements the same `sandbox.Runtime` contract as the Docker backend while forcing Docker to execute each container with the registered `runsc` OCI runtime.

```go
runtime, err := gvisor.New(
    "alpine:3.22@sha256:14358309a308569c32bdc37e2e0e9694be33a9d99e68afb0f5ff33cc1f695dce",
    gvisor.WithWorkspaceRoot("/srv/agent-workspaces/session-123"),
)
```

The host must install gVisor and register a Docker runtime named `runsc` before constructing this backend. The project CI pins gVisor `release-20260817.0` and verifies the release archive checksum before installation.

Like the Docker backend, gVisor requires the sandbox image to be pinned by `sha256` digest by default. A digest prevents a registry tag from silently resolving to different image content between executions or deployments. `WithMutableImageReference()` is an explicit trusted-operator escape hatch for development/compatibility workflows; it should not be used for the production security boundary. Digest pinning is configuration integrity, not provenance verification: it does not prove who built or signed the image.

## What is shared with Docker

The gVisor backend deliberately reuses the Docker control-plane implementation instead of copying it. The following policy compilation and lifecycle behavior therefore stays in one place:

- request validation
- timeout and cancellation semantics
- deterministic container cleanup
- bounded stdout/stderr capture
- cgroup resource flags supplied through Docker
- trusted workspace-root validation and bind mounts
- read-only root filesystem and bounded `/tmp` tmpfs
- network `none` and the opt-in broad-outbound capability
- runtime UID/GID, capability dropping, and no-new-privileges Docker configuration
- immutable sandbox-image policy

The only execution-engine difference is the trusted Docker runtime selection: gVisor always injects `--runtime runsc` into `docker create`.

## Why gVisor is a separate backend

`runsc` is not just another host syscall filter. gVisor provides a userspace application kernel (the Sentry) that implements the Linux interface presented to the workload, reducing the workload's direct syscall exposure to the host kernel.

That does **not** make gVisor a VM-strength boundary and it does not make filesystem mounts, resource limits, network policy, or lifecycle cleanup redundant. Host paths explicitly mounted into a gVisor container are still capabilities granted to the sandbox.

## Seccomp boundary

The Docker control plane still passes its hardened OCI configuration when it creates a runsc container. However, the shared conformance suite does not require gVisor to expose runc-specific `/proc` seccomp or cgroup internals.

In particular, loading OCI seccomp filters *inside* the gVisor sandbox is controlled by runsc's `--oci-seccomp` runtime configuration. gVisor also applies its own host-side defense-in-depth mechanisms. Therefore the project treats workload-visible behavior as the cross-backend contract and keeps Docker/runc implementation-state assertions in Docker-specific tests.

## Shared conformance

Both Docker and gVisor run the same backend-neutral suite from `internal/conformance` covering:

- non-zero exit/result preservation
- environment and working directory behavior
- timeout termination
- bounded output
- read-only root filesystem
- writable workspace
- read-only workspace
- temporary-directory writes
- default no-network route

Docker keeps additional implementation-specific integration tests for cgroup values and process-hardening state.

## Non-goals

- gVisor is not presented as a VM or kernel-zero-day-proof boundary
- no KVM platform requirement; CI uses the default runsc platform
- no request-controlled runsc flags or runtime name
- no gVisor-specific network allowlist
- no image-signature or provenance verification
- no claim that a successful `dmesg` signature is a security-grade runtime attestation
