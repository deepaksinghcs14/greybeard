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
	EdgeType string `json:"edge_type"` // imports | calls_api | shares_schema | depends_on
	Detail   string `json:"detail"`
	Hops     int    `json:"hops"`
}

// Caller is a repo that calls or imports a target.
type Caller struct {
	Repo     string `json:"repo"`
	EdgeType string `json:"edge_type"`
	Detail   string `json:"detail"`
}

// SchemaDependent is a repo that reads or writes a schema.
type SchemaDependent struct {
	Repo        string `json:"repo"`
	AccessMode  string `json:"access_mode"` // read | write | read_write
	TableOrType string `json:"table_or_type"`
}

// GetRelatedRepos walks depends_on edges (both directions) up to maxHops from
// the given repo (short name or identity). Errors if the repo isn't
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

	visited := map[string]bool{rec.Identity: true}
	frontier := []string{rec.Identity}
	seen := map[string]bool{} // dedupe (repo, edge_type, detail) across directions
	var out []RepoRelation

	for hop := 1; hop <= maxHops && len(frontier) > 0; hop++ {
		lits := make([]string, len(frontier))
		for i, f := range frontier {
			lits[i] = lit(f)
		}
		q := `MATCH (a:Repo)-[d:depends_on]-(b:Repo)
			WHERE a.identity IN [` + strings.Join(lits, ", ") + `]
			RETURN b.identity, b.name, d.edge_type, d.detail`
		rows, err := s.cypher(ctx, q, 4)
		if err != nil {
			return nil, err
		}
		var next []string
		for _, r := range rows {
			bIdent, bName, edgeType, detail := r[0], r[1], r[2], r[3]
			if visited[bIdent] {
				continue // reached in a previous hop (or the origin itself)
			}
			key := bIdent + "|" + edgeType + "|" + detail
			if !seen[key] {
				seen[key] = true
				out = append(out, RepoRelation{Repo: bName, EdgeType: edgeType, Detail: detail, Hops: hop})
			}
			next = append(next, bIdent)
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

	cond := `e.path = ` + lit(path)
	if method != "" {
		cond += ` AND e.method = ` + lit(method)
	}
	rows, err := s.cypher(ctx, `MATCH (r:Repo)-[c:calls_api]->(e:Endpoint)
		WHERE `+cond+` RETURN r.name, c.detail`, 2)
	if err != nil {
		return nil, err
	}
	var out []Caller
	for _, r := range rows {
		out = append(out, Caller{Repo: r[0], EdgeType: "calls_api", Detail: r[1]})
	}

	rows, err = s.cypher(ctx, `MATCH (r:Repo)-[i:imports]->(p:Package)
		WHERE p.import_path = `+lit(target)+` OR p.import_path STARTS WITH `+lit(target+"/")+`
		RETURN r.name, p.import_path`, 2)
	if err != nil {
		return nil, err
	}
	for _, r := range rows {
		out = append(out, Caller{Repo: r[0], EdgeType: "imports", Detail: r[1]})
	}
	return out, nil
}

// GetSchemaDependents finds repos that read/write a schema by name. A repo
// that both defines (write) and references (read) it reports read_write.
func (s *Store) GetSchemaDependents(ctx context.Context, schema string) ([]SchemaDependent, error) {
	rows, err := s.cypher(ctx, `MATCH (r:Repo)-[e:shares_schema]->(sc:Schema)
		WHERE sc.name = `+lit(schema)+` RETURN r.name, e.access_mode, sc.name`, 3)
	if err != nil {
		return nil, err
	}
	modes := map[string]map[string]bool{} // repo -> set of modes
	names := map[string]string{}
	for _, r := range rows {
		if modes[r[0]] == nil {
			modes[r[0]] = map[string]bool{}
		}
		modes[r[0]][r[1]] = true
		names[r[0]] = r[2]
	}
	var out []SchemaDependent
	for repo, ms := range modes {
		mode := "read"
		switch {
		case ms["read"] && ms["write"]:
			mode = "read_write"
		case ms["write"]:
			mode = "write"
		}
		out = append(out, SchemaDependent{Repo: repo, AccessMode: mode, TableOrType: names[repo]})
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

// StaleRepo names a repo whose extracted nodes/edges predate the freshness
// threshold (edges carry no timestamps of their own — a repo's
// last_indexed_at governs the age of everything extracted from it).
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
		total := 0
		for _, label := range []string{"Endpoint", "Schema", "Package"} {
			rows, err := s.cypher(ctx, `MATCH (n:`+label+` {repo: `+lit(r.Identity)+`}) RETURN count(n)`, 1)
			if err != nil {
				return res, err
			}
			total += atoi(rows[0][0])
		}
		rows, err := s.cypher(ctx, `MATCH (a:Repo {identity: `+lit(r.Identity)+`})-[e]->() RETURN count(e)`, 1)
		if err != nil {
			return res, err
		}
		total += atoi(rows[0][0])
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

func atoi(s string) int {
	n := 0
	fmt.Sscanf(s, "%d", &n)
	return n
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
