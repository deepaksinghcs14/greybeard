<p align="center">
  <picture>
    <source media="(prefers-color-scheme: dark)" srcset="assets/logo-dark.svg">
    <img src="assets/logo.svg" width="220" alt="greybeard, the old man who remembers what your repos forgot">
  </picture>
</p>

<h1 align="center">greybeard</h1>

<p align="center">
  <em>He remembers what your repos forgot.</em>
</p>

<p align="center">
  <img src="https://img.shields.io/github/v/release/deepaksinghcs14/greybeard?style=flat-square&color=111111&label=release" alt="Release">
  <img src="https://img.shields.io/github/license/deepaksinghcs14/greybeard?style=flat-square&color=111111" alt="MIT license">
  <img src="https://img.shields.io/badge/storage-1%20sqlite%20file-111111?style=flat-square" alt="One SQLite file">
</p>

The old man has been here longer than any of your microservices. He watched
`orders-svc` split off from the monolith. He remembers who still reads that
table you think nobody uses. Your AI agent is scoped to one repo at a time;
he isn't. greybeard puts him behind an MCP server your agent asks before it
makes a locally-correct, globally-breaking change.

## Before / after

You ask your agent to rename the `total` column in the orders table.

Without greybeard:

> Done — updated the migration, the model, and all references in this repo.
> All tests pass. ✅

*(billing-svc's nightly reconciliation, which reads `orders.total` directly,
breaks silently at 2am.)*

With greybeard:

> Before I touch this: the graph shows **billing-svc** reads the `orders`
> schema directly. A rename will break its reconciliation silently. Proceed
> and flag billing-svc, add a compatibility view, or hold off?

## How it works

```
1. Extract   what repos declare: go.mod/package.json, OpenAPI, protos, SQL migrations
2. Store     a typed graph in one SQLite file (~/.greybeard/graph.db)
3. Serve     three MCP queries: what's related · who calls this · who reads this schema
4. Learn     agents record relationships they verify in code, with file:line evidence
```

Every edge carries its type — `imports` (declared, hard fact) ›
`calls_api` (matched in quoted strings) › `shares_schema` (matched in SQL
context) — and its provenance (`scanned` vs `agent`), so the agent knows how
hard each constraint is. Below the evidence bar nothing enters the graph:
names a repo declares itself resolve locally, `/health` never links anything,
and a `users` table only links repos that already share a harder edge. Same
name is not same thing. Node/edge model:
[graph-schema.md](skills/greybeard/references/graph-schema.md).

A session-start hook keeps the current repo's data fresh (and the binary
updated) automatically. Extraction gaps are "unknown," never "no dependency."

## Install

### Claude Code

```
/plugin marketplace add deepaksinghcs14/greybeard
```
```
/plugin install greybeard@greybeard
```

That's everything on macOS/Linux — skill, slash commands, session hook, MCP
server, and the binary bootstraps itself on first use (downloaded once from
Releases, sha256-verified). Then index your repos, once:

```
/greybeard-init ~/code
/greybeard-build
```

### Codex

```bash
cp -r adapters/codex/greybeard ~/.agents/skills/greybeard
codex mcp add greybeard -- ~/.agents/skills/greybeard/scripts/greybeard.sh serve
```

Same self-bootstrapping launcher. Older Codex builds read `~/.codex/skills/`.

### Prebuilt binary

Required on Windows; optional everywhere else. See
[Releases](https://github.com/deepaksinghcs14/greybeard/releases) for
darwin/linux/windows, amd64/arm64:

```bash
# macOS Apple Silicon
sudo curl -L https://github.com/deepaksinghcs14/greybeard/releases/latest/download/greybeard_darwin_arm64 -o /usr/local/bin/greybeard
sudo chmod +x /usr/local/bin/greybeard
```

### From source

```bash
go install github.com/deepaksinghcs14/greybeard/cmd/greybeard@latest
```

A binary already on PATH always beats the bootstrap, so your build stays in charge.

### Cursor / Windsurf / Cline

No plugin system on these hosts — three pieces: install the binary (above),
paste [`adapters/instruction-only/greybeard.md`](adapters/instruction-only/greybeard.md)
into your always-on rules (`.cursorrules` / `.windsurfrules` / Cline custom
instructions), and register the MCP server:

```json
{ "mcpServers": { "greybeard": { "command": "greybeard", "args": ["serve"] } } }
```

Cursor: `~/.cursor/mcp.json` · Windsurf: `~/.codeium/windsurf/mcp_config.json` ·
Cline: MCP Servers panel. Then `greybeard init --root ~/code && greybeard build`
in a terminal; no hooks here, so rerun `build` when the graph feels stale.

### Uninstall

```bash
greybeard uninstall --purge   # removes the binary and ~/.greybeard
```

Then `/plugin uninstall greybeard@greybeard` in Claude Code. A `go install`
copy lives in `~/go/bin` — remove that one too if you made one.

## Commands

| Command | What it does |
|---------|--------------|
| `/greybeard-init` | Register every repo under a root — run once |
| `/greybeard-build` | Full extraction, per-repo progress relayed in chat |
| `/greybeard-query` | Ask the graph anything, in plain language |
| `/greybeard-audit` | What's empty, what's stale — changes nothing |
| `/greybeard-visualize` | Open the interactive graph in your browser, from chat |
| `greybeard visualize` | Same, from the terminal (`--port`, default 7333) |
| `greybeard build --background` | Detached build, desktop notification when done |
| `greybeard clean [--all]` | Forget extracted relations (`--all`: everything) |
| `greybeard update` | Self-update — also runs daily in the background |

The agent-facing queries (`get_related_repos`, `get_callers_of`,
`get_schema_dependents`, `record_relation`) are documented in
[mcp-tools.md](skills/greybeard/references/mcp-tools.md).

## Development

```bash
make install-system   # build + install to ~/go/bin AND /usr/local/bin (what hooks resolve)
make check            # everything CI runs: build, vet, tests, adapter freshness
make adapters         # regenerate adapters/ after editing the canonical skill
```

After switching binaries, `greybeard clean && greybeard build` so the graph
reflects the current extraction rules. macOS note: never `cp` over a binary
in place — Apple Silicon kills it on a stale code signature; the make targets
do the safe remove-then-copy.

## FAQ

**Does it phone home?**
No. One SQLite file on your machine. No hosted service, no API keys, no telemetry.

**Why not Neo4j? Or Postgres?**
A few hundred repos make a few thousand edges, and the queries are exact
lookups plus a shallow walk. That workload doesn't earn a database server to
install, start, and babysit. If your graph hits millions of edges, that's the
ceiling where a real graph database starts paying rent.

**The graph shows a connection that can't be real.**
Click the edge in `greybeard visualize` — the tooltip lists the exact
tables/paths (and for agent-recorded edges, the file:line evidence) behind
it. The matching rules err toward dropping weak evidence, because a false
edge is worse than a missing one. If a phantom survives, file an issue with
the tooltip contents.

**What doesn't it know?**
Runtime call frequency (all edges weigh the same), undeclared dependencies (a
hardcoded URL in a shell script), repos it hasn't extracted yet, and
semantics — matching is contextual text scanning, not AST analysis. Treat
hits as "worth checking," not proof.

**Why "greybeard"?**
Ask him about light mode.

## License

[MIT](LICENSE).
