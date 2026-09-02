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
- unbounded stdout/stderr capture or Docker log storage
- long-running or hung processes
- privileged operations that depend on Linux capabilities
- dangerous syscalls covered by the selected backend's isolation layers
- privilege escalation through setuid/setgid or file capabilities after process start
- orphaned child processes after cancellation or timeout
- workspace path traversal and symlink escape attempts
- workload environment variables attempting to alter Docker client configuration

## Security invariants

1. **Fail closed.** Empty network policy means no network; empty root-filesystem policy means read-only.
2. **Zero is not unlimited.** A backend must apply safe resource defaults when limits are omitted.
3. **Policy validation precedes execution.** Invalid or unsupported policy must be rejected before starting a workload.
4. **Cancellation is terminal.** A cancelled or timed-out execution must not keep child processes running.
5. **Output is bounded.** Backends cap captured output. Docker logging must not create an unbounded host copy.
6. **No silent downgrade.** A backend must return an error when it cannot enforce a requested security boundary.
7. **Backend details do not weaken the contract.** Docker, gVisor, or future backends must satisfy the same backend-neutral conformance semantics even when their internal kernel interfaces differ.
8. **Workspace roots are capabilities.** An untrusted request may select only inside a trusted backend-configured workspace root; it cannot supply an arbitrary host bind source or container mount target.
9. **Workload environment is data, not control-plane configuration.** Request environment variables must not be able to redirect or reconfigure the Docker client used by the trusted runtime.
10. **Network access requires a trusted capability.** A request cannot grant itself Docker outbound access; the operator must explicitly enable that capability on the backend.
11. **Broad outbound is not an allowlist.** A backend must never satisfy `NetworkAllowlist` by silently falling back to an unrestricted bridge network.
12. **Docker process privilege hardening is non-optional.** Docker-created workloads run as the runtime's effective UID/GID with all Linux capabilities dropped, `no-new-privileges` enabled, and Docker's built-in seccomp profile explicitly selected in the OCI configuration. An untrusted request cannot relax those controls.
13. **Security profiles must not stale-fork stronger upstream defaults.** The Docker control plane does not vendor a frozen copy of Docker's built-in seccomp profile merely to customize it; any future syscall customization must preserve or tighten the then-current baseline.
14. **Experiments are not backend guarantees.** A standalone security experiment does not strengthen the production runtime contract until its lifecycle, platform requirements, and conformance semantics are explicitly integrated and tested through the backend.
15. **Backend identity is trusted configuration.** An `ExecRequest` cannot choose the OCI runtime. The gVisor backend fixes runtime selection to the operator-installed `runsc` runtime.
16. **Concurrent execution is bounded.** Each backend instance rejects work when its admission capacity is full.

## Resource admission boundary

Each Docker or gVisor backend instance allows one active execution by default.

The operator can set a different limit with `WithMaxConcurrentSandboxes`.
The operator can also set aggregate memory, PID, output, and CPU budgets.
Admission uses the effective request limits, including default values.
The backend holds each reservation through resource cleanup.

This control is local to one backend instance.
Separate processes do not share the same admission pool.
A multi-process host needs an external coordinator or a shared parent cgroup.
These host controls must cover all runtime processes on that host.

## Docker logging boundary

Each sandbox container uses Docker's `none` logging driver.
The runtime still captures attached stdout and stderr within `MaxOutputBytes`.
Docker does not retain a second persistent copy of workload output.

## Docker workspace assumptions

The trusted operator must configure `WithWorkspaceRoot` as the smallest host directory the workload is allowed to access. Everything beneath that root is considered granted according to the selected read-only/read-write mode.

The current Docker bind-mount implementation validates paths and symlinks against the local host filesystem. Therefore filesystem isolation with a workspace assumes the Docker daemon runs on the same host as the runtime process. Remote Docker contexts are not part of the supported workspace threat model.

Read-only workspaces request recursive read-only bind semantics. A host that cannot enforce that boundary must fail container creation rather than expose writable nested mounts.

Writable workspace access is also subject to ordinary Unix DAC permissions. The Docker workload uses the runtime process's numeric effective UID/GID so it does not need `CAP_DAC_OVERRIDE` merely to use a workspace owned by that runtime identity. This means the sandbox inherits the runtime identity's ordinary access to the explicitly mounted workspace, but no broader host path is exposed by that identity alone.

## Docker network assumptions

The default network policy uses Docker's `none` driver. Outbound access is available only when the trusted operator constructs the backend with `WithOutboundNetwork()` and the request explicitly selects `NetworkOutbound`.

Each outbound execution gets a fresh user-defined bridge instead of the shared Docker default bridge. Inter-container communication is disabled on that bridge and the network is removed after the sandbox container is removed. Network creation and removal are part of the runtime lifecycle, including unknown-result cleanup after a client-side create failure.

`NetworkOutbound` is intentionally broad. It follows the Docker host's routing and firewall policy and can therefore include host-gateway, LAN, or other routed destinations. It must not be described or relied on as "internet only".

Destination allowlists are not implemented by the current Docker or gVisor backend. `NetworkAllowlist` fails closed before any workload starts. Enforcing destination policy safely requires a trusted egress firewall or proxy and explicit handling of DNS resolution, address changes, and bypass paths.

## Docker process-hardening assumptions

The Docker control plane explicitly requests the runtime process's effective numeric UID/GID, `--cap-drop ALL`, `no-new-privileges=true`, and `seccomp=builtin` for every execution.

Matching the runtime UID/GID keeps filesystem permission checks aligned with the trusted process that granted the workspace. This avoids retaining `CAP_DAC_OVERRIDE` just to make a normal user-owned bind mount writable. If the runtime itself runs as root, the sandbox receives UID 0 but still has an empty effective capability set and remains subject to `no-new-privileges`, the selected OCI runtime, the read-only root, and the other isolation layers.

Dropping all capabilities removes Docker's ordinary capability allowlist rather than trying to predict which privileged capability an Agent-generated command might abuse. `no-new-privileges` prevents a process from gaining privileges through exec-time mechanisms such as setuid/setgid binaries or file capabilities. Under the normal Docker/runc backend, Docker's built-in seccomp profile runs in filter mode and blocks a set of dangerous or rarely needed syscalls while retaining broad application compatibility.

These controls are defense in depth, not a VM-strength boundary. Seccomp only filters syscalls; capabilities govern privileged kernel operations; namespaces isolate selected kernel resources; filesystem and network policies constrain different surfaces. Passing one layer does not imply another layer is redundant.

The runtime intentionally does not expose `--privileged`, capability additions, a request-selected container UID/GID, `seccomp=unconfined`, or a request-controlled seccomp profile. A custom seccomp JSON replaces Docker's built-in profile rather than extending it, so a stale vendored profile could accidentally lose later upstream hardening.

## gVisor backend assumptions

The gVisor backend uses the same trusted Docker control plane but forces container creation through a Docker runtime registered as `runsc`. The request cannot choose a different runtime name or pass arbitrary runsc flags.

`runsc` implements an application kernel in userspace. Workload syscalls are handled by the gVisor Sentry instead of being passed directly to the host Linux kernel in the same way as a normal runc container. This reduces direct host-kernel attack surface, but it does not make mounts, network policy, resource limits, identity, cleanup, or host filesystem capabilities redundant.

Host paths explicitly bind-mounted into a gVisor sandbox remain capabilities granted by the operator. The same workspace-root validation and read-only/read-write policy used by the Docker backend therefore remain part of the security boundary.

The backend-neutral conformance suite verifies observable behavior across Docker and gVisor. It deliberately does not require identical `/proc`, cgroup filesystem, or other implementation-internal representations. A backend may satisfy the same contract through different kernel/runtime mechanisms.

Docker still supplies its hardened OCI configuration when creating a runsc container, but workload-visible OCI seccomp behavior is not assumed to match runc. Loading OCI seccomp rules inside the gVisor sandbox is controlled separately by runsc's `--oci-seccomp` runtime configuration. gVisor also uses its own host-side isolation and seccomp mechanisms for its Sentry/runtime processes. Therefore Docker's `Seccomp=2` workload assertion remains a Docker/runc-specific integration check rather than a cross-backend invariant.

The project CI pins and checksum-verifies a specific gVisor release before registering `runsc`, performs a real `--runtime=runsc` preflight, and runs the shared conformance suite. This proves the tested runtime path, but it is not cryptographic remote attestation and does not protect against a malicious host administrator replacing the runtime or Docker configuration.

Like ordinary containers, gVisor is not claimed to be immune to implementation vulnerabilities, side channels, or attacks by a compromised host. It provides a stronger syscall isolation architecture than the normal Docker backend, not a promise equivalent to a hardware VM trust boundary.

## Landlock experiment boundary

`experiments/landlock` is intentionally outside both production backend execution paths. It evaluates Landlock as an additional Linux LSM restriction layer; it does not currently change `Runtime.Execute`, Docker create arguments, gVisor runtime configuration, or the backends' documented production guarantees.

The experiment handles filesystem write and mutation rights only. It grants those rights beneath one selected hierarchy while leaving reads unhandled. This proves that Landlock can remove write rights from paths which remain visible to the process, but it is not a full replacement for the mount namespace, read-only mounts, Unix DAC, or the trusted workspace capability boundary.

The experiment requires Landlock ABI 3 or newer so `LANDLOCK_ACCESS_FS_WRITE_FILE` and `LANDLOCK_ACCESS_FS_TRUNCATE` can be handled together. On ABI 8 or newer it uses `LANDLOCK_RESTRICT_SELF_TSYNC` for process-wide synchronization. On ABI 3-7, its `thread-locked` mode demonstrates behavior only on one locked OS thread; sibling Go runtime threads remain outside that new Landlock domain and this mode must not be treated as a whole-process sandbox.

Any future integration must also account for Landlock limitations that differ from mount isolation: file descriptors opened before enforcement retain their prior access properties, and current Landlock does not restrict every filesystem metadata action such as `chmod`, `chown`, `setxattr`, or `utime`.

Because Landlock is stackable and monotonic, it can only reduce access. It cannot grant an operation already denied by Unix DAC, another LSM, seccomp/capability requirements, a read-only mount, or namespace topology. This makes it a possible defense-in-depth layer rather than a replacement for the controls already enforced by the backends.

## Out of scope

This project does **not** claim protection against:

- Linux kernel zero-days or gVisor implementation vulnerabilities
- container-runtime escape vulnerabilities
- a compromised host kernel or container daemon
- a malicious host administrator or replacement `runsc` registration
- microarchitectural side channels
- VM-strength tenant isolation guarantees

VM-class isolation such as Kata Containers or Firecracker may be explored separately if the threat model requires a hardware-virtualization boundary.

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
        +-- mandatory Docker control-plane hardening
        +-- trusted Docker client environment
        +-- backend-selected OCI runtime
        |
        +-----------------------+
        |                       |
        v                       v
Docker / runc             Docker / runsc (gVisor)
        |                       |
        v                       v
Linux container           gVisor application kernel
        |                       |
        +-----------+-----------+
                    |
                    v
            Sandboxed workload
               (untrusted)

Standalone Landlock experiment
        |
        +-- probes running-kernel ABI
        +-- evaluates an extra LSM write boundary
        +-- does not modify either backend contract
```

The runtime control plane must never treat workload-provided text as trusted configuration for the backend.
