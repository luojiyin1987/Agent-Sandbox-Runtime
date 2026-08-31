# Agent Sandbox Runtime

A policy-driven runtime for executing untrusted Agent tool workloads under explicit resource, filesystem, network, and process boundaries.

> Status: execution contract only. No Docker or other execution backend is implemented yet.

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
      +-- Docker   (planned)
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

## Roadmap

1. ✅ runtime contract and threat model
2. Docker execution backend
3. resource and output limits
4. filesystem isolation
5. network isolation
6. syscall / capability policy
7. Linux Landlock experiments
8. gVisor backend and shared conformance suite

## Development

Requires Go 1.26 or newer.

```sh
gofmt -w .
go vet ./...
go test -race ./...
```
