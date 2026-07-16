# greybeard — cross-repo context rules

Apply these rules whenever a task in one repo might depend on, be depended on by, or share a schema/API contract with another repo — e.g. changing an endpoint, modifying a shared schema, checking "who else calls this", assessing blast radius of a change, or picking which repo owns a piece of functionality. Also trigger when the user asks about cross-repo dependencies, service relationships, "what depends on X", or wants a broader picture than the single open repo provides. This skill queries a local graph knowledge base via an MCP tool to pull in relationships the current repo alone can't show.

# Cross-repo context

Claude Code normally only sees the repo it's working in. This skill fills that gap: a graph of relationships across all repos (dependency, API-call, schema-sharing, ownership) lives in a local embedded graph store (a single SQLite file), exposed through an MCP server. Consult it before making a change whose impact might reach beyond the current repo.

## When to use this

- Before modifying or removing a public endpoint, exported function, or shared schema
- Before deleting/renaming something that other repos might import or call
- When asked "what depends on X" / "who calls this" / "is this safe to change"
- When picking which repo owns a piece of functionality for a new feature
- When building a session context brief for a repo that has known cross-repo ties

Skip this for changes fully contained within one repo with no exported surface (internal-only functions, private types, test files).

## Keeping the graph fresh

This host has no session hooks, so indexing does not happen automatically here. If a query returns empty for a repo that plausibly has cross-repo ties, its extraction may be missing or stale — suggest running `greybeard build` (full rebuild) or `greybeard check --cwd <repo>` (fast freshness check) in a terminal rather than asserting the repo has no dependencies.

## How to query

Call the MCP tool exposed by the graph server (see the greybeard README (https://github.com/deepaksinghcs14/greybeard) for exact signatures). The three core queries:

1. `get_related_repos(repo, max_hops)` — what repos are connected to this one, and how (import, API call, shared schema)
2. `get_callers_of(endpoint_or_symbol)` — reverse lookup: what calls this specific thing
3. `get_schema_dependents(schema_name)` — what repos read/write a given shared schema

Start with `max_hops=1` unless the task explicitly needs a wider blast-radius check (e.g. "how far does this change propagate" → `max_hops=2` or `3`).

## Interpreting results

Results come back as structured edges (JSON). Before using them:

- Distinguish edge types — `imports` (compile-time dependency) is a harder constraint than `calls_api` (runtime, can be versioned/deprecated gracefully) or `shares_schema` (data contract, breaking changes are silent until runtime).
- If a query returns nothing, that's a real signal the change is locally contained — say so explicitly rather than treating empty results as a query failure.
- Summarize findings in plain language before acting: "connector-platform calls this endpoint from `/outbound/execute`, and orchestrator-svc reads the `runs` table this schema backs" — not a raw JSON dump.

## Acting on findings

- If the change affects another repo, say so before proceeding, and ask whether to also update the dependent repo, open a note/ticket, or proceed with just this repo (their choice — don't assume).
- If the graph shows a caller relationship but you can't access that other repo, flag the specific dependency clearly enough that the user can check it themselves.
- Never silently make a breaking change to something with known dependents.

## If the MCP tool isn't available

The graph server may not be connected in every session. If the tool call fails or isn't in the tool list, say so plainly and offer to proceed without cross-repo awareness, rather than guessing at relationships from memory or repo naming conventions.

See the greybeard README (https://github.com/deepaksinghcs14/greybeard) for full tool signatures and the node/edge model if you need to reason about what the graph does and doesn't capture.
