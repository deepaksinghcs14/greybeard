package graph

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"
)

// RepoRelation is one typed relationship reachable from a repo.
type RepoRelation struct {
	Repo     string `json:"repo"`
	EdgeType string `json:"edge_type"` // imports | calls_api | shares_schema
	Detail   string `json:"detail"`
	Hops     int    `json:"hops"`
	Source   string `json:"source"` // scanned (extraction) | agent (verified observation)
}

// Caller is a repo that calls or imports a target.
type Caller struct {
	Repo     string `json:"repo"`
	EdgeType string `json:"edge_type"`
	Detail   string `json:"detail"`
	Source   string `json:"source"`
}

// SchemaDependent is a repo that reads or writes a schema.
type SchemaDependent struct {
	Repo        string `json:"repo"`
	AccessMode  string `json:"access_mode"` // read | write | read_write
	TableOrType string `json:"table_or_type"`
}

// GetRelatedRepos walks the depends_on rollup (both directions) up to maxHops
// from the given repo (short name or identity). Errors if the repo isn't
// registered, so "unknown repo" never masquerades as "no dependencies".
func (s *Store) GetRelatedRepos(ctx context.Context, repo string, maxHops int) ([]RepoRelation, error) {
	rec, err := s.GetRepo(ctx, repo)
	if err != nil {
		return nil, err
	}
	if rec == nil {
		return nil, fmt.Errorf("repo %q is not registered in the graph", repo)
	}
	if maxHops < 1 {
		maxHops = 1
	}

	names := map[string]string{} // identity -> short name
	repos, err := s.ListRepos(ctx)
	if err != nil {
		return nil, err
	}
	for _, r := range repos {
		names[r.Identity] = r.Name
	}

	visited := map[string]bool{rec.Identity: true}
	frontier := []string{rec.Identity}
	seen := map[string]bool{} // dedupe (repo, edge_type, detail) across directions
	var out []RepoRelation

	for hop := 1; hop <= maxHops && len(frontier) > 0; hop++ {
		placeholders := strings.TrimSuffix(strings.Repeat("?,", len(frontier)), ",")
		args := make([]any, 0, len(frontier)*2)
		for _, f := range frontier {
			args = append(args, f)
		}
		for _, f := range frontier {
			args = append(args, f)
		}
		rows, err := s.db.QueryContext(ctx, `SELECT from_repo, to_repo, edge_type, detail, source FROM depends_on
			WHERE from_repo IN (`+placeholders+`) OR to_repo IN (`+placeholders+`)`, args...)
		if err != nil {
			return nil, err
		}
		inFrontier := map[string]bool{}
		for _, f := range frontier {
			inFrontier[f] = true
		}
		var next []string
		for rows.Next() {
			var from, to, edgeType, detail, source string
			if err := rows.Scan(&from, &to, &edgeType, &detail, &source); err != nil {
				rows.Close()
				return nil, err
			}
			other := to
			if !inFrontier[from] {
				other = from
			}
			if visited[other] {
				continue // reached in a previous hop, the origin, or an intra-frontier edge
			}
			key := other + "|" + edgeType + "|" + detail
			if !seen[key] {
				seen[key] = true
				out = append(out, RepoRelation{Repo: names[other], EdgeType: edgeType, Detail: detail, Hops: hop, Source: source})
			}
			next = append(next, other)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return nil, err // a truncated hop must not read as "no more edges"
		}
		// Mark visited only after the whole hop so a repo found twice in the
		// same hop still records all its edge types.
		for _, n := range next {
			visited[n] = true
		}
		frontier = dedupeStrings(next)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Hops != out[j].Hops {
			return out[i].Hops < out[j].Hops
		}
		return out[i].Repo < out[j].Repo
	})
	return out, nil
}

// GetCallersOf finds repos that call an endpoint ("POST /orders", "/orders",
// "OrderService/Create") or import a package/symbol path.
func (s *Store) GetCallersOf(ctx context.Context, target string) ([]Caller, error) {
	method, path := "", strings.TrimSpace(target)
	if m, p, ok := strings.Cut(path, " "); ok && isHTTPMethod(m) {
		method, path = strings.ToUpper(m), strings.TrimSpace(p)
	}

	q := `SELECT r.name, e.detail, e.source FROM edges e JOIN repos r ON r.identity = e.from_repo
		WHERE e.edge_type = 'calls_api' AND e.path = ?`
	args := []any{path}
	if method != "" {
		q += ` AND e.method = ?`
		args = append(args, method)
	}
	var out []Caller
	if err := s.collectCallers(ctx, &out, "calls_api", q, args...); err != nil {
		return nil, err
	}

	// imports: exact package match or a subpackage of the target.
	err := s.collectCallers(ctx, &out, "imports",
		`SELECT r.name, e.detail, e.source FROM edges e JOIN repos r ON r.identity = e.from_repo
		 WHERE e.edge_type = 'imports' AND (e.detail = ?1 OR e.detail LIKE ?1 || '/%')`, target)
	if err != nil {
		return nil, err
	}
	return out, nil
}

// collectCallers must not run other queries while its rows are open — the
// store holds a single SQLite connection, so a nested query deadlocks.
func (s *Store) collectCallers(ctx context.Context, out *[]Caller, edgeType, query string, args ...any) error {
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var name, detail, source string
		if err := rows.Scan(&name, &detail, &source); err != nil {
			return err
		}
		*out = append(*out, Caller{Repo: name, EdgeType: edgeType, Detail: detail, Source: source})
	}
	return rows.Err()
}

// GetSchemaDependents finds repos that read/write a schema by name. A repo
// that both defines (write) and references (read) it reports read_write.
func (s *Store) GetSchemaDependents(ctx context.Context, schema string) ([]SchemaDependent, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT r.name, e.access_mode, e.detail
		FROM edges e JOIN repos r ON r.identity = e.from_repo
		WHERE e.edge_type = 'shares_schema' AND e.detail = ?`, schema)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	modes := map[string]map[string]bool{} // repo -> set of modes
	for rows.Next() {
		var repo, mode, name string
		if err := rows.Scan(&repo, &mode, &name); err != nil {
			return nil, err
		}
		if modes[repo] == nil {
			modes[repo] = map[string]bool{}
		}
		modes[repo][mode] = true
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	var out []SchemaDependent
	for repo, ms := range modes {
		mode := "read"
		switch {
		case ms["read_write"] || (ms["read"] && ms["write"]):
			mode = "read_write"
		case ms["write"]:
			mode = "write"
		}
		out = append(out, SchemaDependent{Repo: repo, AccessMode: mode, TableOrType: schema})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Repo < out[j].Repo })
	return out, nil
}

// AuditResult is the read-only graph health report.
type AuditResult struct {
	TotalRepos int         `json:"total_repos"`
	EmptyRepos []string    `json:"empty_repos"` // registered but nothing extracted
	StaleRepos []StaleRepo `json:"stale_repos"` // extracted data older than the threshold
}

// StaleRepo names a repo whose extracted rows predate the freshness threshold
// (edges carry no timestamps of their own — a repo's last_indexed_at governs
// the age of everything extracted from it).
type StaleRepo struct {
	Repo          string `json:"repo"`
	LastIndexedAt string `json:"last_indexed_at,omitempty"` // "" = never indexed
}

// Audit is read-only: it inspects the graph and never mutates it.
func (s *Store) Audit(ctx context.Context, staleAfter time.Duration) (AuditResult, error) {
	res := AuditResult{EmptyRepos: []string{}, StaleRepos: []StaleRepo{}}
	repos, err := s.ListRepos(ctx)
	if err != nil {
		return res, err
	}
	res.TotalRepos = len(repos)
	for _, r := range repos {
		var total int
		err := s.db.QueryRowContext(ctx, `SELECT
			(SELECT count(*) FROM endpoints WHERE repo = ?1) +
			(SELECT count(*) FROM schemas   WHERE repo = ?1) +
			(SELECT count(*) FROM packages  WHERE repo = ?1) +
			(SELECT count(*) FROM edges     WHERE from_repo = ?1)`, r.Identity).Scan(&total)
		if err != nil {
			return res, err
		}
		if total == 0 {
			res.EmptyRepos = append(res.EmptyRepos, r.Name)
		}
		if r.Stale(staleAfter) {
			res.StaleRepos = append(res.StaleRepos, StaleRepo{Repo: r.Name, LastIndexedAt: r.LastIndexedAt})
		}
	}
	return res, nil
}

func isHTTPMethod(s string) bool {
	switch strings.ToUpper(s) {
	case "GET", "POST", "PUT", "PATCH", "DELETE", "HEAD", "OPTIONS", "GRPC":
		return true
	}
	return false
}

func dedupeStrings(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range in {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}
