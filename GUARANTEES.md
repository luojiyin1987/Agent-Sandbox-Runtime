# v0.1 Guarantee Matrix

This document records the security and runtime guarantees that the current implementation can support with executable evidence. It is intentionally narrower than a feature list: every row distinguishes the contract, the enforcement mechanism, the evidence available today, and the boundary where the guarantee stops.

## Evidence levels

- **Contract** — represented in the backend-neutral API or documented backend policy.
- **Unit** — verified without a real container runtime, including policy compilation, lifecycle, error classification, and fuzz tests.
- **Docker integration** — verified against a real Docker daemon in the dedicated CI lane.
- **Shared conformance** — the same backend-neutral observable behavior is executed against both Docker and gVisor (`runsc`).
- **Experiment** — executable evidence exists, but the behavior is deliberately outside the production backend guarantee.

Passing a unit test is not treated as proof of kernel- or daemon-level enforcement when a stronger integration test is required.

## Guarantee matrix

| Guarantee | Contract / policy | Enforcement | Executable evidence | Boundary |
| --- | --- | --- | --- | --- |
| Fail-closed request validation | Invalid, ambiguous, or unsupported request shapes are rejected | Validation happens before backend execution and unsupported policy returns an error | Unit + fuzz | Does not prove an external runtime cannot be misconfigured outside this process |
| Safe resource defaults | Omitted memory, PID, CPU, output, and timeout fields are bounded rather than unlimited | Effective defaults are compiled before execution | Unit + Docker integration | Defaults are per execution; host-wide protection also depends on admission control |
| Strict Docker memory ceiling | `MaxMemoryBytes` is a total memory ceiling without an implicit swap allowance | Docker receives equal `--memory` and `--memory-swap` values | Unit + Docker integration reads `memory.max` and `memory.swap.max` | Docker/cgroup guarantee; not a generic host memory accounting claim |
| Per-backend admission control | Active executions cannot exceed trusted concurrency or aggregate memory/PID/output/CPU budgets | Reservation is acquired before Docker network/container creation and held through cleanup | Unit + Docker integration | Pool is local to one backend instance; multiple processes require an external coordinator or shared parent cgroup |
| Admission rejection has no Docker side effect | Capacity rejection must not create execution-owned Docker resources | Admission precedes `prepareNetwork` and `docker create` | Docker integration compares labeled container/network sets before and after rejection | Covers resources created by this backend; it does not coordinate unrelated Docker clients |
| Bounded captured output | Combined stdout/stderr capture respects `MaxOutputBytes` and reports truncation | In-process bounded capture; Docker container logging is disabled with `--log-driver none` | Unit + shared conformance for bounded output | Does not impose a quota on arbitrary files written to a writable workspace |
| Terminal timeout/cancellation | Timeout or cancellation ends the execution attempt and triggers cleanup | Context cancellation plus force-removal of execution-owned resources | Unit + shared conformance | If Docker removal itself fails, the runtime returns `ErrCleanup` because absence can no longer be proven |
| Cleanup failures are observable | Successful execution must not hide a failed resource cleanup | Container/network removal errors are joined with the execution result and counted/logged | Unit | No background reaper or cross-process ownership/lease reconciliation in v0.1 |
| Read-only container root | Zero-value filesystem policy keeps the container root read-only | Docker `--read-only` | Shared conformance: Docker + gVisor | Explicit writable root remains unsupported |
| Trusted workspace containment | A request may expose only a selected directory below an operator-configured workspace root | Absolute paths/traversal rejected; symlinks canonicalized; mount target fixed at `/workspace` | Unit + Docker integration for workspace behavior | Host path is validated before the Docker daemon resolves the bind mount; malicious host-side path replacement is outside the current guarantee |
| Recursive read-only workspace | A read-only workspace must not expose writable nested bind mounts | Recursive read-only bind semantics; unsupported hosts fail instead of silently downgrading | Docker integration with a real nested host bind mount | Dedicated nested-mount evidence is Docker-specific; shared gVisor conformance only checks observable read-only workspace behavior |
| Bounded `/tmp` | `TempDir=true` provides writable temporary storage with a 64 MiB cap | Docker tmpfs mounted at `/tmp` with fixed size | Docker integration observes tmpfs capacity; shared conformance checks temporary writes | tmpfs memory also counts toward the memory cgroup; this is not a persistent workspace quota |
| Default no-network route | Zero-value network policy provides no routed egress | Docker `none` network | Shared conformance: Docker + gVisor | Loopback semantics are not a destination-filtering policy |
| Opt-in broad outbound networking | Outbound networking requires both trusted backend capability and request policy | Fresh per-execution user-defined bridge with inter-container communication disabled | Unit + Docker integration | Broad routed egress follows host routes/firewall; it is not internet-only and may reach LAN/host-gateway destinations |
| Destination allowlist fails closed | Unsupported allowlist policy must never degrade to broad outbound access | Rejected before Docker calls | Unit | No v0.1 destination firewall, egress proxy, DNS pinning, or rebinding policy |
| Docker process privilege baseline | Workloads cannot request privileged execution or relax the minimum privilege controls | Runtime UID/GID, `cap-drop ALL`, `no-new-privileges`, Docker built-in seccomp | Unit + Docker integration checks UID/GID, `CapEff`, `NoNewPrivs`, `Seccomp`, and blocked privileged operations | Workload-visible Docker/runc seccomp state is not treated as a backend-neutral gVisor invariant |
| Immutable image identity by default | Sandbox image reference must resolve to an explicit lowercase SHA256 digest unless the trusted operator opts out | Backend constructor validates immutable reference format | Unit; CI pulls the pinned integration image digest | Digest pinning prevents tag drift; it does not verify provenance, signatures, SBOMs, or registry trust |
| Docker/gVisor backend-neutral behavior | Both production backends implement one `sandbox.Runtime` observable contract | gVisor reuses the Docker control plane while forcing OCI runtime `runsc` | Shared conformance in dedicated Docker and gVisor CI lanes | Backend-specific kernel/cgroup/`/proc` internals are deliberately excluded from shared conformance |
| Landlock write confinement research | Landlock can restrict selected filesystem mutations when the running kernel exposes the required ABI | Standalone Landlock ruleset experiment | Real-kernel experiment CI | Not part of the Docker/gVisor production backend contract; ABI 3–7 thread-locked mode is not whole-process Go confinement |

## Explicit non-guarantees in v0.1

The current runtime does **not** claim any of the following:

- persistent byte or inode quotas for writable host workspaces;
- protection against a malicious local host actor racing bind-mount path replacement after validation;
- destination/IP/domain network allowlisting;
- distributed or cross-process admission coordination;
- background orphan reaping with ownership leases;
- image provenance, signature, SBOM, or supply-chain verification;
- VM-strength isolation or immunity to Docker, kernel, or gVisor vulnerabilities;
- Landlock as a production backend enforcement layer.

These are kept explicit so future features cannot be inferred from adjacent controls. For example, a cgroup memory limit does not imply a workspace storage quota, and a pinned image digest does not imply provenance verification.

## CI evidence map

The repository keeps distinct CI lanes so evidence is attributable to the mechanism being tested:

- Go 1.26 / 1.27: formatting, `go vet`, race-enabled unit/fuzz tests.
- Docker integration: real daemon tests for Docker-specific enforcement and Docker shared conformance.
- gVisor integration: pinned `runsc` installation plus the shared backend-neutral conformance suite.
- Landlock integration: kernel probe, standalone demo, and real-kernel confinement test.

The v0.1 baseline should remain narrow: new claims require either existing executable evidence that directly observes the guarantee or a new test at the appropriate enforcement layer.
