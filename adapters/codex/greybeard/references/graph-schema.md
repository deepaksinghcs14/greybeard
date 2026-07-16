# Graph schema

## Nodes

| Label     | Key fields                          | Populated from                        |
|-----------|--------------------------------------|----------------------------------------|
| Repo      | remote_url, local_path, last_indexed_at | git discovery                       |
| Endpoint  | path, method, repo                   | OpenAPI specs, proto files             |
| Schema    | name, repo                           | migration files, proto messages        |
| Package   | import_path, repo                    | go.mod / package.json                  |

## Edges

| Type           | From -> To          | Meaning                                   | Source                          |
|----------------|---------------------|--------------------------------------------|----------------------------------|
| imports        | Repo -> Package     | compile-time dependency                    | go.mod / package.json diff       |
| calls_api      | Repo -> Endpoint    | runtime HTTP/gRPC call                     | OpenAPI/proto cross-refs, code scan |
| shares_schema  | Repo -> Schema      | reads or writes a shared data model        | migration/proto parsing          |
| depends_on     | Repo -> Repo        | rolled-up summary edge (any of the above)  | derived, recomputed each build   |

## Update mechanism

Extraction runs on demand (`greybeard build`), via a post-merge CI hook if the user wires one up, and via the session-start auto-index described below.

## Repo discovery and identity

Repos enter the graph two ways:

1. **Root scan** (`greybeard init --root <path>` / `/greybeard-init`) — walks the given tree for `.git` directories, registers every repo found in one pass. Run once per root; rerun only when adding a new root folder.
2. **Session-start auto-index** — a hook fires whenever an agent (Claude Code or Codex) opens a session in a repo. It checks whether that repo is already registered and fresh; if not, it registers it and queues extraction in the background, non-blocking. This catches anything outside the scanned root, or anything cloned after the last scan.

**Identity key**: normalized git remote URL, when one exists. Falls back to absolute path for repos with no remote. This means the same repo cloned to two different paths, or on two different machines, resolves to one graph node rather than duplicating it.

**Freshness**: each `Repo` node carries a `last_indexed_at` timestamp. The session-start hook treats anything older than a configurable threshold (default 24h, `GREYBEARD_STALE_AFTER` env var) as stale and re-queues extraction, same as a brand-new repo.

## What this graph does NOT capture

- Runtime call frequency or criticality (all edges are equally weighted)
- Non-declared dependencies (e.g. an endpoint called via a hardcoded URL string in tooling scripts)
- Anything not yet extracted for a given repo — treat gaps as "unknown," not "no dependency"
