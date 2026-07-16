# Graph MCP tool signatures

Assumes an MCP server (Go) backed by an embedded SQLite store.

## get_related_repos

```
get_related_repos(repo: string, max_hops: int = 1) -> RepoRelation[]

RepoRelation {
  repo:        string   // related repo name
  edge_type:   string   // "imports" | "calls_api" | "shares_schema" | "depends_on"
  detail:      string   // e.g. package path, endpoint, schema/table name
  hops:        int
}
```

## get_callers_of

```
get_callers_of(target: string) -> Caller[]

// target can be an endpoint ("POST /connectors/execute"), an exported
// symbol, or a package path.

Caller {
  repo:        string
  edge_type:   string
  detail:      string
}
```

## get_schema_dependents

```
get_schema_dependents(schema: string) -> SchemaDependent[]

SchemaDependent {
  repo:        string
  access_mode: string   // "read" | "write" | "read_write"
  table_or_type: string
}
```

## record_relation

\`\`\`
record_relation(from: string, to: string, edge_type: string,
                detail: string, access_mode?: string, evidence: string)

// Store a cross-repo relationship you verified in code that extraction
// can't see. edge_type: "imports" | "calls_api" | "shares_schema".
// detail: import path, "POST /orders", or table name. evidence: file:line
// and/or snippet — required. Recorded edges carry source="agent" and
// survive rebuilds.
\`\`\`

## init_root / build_graph / audit_graph / visualize_graph

These back the CLI/slash commands, not agent-facing reasoning queries.
`visualize_graph` starts (or finds) the local interactive graph page and
returns its URL — relay it to the user.

## Query cost note

`max_hops` beyond 2-3 mostly adds noise on dense graphs — transitive edges pile up fast. Default to 1 hop; only widen when the task explicitly needs a broader blast-radius check.
