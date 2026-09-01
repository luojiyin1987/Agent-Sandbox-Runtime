# Threat model

Agent Sandbox Runtime is intended to execute untrusted or accidentally dangerous tool workloads under an explicit, fail-closed policy.

## Assets to protect

- host filesystem and credentials
- host and adjacent network services
- host CPU, memory, process table, and storage
- other sandbox executions
- runtime availability and control-plane integrity

## In-scope threats

The runtime is designed to contain common Agent-generated workload failures and abuse, including:

- destructive shell commands
- accidental or malicious filesystem writes outside the granted workspace
- unexpected outbound network access when networking has not been granted
- fork bombs and process exhaustion
- CPU and memory exhaustion
- unbounded stdout/stderr output
- long-running or hung processes
- privileged operations that depend on Linux capabilities
- dangerous syscalls covered by Docker's built-in seccomp profile
- privilege escalation through setuid/setgid or file capabilities after process start
- orphaned child processes after cancellation or timeout
- workspace path traversal and symlink escape attempts
- workload environment variables attempting to alter Docker client configuration

## Security invariants

1. **Fail closed.** Empty network policy means no network; empty root-filesystem policy means read-only.
2. **Zero is not unlimited.** A backend must apply safe resource defaults when limits are omitted.
3. **Policy validation precedes execution.** Invalid or unsupported policy must be rejected before starting a workload.
4. **Cancellation is terminal.** A cancelled or timed-out execution must not keep child processes running.
5. **Output is bounded.** Backends must cap captured stdout/stderr rather than allow unbounded memory growth.
6. **No silent downgrade.** A backend must return an error when it cannot enforce a requested security boundary.
7. **Backend details do not weaken the contract.** Docker, gVisor, or future backends must satisfy the same conformance semantics.
8. **Workspace roots are capabilities.** An untrusted request may select only inside a trusted backend-configured workspace root; it cannot supply an arbitrary host bind source or container mount target.
9. **Workload environment is data, not control-plane configuration.** Request environment variables must not be able to redirect or reconfigure the Docker client used by the trusted runtime.
10. **Network access requires a trusted capability.** A request cannot grant itself Docker outbound access; the operator must explicitly enable that capability on the backend.
11. **Broad outbound is not an allowlist.** A backend must never satisfy `NetworkAllowlist` by silently falling back to an unrestricted bridge network.
12. **Process privilege hardening is non-optional.** Docker workloads run as the runtime's effective UID/GID with all Linux capabilities dropped, `no-new-privileges` enabled, and Docker's built-in seccomp profile explicitly selected. An untrusted request cannot relax those controls.
13. **Security profiles must not stale-fork stronger upstream defaults.** The Docker backend does not vendor a frozen copy of Docker's built-in seccomp profile merely to customize it; any future syscall customization must preserve or tighten the then-current baseline.

## Docker workspace assumptions

The trusted operator must configure `WithWorkspaceRoot` as the smallest host directory the workload is allowed to access. Everything beneath that root is considered granted according to the selected read-only/read-write mode.

The current Docker bind-mount implementation validates paths and symlinks against the local host filesystem. Therefore filesystem isolation with a workspace assumes the Docker daemon runs on the same host as the runtime process. Remote Docker contexts are not part of the supported workspace threat model.

Read-only workspaces request recursive read-only bind semantics. A host that cannot enforce that boundary must fail container creation rather than expose writable nested mounts.

Writable workspace access is also subject to ordinary Unix DAC permissions. The Docker workload uses the runtime process's numeric effective UID/GID so it does not need `CAP_DAC_OVERRIDE` merely to use a workspace owned by that runtime identity. This means the sandbox inherits the runtime identity's ordinary access to the explicitly mounted workspace, but no broader host path is exposed by that identity alone.

## Docker network assumptions

The default network policy uses Docker's `none` driver. Outbound access is available only when the trusted operator constructs the backend with `WithOutboundNetwork()` and the request explicitly selects `NetworkOutbound`.

Each outbound execution gets a fresh user-defined bridge instead of the shared Docker default bridge. Inter-container communication is disabled on that bridge and the network is removed after the sandbox container is removed. Network creation and removal are part of the runtime lifecycle, including unknown-result cleanup after a client-side create failure.

`NetworkOutbound` is intentionally broad. It follows the Docker host's routing and firewall policy and can therefore include host-gateway, LAN, or other routed destinations. It must not be described or relied on as "internet only".

Destination allowlists are not implemented by the current Docker backend. `NetworkAllowlist` fails closed before any Docker call. Enforcing destination policy safely requires a trusted egress firewall or proxy and explicit handling of DNS resolution, address changes, and bypass paths.

## Docker process-hardening assumptions

The Docker backend explicitly requests the runtime process's effective numeric UID/GID, `--cap-drop ALL`, `no-new-privileges=true`, and `seccomp=builtin` for every execution.

Matching the runtime UID/GID keeps filesystem permission checks aligned with the trusted process that granted the workspace. This avoids retaining `CAP_DAC_OVERRIDE` just to make a normal user-owned bind mount writable. If the runtime itself runs as root, the sandbox receives UID 0 but still has an empty effective capability set and remains subject to `no-new-privileges`, seccomp, read-only root, and the other isolation layers.

Dropping all capabilities removes Docker's ordinary capability allowlist rather than trying to predict which privileged capability an Agent-generated command might abuse. `no-new-privileges` prevents a process from gaining privileges through exec-time mechanisms such as setuid/setgid binaries or file capabilities. Docker's built-in seccomp profile runs in filter mode and blocks a set of dangerous or rarely needed syscalls while retaining broad application compatibility.

These controls are defense in depth, not a VM-strength boundary. Seccomp only filters syscalls; capabilities govern privileged kernel operations; namespaces isolate selected kernel resources; filesystem and network policies constrain different surfaces. Passing one layer does not imply another layer is redundant.

The runtime intentionally does not expose `--privileged`, capability additions, a request-selected container UID/GID, `seccomp=unconfined`, or a request-controlled seccomp profile. A custom seccomp JSON replaces Docker's built-in profile rather than extending it, so a stale vendored profile could accidentally lose later upstream hardening.

## Out of scope for the initial Docker backend

This project does **not** claim that a normal container is a VM-strength hostile multi-tenant security boundary. The initial backend does not promise protection against:

- Linux kernel zero-days
- container-runtime escape vulnerabilities
- a compromised host kernel or container daemon
- microarchitectural side channels
- attacks by a malicious host administrator

A later gVisor backend is intended to reduce direct exposure to the host-kernel syscall surface. VM-class isolation such as Kata Containers or Firecracker may be explored separately.

## Trust boundaries

```text
Agent / model output (untrusted)
        |
        v
ExecRequest + policy validation
        |
        v
Sandbox Runtime control plane (trusted)
        |
        +-- trusted image
        +-- trusted workspace root
        +-- runtime effective UID/GID
        +-- trusted outbound-network capability
        +-- mandatory process-hardening baseline
        +-- trusted Docker client environment
        |
        v
Execution backend
        |
        v
Sandboxed workload (untrusted)
```

The runtime control plane must never treat workload-provided text as trusted configuration for the backend.
