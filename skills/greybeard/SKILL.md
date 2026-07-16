---
name: greybeard
description: Use this skill whenever a task in one repo might depend on, be depended on by, or share a schema/API contract with another repo — e.g. changing an endpoint, modifying a shared schema, checking "who else calls this", assessing blast radius of a change, or picking which repo owns a piece of functionality. Also trigger when the user asks about cross-repo dependencies, service relationships, "what depends on X", or wants a broader picture than the single open repo provides. This skill queries a local graph knowledge base via an MCP tool to pull in relationships the current repo alone can't show.
---

# Cross-repo context

Claude Code normally only sees the repo it's working in. This skill fills that gap: a graph of relationships across all repos (dependency, API-call, schema-sharing, ownership) lives in a local embedded graph store (a single SQLite file), exposed through an MCP server. Consult it before making a change whose impact might reach beyond the current repo.

## When to use this

- Before modifying or removing a public endpoint, exported function, or shared schema
- Before deleting/renaming something that other repos might import or call
- When asked "what depends on X" / "who calls this" / "is this safe to change"
- When picking which repo owns a piece of functionality for a new feature
- When building a session context brief for a repo that has known cross-repo ties

Skip this for changes fully contained within one repo with no exported surface (internal-only functions, private types, test files).

## You don't need to trigger indexing yourself

A session-start hook checks whether the current repo is registered and fresh before you're even asked to do anything, and queues extraction in the background if not. If a query comes back empty for a repo that was only just opened for the first time, that may mean extraction is still running rather than "no dependencies exist" — say so rather than asserting the repo has no cross-repo ties. See `references/graph-schema.md` for the discovery/freshness rules.

## How to query

Call the MCP tool exposed by the graph server (see `references/mcp-tools.md` for exact signatures). The three core queries:

1. `get_related_repos(repo, max_hops)` — what repos are connected to this one, and how (import, API call, shared schema)
2. `get_callers_of(endpoint_or_symbol)` — reverse lookup: what calls this specific thing
3. `get_schema_dependents(schema_name)` — what repos read/write a given shared schema

Start with `max_hops=1` unless the task explicitly needs a wider blast-radius check (e.g. "how far does this change propagate" → `max_hops=2` or `3`).

## Interpreting results

Each query tool returns `{ results: [...], caveat?: "..." }`, not a bare array. Before using the results:

- Check `caveat` first. It's only set when the graph itself has a gap that could explain an empty (or partial) result — the repo you queried was never extracted, is stale, or (for `get_callers_of`/`get_schema_dependents`, which aren't anchored on one repo) some registered repo overall hasn't been indexed. When `caveat` is set, an empty `results` means unknown, not confirmed absent — say that explicitly, don't report "no dependencies."
- If `results` is empty and there's no `caveat`, that's a real signal the change is locally contained — say so explicitly rather than treating it as a query failure.
- Distinguish edge types — `imports` (compile-time dependency) is a harder constraint than `calls_api` (runtime, can be versioned/deprecated gracefully), `shares_schema` (data contract, breaking changes are silent until runtime), or `calls_symbol` (a scanned reference to an exported function/type/class — weakest signal of the four, since renaming or removing it is a compile/import error in the caller, but the *match itself* is a name-based text scan, not semantic).
- Summarize findings in plain language before acting: "connector-platform calls this endpoint from `/outbound/execute`, and orchestrator-svc reads the `runs` table this schema backs" — not a raw JSON dump.

## Acting on findings

- If the change affects another repo, say so before proceeding, and ask whether to also update the dependent repo, open a note/ticket, or proceed with just this repo (their choice — don't assume).
- If the graph shows a caller relationship but you can't access that other repo, flag the specific dependency clearly enough that the user can check it themselves.
- Never silently make a breaking change to something with known dependents.

## Teaching the graph what you see

Extraction is text-level and misses relationships you can verify while working — a URL assembled from config, an ORM writing a table with no raw SQL. When you *confirm* such a cross-repo relationship in code, record it with `record_relation` (from, to, edge_type, detail, evidence — cite the file:line you saw). Rules:

- Only record what you verified in the code in front of you — never inference from naming, docs, or memory. A false edge poisons every future blast-radius answer.
- Results carry a `source` field: `scanned` edges come from extraction, `agent` edges from recorded observations. Weigh them accordingly when summarizing.

## If the MCP tool isn't available

The graph server may not be connected in every session. If the tool call fails or isn't in the tool list, say so plainly and offer to proceed without cross-repo awareness, rather than guessing at relationships from memory or repo naming conventions. The plugin normally bootstraps the binary automatically on first use (macOS/Linux), so a persistent failure usually means the download was blocked or the platform is Windows — offer the manual fix: `go install github.com/deepaksinghcs14/greybeard/cmd/greybeard@latest` (or a prebuilt binary from the project's GitHub Releases), then restart the session.

See `references/mcp-tools.md` for full tool signatures and `references/graph-schema.md` for the node/edge model if you need to reason about what the graph does and doesn't capture.
