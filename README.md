# 🦞 Lobster

A fast, **single-binary** personal AI assistant — Go, **zero external dependencies**
(standard library only). Built to be small at the core and **extended in layers** —
your own LLM provider, skills, MCP servers — not locked to any vendor.

Lobster connects a chat (Telegram) to an LLM with **native tool calling**, runs real
tools on your own machine, and lets you **steer it mid-task**: a message you send while
it's working is folded in immediately, so a "no, do it differently" lands *before* it
commits to the wrong path.

> ⚠️ Lobster has a `shell` tool — whoever can message the bot can run commands on your
> machine. It's **locked to your chat ID by default**; keep it that way.

## Commands

| | |
|---|---|
| `/start` | meet the bot, get your chat ID |
| `/setup` | tune how it works with you |
| `/skills` | list installed skills |
| `/sessions` | browse past conversations |
| `/mcp` | show connected MCP servers |
| `/help` · `/id` · `/reset` | help · your chat ID · fresh conversation |

Or just talk to it — it has real tools and uses them.

## Why

- **Steerable** — interrupt and redirect the agent mid-run without breaking it.
- **Real tools on the host** — `shell` (PowerShell *or* bash), file I/O, background jobs.
- **Provider-agnostic** — any OpenAI- or Anthropic-compatible endpoint; no vendor lock-in.
- **Extensible** — drop in [Skills](#skills) and [MCP servers](#mcp); it can even add them
  itself at runtime.
- **Remembers you** — durable facts plus a searchable archive of every conversation.
- **Dependency-free & single-binary** — `go build`, copy it anywhere, run.

## Quickstart

Requires **Go 1.26+**.

```sh
cp lobster.example.json lobster.json    # set telegram.token + provider.*
go build -o lobster ./cmd/lobster       # Windows: -o lobster.exe
./lobster -config lobster.json
```

Then message your bot and send `/start`; it replies with your chat ID — add it to
`auth.allowed_chats` and restart.

## Providers

Works with **any OpenAI- or Anthropic-compatible API**. Pick the wire protocol with
`type`, point `base_url` at the endpoint, and set the auth — that's it, no per-vendor
code. `auth_scheme` places the key (`bearer` → `Authorization: Bearer`, `x-api-key`, or
`none`); `headers` adds any extras.

```json
"provider": {
  "type": "anthropic",
  "base_url": "https://your-endpoint/...",
  "api_key": "${LOBSTER_API_KEY}",
  "model": "your-model",
  "auth_scheme": "bearer"
}
```

`type` is `openai` or `anthropic` (the two protocols); `minimax` is a convenience preset
(Anthropic protocol + Bearer). Examples: OpenAI (`https://api.openai.com/v1`), Anthropic
(`https://api.anthropic.com`), or any compatible gateway / local server.

## Secrets (`.env`)

Keep keys out of `lobster.json`. Put them in **`~/.lobster/.env`** (`KEY=VALUE`, see
[`lobster.env.example`](lobster.env.example)). They're loaded into the environment, so
you can reference any of them in the config as `${NAME}`, and **MCP server subprocesses
inherit them automatically**. A real environment variable wins over the file.

## Skills

Supports [Agent Skills](https://www.anthropic.com/news/skills): a folder with a `SKILL.md`
(name + description + instructions) plus optional scripts, under `~/.lobster/skills/`. The
model only sees a skill's name/description until it's relevant, then loads the rest. Ask
Lobster to "make a skill for X" and it writes one itself.

## MCP

Supports [MCP](https://modelcontextprotocol.io) servers (stdio) — their tools appear to
the model alongside the native ones:

```json
"mcp": { "servers": [ { "name": "fs", "command": "npx",
  "args": ["-y", "@modelcontextprotocol/server-filesystem", "/path"] } ] }
```

`/mcp` lists what's connected. The agent can also add a server at runtime.

## Memory & personality

State lives in `~/.lobster/`: durable **facts** the agent saves about you, a rolling
conversation window, and a permanent **searchable session archive** (`search_sessions`,
`/sessions`). It has a personality and adapts to you via `/setup` (tone, verbosity, how
technical you are). Override the persona entirely with the config's `system` field.

## Access control

Locked by default — only chat IDs in `auth.allowed_chats` reach the model. `access_code`
is an optional shared-secret unlock; `open: true` disables the gate (local dev only).

*(A smoother one-command onboarding is on the [roadmap](#roadmap).)*

## Configuration

`lobster.json` (see [`lobster.example.json`](lobster.example.json)); any value may use
`${ENV_VAR}`:

| key | meaning |
|-----|---------|
| `telegram.token` | bot token from [@BotFather](https://t.me/BotFather) |
| `provider` | `type`, `base_url`, `api_key`, `model`, `max_tokens`, `auth_scheme`, `headers` |
| `auth` | `allowed_chats`, `access_code`, `open` |
| `mcp.servers` | `{ name, command, args, env, disabled }` |
| `system` | override the persona (empty = built-in) |
| `verbosity` | `quiet` · `normal` · `verbose` |
| `max_steps` | reason-act cap; `-1` = unlimited |

## Architecture

```
cmd/lobster        entry point
internal/config    JSON config + env/.env + ${VAR}
internal/llm       provider-agnostic chat (OpenAI / Anthropic protocols)
internal/tools     native tool registry + builtins (shell, files)
internal/agent     interruptible reason-act loop, per-turn dynamic prompt
internal/channel   channel interface + telegram impl
internal/memory    durable facts + preferences
internal/history   rolling transcript (live context window)
internal/session   permanent, searchable conversation archive
internal/skills    Agent Skills (SKILL.md)
internal/mcp       MCP client (JSON-RPC over stdio)
internal/bgproc    background command manager
internal/gateway   wiring: per-chat agents/tools, auth, telegram renderer
```

## Contributing

See [CONTRIBUTING.md](CONTRIBUTING.md). Keep it **dependency-free**; run
`go build ./... && go vet ./... && go test ./...` before a PR.

## Roadmap

- **One-command onboarding** — run it in the background; the bot hands you a single
  command to paste in your terminal that whitelists you automatically. Rework access
  control around this.
- More channels beyond Telegram.
- Memory: vector recall, summarizing compaction.
- MCP: HTTP/SSE transport, resources & prompts.

## License

[MIT](LICENSE).
