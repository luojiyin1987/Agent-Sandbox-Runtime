# Agent Sandbox Runtime

A policy-driven runtime for executing untrusted Agent tool workloads under explicit resource, filesystem, network, and process boundaries.

> Status: Docker execution backend with per-request resource, timeout, and output limits. Filesystem and network policy expansion remain fail-closed and are planned in later changes.

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
      +-- Docker   (resource limits available)
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
package main

import (
    "context"
    "fmt"
    "time"

    sandbox "github.com/luojiyin1987/Agent-Sandbox-Runtime"
    dockerbackend "github.com/luojiyin1987/Agent-Sandbox-Runtime/backend/docker"
)

func main() {
    runtime, err := dockerbackend.New("alpine:3.22")
    if err != nil {
        panic(err)
    }

    result, err := runtime.Execute(context.Background(), sandbox.ExecRequest{
        Command: "sh",
        Args:    []string{"-c", "printf hello"},
        Timeout: 5 * time.Second,
        Resources: sandbox.ResourceLimits{
            MaxMemoryBytes: 128 << 20,
            MaxProcesses:   32,
            MaxOutputBytes: 256 << 10,
            MilliCPUs:      500,
        },
    })
    fmt.Printf("exit=%d stdout=%q err=%v\n", result.ExitCode, result.Stdout, err)
}
```

Resource fields are independent overrides. A zero field keeps the Docker backend's safe default for that dimension:

| Resource | Zero-value default | Docker enforcement |
| --- | ---: | --- |
| memory | 256 MiB | `--memory` |
| processes | 64 | `--pids-limit` |
| CPU | 1000 millicores / 1 core | `--cpus` |
| captured stdout + stderr | 1 MiB | in-process bounded capture |
| timeout | 30 seconds | execution context + forced cleanup |

Docker requires an explicit memory limit to be at least 6 MiB; smaller non-zero values are rejected before container creation. An OOM-killed container is classified as `TerminationResourceLimit`. Non-zero application exit codes remain ordinary completed workload results.

The backend still enforces:

- network `none`
- read-only root filesystem
- timeout/cancellation cleanup through an independent `docker rm --force` context
- bounded combined stdout/stderr capture with `ExecResult.OutputTruncated`

Writable roots, workspace mounts, tmpfs, and non-`none` networking still return `docker.ErrUnsupportedPolicy` before Docker is called.

Request environment values are not interpolated into a shell command or placed in Docker CLI arguments. Only variable names are passed as `--env NAME`; values are inherited by the Docker CLI process environment.

## Roadmap

1. ✅ runtime contract and threat model
2. ✅ Docker execution backend baseline
3. ✅ resource and output limits
4. filesystem isolation
5. network isolation
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
