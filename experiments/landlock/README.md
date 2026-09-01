# Landlock experiment

This directory is a standalone experiment, not part of the Docker backend security contract.

Its purpose is to answer three questions before Landlock is considered for runtime integration:

1. Is Landlock available on the running Linux kernel, and which ABI is exposed?
2. Can a process add a second filesystem-write boundary without changing mount topology?
3. Can that restriction be made process-wide for a multithreaded Go program?

## Run

```sh
go run ./experiments/landlock probe
go run ./experiments/landlock demo
```

Example probe output:

```json
{
  "available": true,
  "abi": 8,
  "write_confinement": true,
  "process_wide_tsync": true
}
```

The demo creates two sibling directories before sandboxing:

```text
temporary root
├── allowed/
└── denied/
```

It then installs a Landlock ruleset that handles filesystem mutation rights while granting those rights only beneath `allowed/`.

The expected result is:

```text
create/write beneath allowed/   -> allowed
create beneath denied/          -> blocked
truncate beneath denied/        -> blocked
read beneath denied/            -> still allowed
```

Reads remain allowed intentionally because this experiment handles write/mutation rights only. It is measuring a narrow defense-in-depth layer, not a complete filesystem sandbox.

## ABI handling

The experiment probes the ABI at runtime with `landlock_create_ruleset(..., LANDLOCK_CREATE_RULESET_VERSION)` rather than assuming the build machine and runtime kernel match.

The write experiment requires ABI 3 or newer because `LANDLOCK_ACCESS_FS_TRUNCATE` was added in ABI 3. `WRITE_FILE` and `TRUNCATE` are handled together so an existing file cannot be shortened through a truncate path that bypasses the write-only part of the policy.

For ABI 2 and newer the mutation mask also includes `LANDLOCK_ACCESS_FS_REFER` so cross-directory rename/link behavior is explicit.

Current upstream kernel documentation has reached newer ABIs, but this experiment intentionally uses only the filesystem rights needed for this write-confinement question.

## Go threading boundary

This is the main reason the experiment is not wired into the runtime yet.

Landlock normally restricts the calling thread and its future descendants. A Go process is multithreaded, so restricting one goroutine is not equivalent to restricting the whole process.

- ABI 8+ supports `LANDLOCK_RESTRICT_SELF_TSYNC`, which applies the Landlock configuration atomically to all threads of the process. The experiment reports `process-tsync` in this case.
- On ABI 3-7, the demo locks the current goroutine to one OS thread before enforcing the ruleset and reports `thread-locked`. This proves syscall behavior on that thread, but it is **not** a production-safe whole-Go-process sandbox because sibling runtime threads remain outside the new Landlock domain.

A future runtime integration must therefore either require a kernel ABI with TSYNC, or arrange Landlock before executing an isolated child in a way that does not rely on unrestricted Go runtime sibling threads.

## Landlock vs mount isolation

Landlock and mount isolation solve different problems.

Docker bind mounts / mount namespaces define which filesystem trees are visible and whether a mount is read-only. Landlock is a stackable Linux Security Module that adds access-control restrictions to path hierarchies already visible to the process.

Landlock can only remove rights; it cannot grant access that Unix DAC, another LSM, a read-only mount, or namespace topology already denies. This makes it useful as defense in depth rather than a replacement for the existing filesystem-isolation layer.

Important current limitations also matter for the runtime design:

- file descriptors opened before Landlock enforcement keep the access properties associated with those opens;
- Landlock does not currently restrict every metadata operation such as `chmod`, `chown`, `setxattr`, or `utime`;
- this experiment does not handle Landlock network rights;
- this experiment does not change the Docker backend and does not strengthen its documented production boundary yet.

Upstream reference: https://docs.kernel.org/userspace-api/landlock.html
