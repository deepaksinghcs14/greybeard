# Graph MCP tool signatures

Assumes an MCP server (Go) backed by an embedded SQLite store.

All three query tools return `{ results: T[], caveat?: string }`, not a bare
array. **Check `caveat` before reading an empty `results` as "confirmed no
ties."** It's only set when part of the graph this query touches hasn't
been extracted yet or is stale — in that case empty means unknown, not
absent (README/FAQ: extraction gaps are "unknown," never "no dependency").
No `caveat` field at all (or empty) means the answer is trustworthy as-is.

## get_related_repos

```
get_related_repos(repo: string, max_hops: int = 1) -> { results: RepoRelation[], caveat?: string }

RepoRelation {
  repo:        string   // related repo name
  local_path:  string   // the related repo's checkout on this machine — where a coordinated change goes
  edge_type:   string   // "imports" | "calls_api" | "shares_schema" | "calls_symbol"
  detail:      string   // e.g. package path, endpoint, schema/table name
  hops:        int
  source:      string   // "scanned" (extraction) | "agent" (verified observation)
  evidence:    string   // agent edges only: the file:line/snippet cited at record_relation time
}

// caveat here checks the ONE repo you passed in: set if it has never been
// extracted, or its extraction is stale.
```

## get_callers_of

```
get_callers_of(target: string) -> { results: Caller[], caveat?: string }

// target can be an endpoint ("POST /connectors/execute"), a package path,
// or an exported symbol — a top-level function/type/class name
// ("ParseConfig"). Symbol matching is word-boundary text scanning (not
// AST/semantic), same corroboration rules as shares_schema: a generic name
// only links if there's a harder edge (imports/calls_api) to the same repo,
// or it's a distinctive name within the same org.

Caller {
  repo:        string
  local_path:  string   // the caller's checkout on this machine — where a coordinated change goes
  edge_type:   string   // "calls_api" | "imports" | "calls_symbol"
  detail:      string
  source:      string
  evidence:    string   // agent edges only
}

// caveat here is graph-wide (target isn't tied to one repo): set if ANY
// registered repo is stale or was never extracted.
```

## get_schema_dependents

```
get_schema_dependents(schema: string) -> { results: SchemaDependent[], caveat?: string }

SchemaDependent {
  repo:        string
  local_path:  string   // the dependent's checkout on this machine — where a coordinated change goes
  access_mode: string   // "read" | "write" | "read_write"
  table_or_type: string
  source:      string   // if both a scanned and an agent edge exist for this repo, agent wins
  evidence:    string   // agent edges only
}

// caveat: same graph-wide check as get_callers_of.
```

## record_relation

\`\`\`
record_relation(from: string, to: string, edge_type: string,
                detail: string, access_mode?: string, evidence: string)

// Store a cross-repo relationship you verified in code that extraction
// can't see. edge_type: "imports" | "calls_api" | "shares_schema" | "calls_symbol".
// detail: import path, "POST /orders", table name, or exported symbol name.
// evidence: file:line and/or snippet — required. Recorded edges carry
// source="agent" and survive rebuilds.
\`\`\`

## init_root / build_graph / audit_graph / visualize_graph

These back the CLI/slash commands, not agent-facing reasoning queries.
`visualize_graph` starts (or finds) the local interactive graph page and
returns its URL — relay it to the user.

## Query cost note

`max_hops` beyond 2-3 mostly adds noise on dense graphs — transitive edges pile up fast. Default to 1 hop; only widen when the task explicitly needs a broader blast-radius check.
