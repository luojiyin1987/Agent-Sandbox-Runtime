# Writable workspace deployment boundary

Writable workspaces deliberately cross the container boundary: the trusted runtime bind-mounts a host directory into the sandbox at `/workspace` so Agent-generated workloads can persist artifacts.

That capability is useful, but two properties are **not** enforced by the current Docker/gVisor backend contract:

1. a byte or inode quota for the host bind-mounted workspace;
2. race-resistant host-path resolution against a concurrent host-side actor that can mutate workspace path components while a container is being created.

These are deployment responsibilities today. They must not be presented as runtime guarantees.

## Current runtime guarantees

For a configured `WithWorkspaceRoot(root)`, the Docker control plane:

- treats `root` as trusted backend configuration, not Agent input;
- canonicalizes the configured root before it is stored;
- requires `Filesystem.WorkspacePath` to be relative;
- rejects lexical `..` traversal and absolute paths;
- resolves request-time symlinks and rejects a resolved path outside the configured root;
- requires the selected workspace to exist and be a directory;
- fixes the container mount target to `/workspace`;
- uses recursive read-only bind semantics when `WorkspaceReadOnly=true`;
- keeps the container root filesystem read-only;
- bounds the optional `/tmp` tmpfs to 64 MiB.

These checks are intended to stop an untrusted `ExecRequest` from selecting an arbitrary host bind source or mount target.

## Writable workspace capacity is not a cgroup limit

`ResourceLimits` currently bound memory, processes, CPU, captured output, and execution time. They do **not** bound persistent storage written through a host bind mount.

A writable bind mount writes directly to the backing host filesystem. The sandbox can therefore consume whatever byte and inode capacity that filesystem and ordinary filesystem permissions make available to the runtime identity.

The 64 MiB `/tmp` tmpfs is different: it is an explicitly bounded ephemeral mount and its memory usage is also subject to the container memory limit.

Docker per-container storage-driver size options are not a substitute for a workspace quota. They constrain the container filesystem for supported storage drivers; the workspace is an external host bind mount and is intentionally outside that writable layer.

### Deployment requirement

If workspace exhaustion is part of the threat model, the operator must provision the workspace on storage with an enforceable quota outside this runtime contract.

Suitable designs include a dedicated quota-limited filesystem, project/dataset/subvolume quota, or another operator-controlled storage boundary that limits both bytes and, where required, inode/file-count exhaustion.

For mutually untrusted jobs, prefer one quota domain per execution or trust domain rather than one large shared filesystem quota. A global quota can prevent host-wide exhaustion while still allowing one workload to consume another workload's allocation.

The operator should also reserve host headroom for the Docker daemon, logs, image layers, runtime temporary files, and cleanup. A workspace quota equal to all remaining disk space is not an availability boundary.

## Path validation has a host-side TOCTOU assumption

Workspace validation and Docker bind-mount creation are two separate operations:

```text
runtime process
    |
    |  clean / EvalSymlinks / containment check / stat
    v
validated host path string
    |
    |  docker create --mount type=bind,src=<path>,dst=/workspace
    v
Docker daemon resolves and mounts the host path
```

The current implementation proves that the path it observed during validation was inside the trusted workspace root. It does not hold a kernel object or file descriptor that Docker later mounts.

Therefore the runtime does **not** claim to defeat a concurrent host-side principal that can replace, rename, or retarget relevant path components after validation but before the Docker daemon resolves the bind source.

This is a different threat from an Agent request containing `../` or a pre-existing escaping symlink. Those request-controlled cases are rejected by the current validation path.

### Deployment requirement

The workspace root and its ancestor path components must be controlled by the trusted operator during sandbox setup.

Do not place the configured workspace root underneath a directory where an unrelated untrusted host-side principal can rename or replace path components while executions are being created.

For concurrent mutually untrusted executions:

- allocate narrow workspace roots per trust domain, preferably per execution/session;
- pre-create the selected workspace under operator-controlled ancestors;
- do not expose a shared writable ancestor that one workload can use to rewrite another workload's future bind source;
- ensure lifecycle cleanup does not recycle a workspace path while another execution can still mutate it.

A workload may of course mutate files **inside the workspace it was intentionally granted**. That is the purpose of a writable workspace and is not considered an escape by itself.

## Why PR12 does not add `MaxStorageBytes`

Adding a field to `ResourceLimits` would imply a backend-neutral guarantee. The current Docker CLI path cannot provide that guarantee for arbitrary host bind mounts.

A trustworthy storage-limit API would need a concrete enforcement backend and conformance semantics, for example:

- creation of a dedicated quota domain before workload start;
- deterministic association of the workspace with that quota domain;
- byte and inode accounting semantics;
- cleanup behavior after timeout, cancellation, or daemon failure;
- explicit behavior for pre-existing workspace contents;
- cross-backend tests proving that the advertised limit is actually enforced.

Until those semantics exist, reporting a `MaxStorageBytes` policy would violate the project's **no silent downgrade** invariant.

## What stronger TOCTOU resistance would require

If the threat model includes a malicious local actor racing path resolution, the architecture must change rather than adding another string check.

A stronger design would need an operator-controlled mount/helper layer that resolves filesystem objects with race-resistant kernel primitives and keeps that object identity stable through mount creation. The current Docker CLI `--mount src=<path>` interface is path-based, so simply adding more `filepath` validation in the client process does not close the validation-to-daemon race.

That work should be treated as a separate backend capability with adversarial tests, not as an undocumented property of the current bind-mount implementation.

## Boundary matrix

| Property | Current runtime guarantee | Deployment responsibility |
| --- | --- | --- |
| Agent cannot choose arbitrary host bind source | yes | configure a narrow trusted root |
| lexical traversal rejected | yes | — |
| pre-existing symlink escape rejected at validation time | yes | — |
| fixed container target `/workspace` | yes | — |
| recursive read-only workspace when requested | yes | host/kernel must support enforcement |
| bounded `/tmp` tmpfs | yes, 64 MiB | reserve host memory/headroom |
| writable workspace byte quota | no | provision filesystem/project/dataset quota |
| writable workspace inode/file-count quota | no | provision an appropriate quota boundary |
| protection from concurrent hostile host-path replacement between validation and mount | no | keep workspace ancestors trusted/stable during setup |
| isolation between mutually untrusted workspace consumers | only to the configured capability boundary | use separate roots/quota domains and lifecycle ownership |

## Security interpretation

The current workspace policy is a **capability boundary**, not a storage service and not a malicious-host filesystem resolver.

It answers:

> Which operator-approved host subtree may this workload see, and is that subtree mounted read-only or read-write?

It does not yet answer:

> How many persistent bytes may this workload consume?

or:

> Can an adversarial host-side process race the mount setup path?

Keeping those statements separate makes the runtime contract auditable and preserves fail-closed semantics.