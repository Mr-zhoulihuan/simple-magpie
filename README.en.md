# simple-magpie

[简体中文](README.md) · **English**

A **minimal magpie**, written from scratch in Go: a local LLM gateway plus a
context-window dashboard.

- **Multi-protocol gateway**: serves OpenAI Chat, OpenAI Responses, Anthropic
  Messages and Gemini at once.
- **Protocol translation**: through a unified intermediate representation (IR),
  so any agent can use any provider.
- **Routing + failover**: provider/account pools, routing groups
  (`group/<id>`), strategies (order/rotate/usage/weighted), account cooldown,
  and session affinity (keeps the prompt cache warm).
- **Context-window dashboard**: squarified treemap, cache hit rate, headroom
  estimate, per-category breakdown — a re-implementation of magpie's
  "Context Window" page.
- **Conversation view**: per-turn transcripts with tool calls and results,
  grouped by date, collapsible, switchable by session.

## Screenshots

| Context window (treemap + cache + categories) | Conversation (per turn / date / collapse) |
| --- | --- |
| ![dashboard](docs/readme-dashboard.png) | ![conversation](docs/readme-conversation.png) |

Click any category item to see its real content, auto-formatted by type:

| Source / tool output (with line numbers) | JSON (tool_call args, highlighted) |
| --- | --- |
| ![content-code](docs/readme-content-code.png) | ![content-json](docs/readme-content-json.png) |

## Layout

```
simple-magpie/
├── cmd/simple-magpie/main.go   entry: gateway + dashboard on one port
├── config/config.json          sample config (providers/accounts/groups)
└── internal/
    ├── config/                 config load + validation
    ├── protocol/               unified IR + 4-protocol parse/build
    ├── gateway/                handle / relay / translate / record
    ├── routing/                candidate planning, strategies, cooldown, affinity
    ├── prompt/                 prompt dissection, token estimate, headroom
    ├── transcript/             IR <-> displayable conversation turns
    ├── store/                  in-memory records + per-session series
    └── web/                    dashboard (embedded HTML) + JSON API
```

## Quick start

```bash
# 1. edit config/config.json with your provider keys
# 2. run
go run ./cmd/simple-magpie -config config/config.json
```

Listens on `127.0.0.1:3425` by default (override with `-listen`).

### Point an agent at the gateway

```bash
# OpenAI-style
export OPENAI_BASE_URL=http://127.0.0.1:3425/v1
export OPENAI_API_KEY=smagpie

# Anthropic-style
export ANTHROPIC_BASE_URL=http://127.0.0.1:3425
export ANTHROPIC_API_KEY=smagpie

# Gemini-style
export GEMINI_BASE_URL=http://127.0.0.1:3425
```

### Open the dashboard

Visit <http://127.0.0.1:3425/dashboard> after sending a few requests:

- **Used context** `162K / 1.05M`, `used 15% · ~N requests left`
- **Prompt cache** `99% hit`, `160K from cache · 1.5K new`
- **Treemap**: each block by token share, free space hatched
- **Legend**: system prompt / tools / memory / files / tool results / chat / free
- **Detail table**: filter by category; **click any row (or treemap tile) to see
  the item's real content** — JSON pretty/highlighted, source/output with line
  numbers, tool descriptions with colored sections; copyable, wrap toggle.
- **Conversation**: per-turn list, grouped by date, collapsible; each turn shows
  system / you / tool calls (formatted JSON) / tool results / thinking / assistant,
  with copy buttons. Open directly at `/dashboard#conv`.

## Configuration

```jsonc
{
  "listen": "127.0.0.1:3425",
  "default_model": "deepseek/deepseek-chat",
  "affinity": "auto",            // auto | off | strict
  "providers": [
    {
      "id": "deepseek",
      "base_url": "https://api.deepseek.com",
      "protocols": ["chat"],     // protocols this provider speaks upstream
      "default_model": "deepseek-chat",
      "weight": 1,
      "accounts": [              // multiple accounts => failover
        { "name": "main", "key": "sk-..." }
      ]
    }
  ],
  "groups": [
    {
      "id": "daily-coding",
      "strategy": "order",       // order | rotate | usage | weighted
      "members": [
        { "provider": "deepseek",  "model": "deepseek-chat" },
        { "provider": "anthropic", "model": "claude-sonnet-4-5" }
      ]
    }
  ]
}
```

Request a model as `group/<id>`, `<provider>/<model>`, or a bare model name
(matched against `default_model`).

## How it works

> Detailed **architecture / request-flow / communication diagrams** are in
> [`docs/`](docs/README.md).

1. **Protocol translation (IR)** — each protocol has a parser (in) and a builder
   (out), both over a unified `protocol.Request` / `Reply`. N protocols need N
   parsers + N builders instead of N² paths.
2. **relay vs translate** — if the provider speaks the agent's protocol, relay
   as-is (keeping vendor-private fields); otherwise translate via the IR.
3. **Routing & failover** — `routing.Plan()` yields a candidate chain (groups by
   strategy; session affinity keeps a conversation on the account that answered
   it to reuse the prompt cache); on 429/402/5xx it rests the account and tries
   the next candidate.
4. **Prompt dissection** — `prompt.Analyze()` classifies each piece by source
   (system / tools / memory / files / results / chat) and estimates tokens
   (ASCII ≈ 4 chars/token, CJK ≈ 1 char/token, image ≈ 1600), then calibrates to
   the vendor's real `usage`. Headroom uses the median per-turn growth.

## Fixed port + LAN access

To open the dashboard from a browser by IP, listen on `0.0.0.0`. See
`config/config.public.json`:

```jsonc
{ "listen": "0.0.0.0:8080", ... }
```

```bash
go build -o bin/simple-magpie ./cmd/simple-magpie
./bin/simple-magpie -config config/config.public.json
# open http://<host-ip>:8080/dashboard
```

> Security: `0.0.0.0` exposes the gateway to everyone on the network and the
> gateway accepts any bearer token. Only do this on a trusted LAN; for public
> exposure add an authenticating reverse proxy or use `127.0.0.1` + an SSH tunnel.

## Testing with pi (provider name `zlh-simple-magpie`)

Register an OpenAI-compatible provider in `~/.pi/agent/models.json` pointing at
this gateway, then run:

```bash
pi --provider zlh-simple-magpie \
   --model zlh-simple-magpie/deepseek/deepseek-flash \
   --session-id demo --print --name zlh-simple-magpie \
   "Use the bash tool to run 'echo hi && ls', then summarize."
```

Open `/dashboard#conv` to see the per-turn transcript with tool calls/results.

## API

| Path | Description |
| --- | --- |
| `GET /dashboard` | Dashboard page (`#conv` opens the conversation) |
| `GET /dashboard/session?session=` | Context stats for one session (treemap data) |
| `GET /dashboard/turns?session=` | Per-turn items + tokens |
| `GET /dashboard/transcript?session=` | Flattened transcript |
| `GET /dashboard/sessions` | All sessions (for the switcher) |
| `GET /dashboard/recent?n=` | Recent requests |

Gateway endpoints: `/v1/chat/completions`, `/v1/responses`, `/v1/messages`,
`/v1beta/models/{model}:generateContent`, `GET /v1/models`.

## Differences from magpie

This is a teaching/demo implementation; deliberately simplified:

- streaming translation gathers then re-encodes (not event-by-event);
- no plugin system, masking, subscription bridge or persistence;
- basic routing strategies (no nested groups / intent routing / quota windows);
- context windows use a small built-in table (not models.dev).

But the **gateway core (multi-protocol, IR translation, routing, failover) and
the context-window dashboard** are fully runnable.

## License

MIT
