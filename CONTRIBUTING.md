# Contributing to Lobster

Thanks for your interest! Lobster aims to stay small, readable, and hackable.

## Ground rules

- **Zero dependencies.** Standard library only — no third-party Go modules. This is a
  hard constraint, not a preference. If something seems to need a dependency, open an
  issue first to discuss.
- **Keep the core lean.** New capabilities should plug in via the existing seams (tools,
  skills, MCP, channels) rather than bloating the core loop.
- **Match the style.** Read the surrounding code; comments explain *why*, not *what*.

## Before you open a PR

Make sure everything builds clean and tests pass:

```sh
go build ./...
go vet ./...
go test ./...
gofmt -l .   # should print nothing
```

Add tests for new behavior where it makes sense — see the existing `_test.go` files for
the in-package, table-driven style (and the fake provider / in-memory transports used to
test the agent loop, MCP client, and stores without network or subprocesses).

## Project layout

See the [Architecture](README.md#architecture) section of the README. In short: `internal/`
holds one package per concern, and `internal/gateway` wires them together.

## Security

Lobster runs real shell commands on the host. Be mindful of anything that widens what an
inbound message can do, and never weaken the default-locked auth without a clear opt-in.
If you find a security issue, please report it privately rather than opening a public
issue.
