# Contributing to Lobster

First: thank you for caring. A short, honest read before you spend time.

## The philosophy (please read this)

I want Lobster to stay **small and sharp**. I do **not** want it to turn into a giant
where every commit bolts on another feature that either breaks something that already
worked, or just piles up and bloats the whole thing. A bigger surface is not a better
project.

So:

- **Have an idea? Open an issue first.** Let's talk about it before any code. If it's a
  good idea, great — let's shape it so it fits. Please don't drop a large feature PR out
  of nowhere; I'm unlikely to merge it.
- **I'm happy to take fixes and clean improvements.** Bug fixes, small well-scoped
  refinements, simplifications, better tests, docs — yes, please. The bar is: it must be
  **correct, clean, minimal, and not break or clutter what's already there**.
- **A change should earn its place.** Tight, tested, no new dependencies, no duplication,
  fits the existing style. If it adds more complexity than value, it doesn't belong.

If in doubt, smaller is better. A change that deletes code while keeping behavior is
almost always welcome.

## Hard rules

- **Zero dependencies.** Standard library only — no third-party Go modules. This is a
  constraint, not a preference. If something seems to need a dependency, open an issue.
- **Keep the core lean.** New capabilities plug in through the existing seams (tools,
  skills, MCP, channels), not by growing the core loop.
- **Match the style.** Read the surrounding code; comments explain *why*, not *what*.

## Before you open a PR

```sh
go build ./...
go vet ./...
go test ./...
gofmt -l .   # should print nothing
```

Add tests for new behavior where it makes sense — see the existing `_test.go` files for
the in-package style (and the fakes / in-memory transports used to test the agent loop,
MCP client, and stores without network or subprocesses). Keep the PR focused: one change,
described clearly.

## Project layout

See the [Architecture](README.md#architecture) section of the README: `internal/` holds
one package per concern, and `internal/gateway` wires them together.

## Security

Lobster runs real shell commands on the host. Be mindful of anything that widens what an
inbound message can do, and never weaken the default-locked auth without a clear opt-in.
Found a security issue? Please report it privately rather than opening a public issue.
