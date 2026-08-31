# Agent Sandbox Runtime

A policy-driven runtime for executing untrusted Agent tool workloads under explicit resource, filesystem, network, and process boundaries.

> Status: a baseline Docker execution backend is available. Custom resource, filesystem, and network policies are still intentionally rejected until their enforcement layers are implemented.

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
      +-- Docker   (baseline available)
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

The current Docker backend uses one fresh container per execution and requires the Docker CLI to be available to the runtime process.

```go
package main

import (
    "context"
    "fmt"

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
    })
    fmt.Printf("exit=%d stdout=%q err=%v\n", result.ExitCode, result.Stdout, err)
}
```

PR2 deliberately supports only a baseline policy:

- network is `none`
- root filesystem is `read-only`
- memory is capped at 256 MiB
- process count is capped at 64
- CPU is capped at 1 core
- captured stdout + stderr is capped at 1 MiB
- zero timeout becomes a 30 second backend default
- non-zero workload exit codes are returned in `ExecResult`, not treated as runtime failures
- timeout/cancellation attempts `docker rm --force` through a separate cleanup context

Custom `ResourceLimits`, writable roots, workspace mounts, tmpfs, and non-`none` networking currently return `docker.ErrUnsupportedPolicy` before Docker is called. Later PRs will replace those rejections with policy-specific enforcement.

Request environment values are not interpolated into a shell command or placed in Docker CLI arguments. Only variable names are passed as `--env NAME`; values are inherited by the Docker CLI process environment.

## Roadmap

1. ✅ runtime contract and threat model
2. ✅ Docker execution backend baseline
3. resource and output limits
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
