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
- unexpected outbound network access
- fork bombs and process exhaustion
- CPU and memory exhaustion
- unbounded stdout/stderr output
- long-running or hung processes
- privileged operations and dangerous syscalls when the backend can enforce them
- orphaned child processes after cancellation or timeout

## Security invariants

1. **Fail closed.** Empty network policy means no network; empty root-filesystem policy means read-only.
2. **Zero is not unlimited.** A backend must apply safe resource defaults when limits are omitted.
3. **Policy validation precedes execution.** Invalid or unsupported policy must be rejected before starting a workload.
4. **Cancellation is terminal.** A cancelled or timed-out execution must not keep child processes running.
5. **Output is bounded.** Backends must cap captured stdout/stderr rather than allow unbounded memory growth.
6. **No silent downgrade.** A backend must return an error when it cannot enforce a requested security boundary.
7. **Backend details do not weaken the contract.** Docker, gVisor, or future backends must satisfy the same conformance semantics.

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
        v
Execution backend
        |
        v
Sandboxed workload (untrusted)
```

The runtime control plane must never treat workload-provided text as trusted configuration for the backend.
