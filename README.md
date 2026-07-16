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

The old man has been here longer than any of your microservices. He watched
`orders-svc` split off from the monolith. He remembers who still reads that
table you think nobody uses. Ask him before you touch anything.

## The problem

Claude Code and Codex are scoped to whichever repo they're opened in. Neither
can answer *"what depends on this endpoint?"* or *"is it safe to delete this
schema column?"* — the information exists, scattered across `go.mod` files,
OpenAPI specs, proto definitions, and migrations in your other repos, but
nothing extracts and connects it. So your agent makes a locally-correct,
globally-breaking change, and you find out in production.

## What it does

1. **Extract** — walks your repos once, parsing `go.mod` / `package.json`,
   OpenAPI specs, `.proto` files, and SQL migrations into declared surface:
   packages, endpoints, schemas.
2. **Store** — connects them into a typed graph (who imports what, who calls
   what, who reads whose tables) in an embedded SQLite file
   (`~/.greybeard/graph.db`). Nothing to install, start, or babysit.
3. **Serve** — exposes the graph to any MCP-speaking agent over stdio. No
   hosted service, no API keys, no telemetry. Everything stays on your machine.
4. **Answer** — three narrow queries an agent asks before changing anything:
   what's related to this repo, who calls this thing, who depends on this schema.

## Commands

| Command | What it does |
|---|---|
| `greybeard init --root <path>` | Scan a tree for git repos and register every one found |
| `greybeard build` | Full extraction across all registered repos (safe to rerun; not incremental) |
| `greybeard serve` | MCP server over stdio — this is what your agent talks to |
| `greybeard visualize` | Open the graph as an interactive local web page (`--port`, default 7333) |
| `greybeard update` | Self-update to the latest GitHub release |
| `greybeard clean` | Forget all extracted relations (`build` re-creates them); `--all` also unregisters every repo |
| `greybeard check --cwd <path>` | Session-start freshness check: no-ops if the repo is registered and fresh, otherwise queues background extraction and returns immediately |

`check` also self-updates the binary in the background at most once a day, so
agent sessions keep greybeard current without anyone running `update` by hand
(`GREYBEARD_AUTO_UPDATE=off` disables this).

Inside Claude Code, the plugin adds `/greybeard-init`, `/greybeard-build`,
`/greybeard-query`, and `/greybeard-audit` on top of these.

## Install

**1. The binary** — that's the whole setup; storage is an embedded SQLite
file greybeard creates itself. Grab a prebuilt binary from
[Releases](https://github.com/deepaksinghcs14/greybeard/releases):

```sh
# macOS Apple Silicon (see Releases for darwin/linux/windows, amd64/arm64)
sudo curl -L https://github.com/deepaksinghcs14/greybeard/releases/latest/download/greybeard_darwin_arm64 -o /usr/local/bin/greybeard
sudo chmod +x /usr/local/bin/greybeard
```

or build from source if you have Go:

```sh
go install github.com/deepaksinghcs14/greybeard/cmd/greybeard@latest
```

**2. Claude Code:**

```
/plugin marketplace add deepaksinghcs14/greybeard
/plugin install greybeard@greybeard
```

That wires up the skill, the slash commands, the MCP server, and a
session-start hook that keeps the current repo's graph data fresh
automatically.

**3. Codex:** copy the generated skill into Codex's skills directory and
register the MCP server:

```sh
cp -r adapters/codex/greybeard ~/.agents/skills/greybeard
codex mcp add greybeard -- greybeard serve
```

(Older Codex builds read `~/.codex/skills/` instead.)

**Cursor / Windsurf / Cline:** paste `adapters/instruction-only/greybeard.md`
into your always-on rules, and register `greybeard serve` as a stdio MCP
server however your editor does that.

**4. First index:**

```sh
greybeard init --root ~/code
greybeard build
```

## What it looks like

**Without greybeard:**

> **You:** Rename the `total` column in the orders table to `amount`.
>
> **Agent:** Done — updated the migration, the model, and all references in
> this repo. All tests pass. ✅
>
> *(billing-svc's nightly reconciliation, which reads `orders.total` directly,
> breaks silently at 2am.)*

**With greybeard:**

> **You:** Rename the `total` column in the orders table to `amount`.
>
> **Agent:** Before I touch this: the graph shows the `orders` schema has a
> dependent outside this repo — **billing-svc** reads it directly
> (access mode: read). A rename will break its reconciliation queries
> silently. Want me to (a) proceed and flag billing-svc for a follow-up
> change, (b) add a compatibility view, or (c) hold off?

**Without greybeard:**

> **You:** This `POST /orders` endpoint has a weird response shape. Clean it up.
>
> **Agent:** Restructured the response. Nothing in this repo consumed the old
> shape, so it's a safe change. ✅ *(It wasn't.)*

**With greybeard:**

> **You:** This `POST /orders` endpoint has a weird response shape. Clean it up.
>
> **Agent:** `get_callers_of("POST /orders")` shows one caller: **billing-svc**
> (edge type: calls_api — a runtime dependency, so it can be versioned rather
> than broken). I'll keep the old shape working and add the cleaned-up shape
> alongside, unless you'd rather coordinate a breaking change with billing-svc.

## The graph

Nodes:

| Label     | Key fields                              | Populated from                 |
|-----------|------------------------------------------|--------------------------------|
| Repo      | remote_url, local_path, last_indexed_at  | git discovery                  |
| Endpoint  | path, method, repo                       | OpenAPI specs, proto files     |
| Schema    | name, repo                               | migration files, proto messages |
| Package   | import_path, repo                        | go.mod / package.json          |

Edges (every query result carries its edge type — `imports` is a hard
compile-time constraint, `calls_api` is runtime and can be versioned,
`shares_schema` is a data contract that breaks silently):

| Type           | From → To        | Meaning                                  |
|----------------|------------------|-------------------------------------------|
| imports        | Repo → Package   | compile-time dependency                   |
| calls_api      | Repo → Endpoint  | runtime HTTP/gRPC call                    |
| shares_schema  | Repo → Schema    | reads or writes a shared data model       |
| depends_on     | Repo → Repo      | rolled-up summary edge, recomputed each build |

Repo identity is the normalized git remote URL (falling back to absolute path
for remoteless repos), so the same repo cloned twice is one node, not two.
Freshness is per-repo via `last_indexed_at`; the session-start hook re-queues
extraction for anything older than `GREYBEARD_STALE_AFTER` (default `24h`).

## Why embedded SQLite, not a graph database

Because the graph is small and the queries are shallow. A few hundred repos
produce a few thousand edges, and the three agent-facing queries are exact
lookups plus a 1–3 hop walk — no workload that earns a Neo4j (or even a
Postgres + Apache AGE) daemon to install, start, and babysit. SQLite is one
file at `~/.greybeard/graph.db` (override with `GREYBEARD_DB`), created on
first run, inspectable with the `sqlite3` CLI, backed up with `cp`. If your
graph ever hits millions of edges with deep traversals, that's the ceiling
where a real graph database starts paying rent; the storage layer is one
small package if that day comes.

## What greybeard doesn't know

Honesty section. The graph is built from *declared* surface, so it does not
capture:

- **Runtime call frequency or criticality** — all edges weigh the same; the
  once-a-year admin script and the hot path look identical.
- **Undeclared dependencies** — an endpoint hit via a hardcoded URL in a shell
  script, a table read by a BI tool, anything outside the parsed manifests.
- **Repos it hasn't extracted yet** — a gap is "unknown," not "no dependency."
  If a repo was first opened minutes ago, extraction may still be running.
- **Semantic matches** — reference detection is contextual text scanning, not
  per-language AST analysis. A table name only counts next to a SQL keyword
  (`FROM orders`, `JOIN orders`), an endpoint path only on a line with a string
  literal, a proto message only inside `.proto` files. Same-name is not
  same-thing: a name a repo declares itself resolves locally, universal
  endpoints (`/health`, `/metrics`) never link, and a schema-name match links
  on its own only for a distinctive name within one org (same `github.com/org`
  or parent directory) — generic names (`users`, `messages`, ...) and
  cross-org matches both need an existing imports or calls_api edge as
  corroboration. Still text — treat hits as "worth checking," not proof.

## License

[MIT](LICENSE)
