# 🦞 Lobster

A fast, native, **single-binary** personal AI assistant — written in Go with **zero
external dependencies** (standard library only).

Lobster connects a chat channel (Telegram) to an LLM with **native tool calling**, runs
real tools on your own machine, and lets you **steer it mid-task**: a message you send
while it's working is folded in immediately, so a "no, do it differently" lands *before*
it commits to the wrong path.

The goal is a small, hackable core extended in layers — skills, MCP servers, memory —
rather than a bloated framework.

> ⚠️ Lobster has a `shell` tool: whoever can message the bot can run commands on your
> machine. It is **locked to your chat ID by default** — keep it that way. See
> [Access control](#access-control).

## Features

- **Telegram bot** over plain HTTP long-polling — no SDK, no webhooks.
- **Pluggable LLMs** — OpenAI-, Anthropic-, and MiniMax-compatible providers.
- **Real tools on the host** — `shell` (PowerShell *or* bash), `read_file`, `write_file`,
  `list_dir`, plus self-aware tools: `remember`, `configure`, `search_sessions`,
  `use_skill`, `add_mcp_server`, `send_photo`, background jobs.
- **Steering on the fly** — interrupt and redirect the agent mid-run without breaking the
  tool-call/result pairing.
- **Streaming replies** — the answer types out live (toggleable) with a continuous
  *typing…* indicator that never stalls.
- **Vision** — send the bot a photo and it actually sees it (on a vision-capable model).
- **Three memory layers** — durable facts (`remember`), a rolling conversation window,
  and a permanent, **searchable session archive** (`search_sessions`).
- **Personality & setup** — it has a character, gets to know you, and adapts how it works
  (verbosity, streaming, tone, tech level, shell) via `/setup`.
- **Agent Skills** — Anthropic-style `SKILL.md` folders with progressive disclosure; it
  can even author its own.
- **MCP client** (stdio) — connect Model Context Protocol servers; their tools plug
  straight in, including at runtime.
- **Background jobs** for long tasks — non-blocking, with a ping when done.
- **Autonomous** — no step limit by default.

## Quickstart

Requires **Go 1.26+**.

1. Create a Telegram bot with [@BotFather](https://t.me/BotFather) and copy the token.
2. Copy the example config and fill it in:

   ```sh
   cp lobster.example.json lobster.json
   # set telegram.token and provider.* (api_key, model)
   ```

   Secrets can also come from the environment instead of the file: `LOBSTER_TELEGRAM_TOKEN`,
   `LOBSTER_API_KEY`, `LOBSTER_BASE_URL`, `LOBSTER_MODEL`, `LOBSTER_ACCESS_CODE`.

3. Build and run:

   ```sh
   go build -o lobster ./cmd/lobster      # on Windows: -o lobster.exe
   ./lobster -config lobster.json
   ```

4. Message your bot and send `/start`. It replies with your chat ID — add it to
   `auth.allowed_chats` and restart (see [Access control](#access-control)).

## Commands

`/start` · `/setup` · `/skills` · `/sessions` · `/mcp` · `/help` · `/id` · `/reset`

Or just talk to it normally — it has real tools and uses them.

## Providers

OpenAI-compatible (also covers proxies, Ollama, OpenRouter, …):

```json
"provider": { "type": "openai", "base_url": "https://api.openai.com/v1", "model": "gpt-4o-mini" }
```

Anthropic-compatible:

```json
"provider": { "type": "anthropic", "base_url": "https://api.anthropic.com", "model": "claude-3-5-sonnet-latest" }
```

MiniMax ([Anthropic-compatible](https://platform.minimax.io/docs/token-plan/claude-code),
Bearer auth, vision-capable; `base_url` defaults to the value below):

```json
"provider": { "type": "minimax", "base_url": "https://api.minimax.io/anthropic", "model": "MiniMax-M3" }
```

## Access control

The bot is **locked by default** — an unknown chat never reaches the model. Whitelisting
is by chat ID:

1. Start the bot and send it `/start`; it replies with your chat ID.
2. Paste that ID into the config and restart:

   ```json
   "auth": { "allowed_chats": ["123456789"] }
   ```

`auth` block:

- `allowed_chats` — authorized chat IDs (the primary mechanism).
- `access_code` — optional shared secret; a user sends it as a message to unlock their
  chat for the run. Also settable via `LOBSTER_ACCESS_CODE`.
- `open` — set to `true` to disable auth entirely (everyone admitted). **Local dev only**;
  the startup log warns loudly when it's on.

## Memory

Three layers, all in `~/.lobster/`:

- **Facts** — the agent saves durable things about you (`remember`) to `memory.json`;
  they're folded into the system prompt every turn.
- **Transcript** — the live conversation window (`history/`), trimmed to a budget so the
  context stays bounded.
- **Sessions** — a permanent, never-trimmed, **searchable archive** of every conversation
  (`sessions/`). The agent uses `search_sessions` to recall things from the past, so it
  won't claim it "doesn't remember" without checking. Browse with `/sessions`.

`/reset` starts a fresh conversation (clears the live window) but keeps facts and the
archive.

## Personality & setup

Lobster has a character and gets to know each user — lightly, never as a form. Run
`/setup` for a short interview (name, goals, tone, how technical you are, how much detail
to show, streaming on/off); it saves your preferences with `configure` and adapts. For
non-technical users it drops the jargon and never asks about shells or settings. Override
the whole persona via the config's `system` field.

## Skills

Lobster supports [Agent Skills](https://www.anthropic.com/news/skills): a skill is a
folder with a `SKILL.md` (YAML frontmatter `name` + `description`, then Markdown
instructions) plus any bundled scripts, under `~/.lobster/skills/<name>/`.

Discovery is always on but cheap — the model only sees each skill's name + description
until a task matches, then calls `use_skill` to load the full instructions and run its
scripts. This *progressive disclosure* keeps many skills from bloating the prompt.

```
~/.lobster/skills/
  pc-health/
    SKILL.md      # name + description + instructions
    check.sh      # bundled script the instructions call
```

Lobster can also **author its own skills** — ask it to "make a skill for X" and it writes
the folder, then `reload_skills`. See `examples/skills/` for a starter.

## MCP (Model Context Protocol)

Lobster is an [MCP](https://modelcontextprotocol.io) client: point it at MCP servers and
their tools appear to the model alongside the native ones. It speaks JSON-RPC 2.0 over
each server's **stdio** transport — stdlib only, no SDK.

```json
"mcp": {
  "servers": [
    { "name": "filesystem", "command": "npx",
      "args": ["-y", "@modelcontextprotocol/server-filesystem", "/path/to/allow"] }
  ]
}
```

Each enabled server is spawned and handshaked at startup; its tools are registered as
`<server>__<tool>`. A failed server is logged and skipped. `/mcp` lists what's connected.
The agent can also connect a server **at runtime** (`add_mcp_server`) — ask it to install
and wire one up and it does, no restart.

## Configuration

`lobster.json` (see [`lobster.example.json`](lobster.example.json)):

| key | meaning |
|-----|---------|
| `telegram.token` | Bot token from @BotFather |
| `provider` | `type` (`openai`/`anthropic`/`minimax`), `base_url`, `api_key`, `model`, `max_tokens` |
| `auth` | `allowed_chats`, `access_code`, `open` |
| `mcp.servers` | list of `{ name, command, args, env, disabled }` |
| `system` | override the persona/system prompt (empty = built-in) |
| `verbosity` | `quiet` · `normal` · `verbose` (default `normal`) |
| `streaming` | `on` · `off` (default `on`) |
| `max_steps` | reason-act step cap; `-1` = unlimited (default `40`) |
| `memory_file` / `history_dir` / `sessions_dir` / `skills_dir` | state paths (default under `~/.lobster/`) |

Most state lives in `~/.lobster/` (created on first run) so it survives restarts
regardless of the working directory. If the `-config` path doesn't exist, Lobster also
looks for `~/.lobster/lobster.json`.

## Architecture

```
cmd/lobster        entry point
internal/config    single JSON config + env overrides
internal/llm       provider-agnostic chat (OpenAI, Anthropic, MiniMax) + streaming + vision
internal/tools     native tool registry + builtins (shell, files)
internal/agent     interruptible reason-act loop, per-turn dynamic system prompt
internal/channel   channel interface + telegram impl (markdown, photos, streaming, commands)
internal/memory    per-user durable facts + preferences
internal/history   per-chat rolling transcript (the live context window)
internal/session   permanent, searchable conversation archive
internal/skills    Anthropic-style Agent Skills (SKILL.md discovery + loading)
internal/mcp       Model Context Protocol client (JSON-RPC over stdio)
internal/bgproc    background command manager (long-running jobs)
internal/event     timeline events
internal/gateway   wiring: per-chat agents/tools, auth, telegram renderer
```

## Contributing

Contributions welcome — see [CONTRIBUTING.md](CONTRIBUTING.md). In short: keep it
**dependency-free** (standard library only), run `go build ./... && go vet ./... &&
go test ./...` before sending a PR, and match the surrounding style.

## Roadmap

- Memory: vector recall, tiering, summarizing compaction.
- MCP: HTTP/SSE transport, resources & prompts.
- More channels beyond Telegram.
- Background terminals, missions, self-restart.

## License

[MIT](LICENSE).
