package graph

import (
	"context"
	"os"
	"strings"
	"time"

	"github.com/deepaksinghcs14/greybeard/internal/discover"
	"github.com/deepaksinghcs14/greybeard/internal/extract"
)

// BuildResult summarizes a full graph rebuild.
type BuildResult struct {
	ReposProcessed int           `json:"repos_processed"`
	Nodes          int           `json:"nodes"` // nodes created or confirmed (MERGE)
	Edges          int           `json:"edges"` // edges created or confirmed (MERGE)
	Failed         []RepoFailure `json:"failed,omitempty"`
}

// RepoFailure is a repo whose extraction was skipped, with the reason.
type RepoFailure struct {
	Repo   string `json:"repo"`
	Reason string `json:"reason"`
}

// declared is one repo's extracted surface, held in memory during a build.
type declared struct {
	rec RepoRecord
	ex  extract.Extraction
}

// BuildAll re-extracts every registered repo and fully rebuilds all extracted
// nodes and edges (Repo nodes and their registration survive).
func (s *Store) BuildAll(ctx context.Context) (BuildResult, error) {
	var res BuildResult
	repos, err := s.ListRepos(ctx)
	if err != nil {
		return res, err
	}

	// Extract everything first — cross-referencing needs all repos' surfaces.
	var all []declared
	for _, r := range repos {
		if _, err := os.Stat(r.LocalPath); err != nil {
			res.Failed = append(res.Failed, RepoFailure{Repo: r.Name, Reason: "local path missing: " + r.LocalPath})
			continue
		}
		ex := extract.Repo(r.LocalPath)
		for _, e := range ex.Errors {
			res.Failed = append(res.Failed, RepoFailure{Repo: r.Name, Reason: e})
		}
		all = append(all, declared{rec: r, ex: ex})
	}

	// Full rebuild: drop all extracted nodes (their edges go with them) and
	// the derived depends_on edges.
	for _, q := range []string{
		`MATCH (n:Package) DETACH DELETE n`,
		`MATCH (n:Endpoint) DETACH DELETE n`,
		`MATCH (n:Schema) DETACH DELETE n`,
		`MATCH ()-[e:depends_on]->() DELETE e`,
	} {
		if _, err := s.cypher(ctx, q, 1); err != nil {
			return res, err
		}
	}

	now := time.Now()
	for _, d := range all {
		n, err := s.createDeclared(ctx, d)
		if err != nil {
			return res, err
		}
		res.Nodes += n.nodes
		res.Edges += n.edges
		if err := s.SetIndexed(ctx, d.rec.Identity, now, d.ex.Modules); err != nil {
			return res, err
		}
	}
	for _, d := range all {
		n, err := s.crossRef(ctx, d, others(all, d.rec.Identity))
		if err != nil {
			return res, err
		}
		res.Nodes += n.nodes
		res.Edges += n.edges
	}
	res.ReposProcessed = len(all)
	return res, nil
}

// Reindex re-extracts a single repo (used by the session-start hook).
// ponytail: refreshes this repo's declared nodes and OUTGOING edges only;
// stale inbound edges (other repos calling endpoints this repo deleted) and
// removed declared nodes are reconciled by the next full `greybeard build`.
func (s *Store) Reindex(ctx context.Context, repo discover.Repo) error {
	if err := s.UpsertRepo(ctx, repo); err != nil {
		return err
	}
	ex := extract.Repo(repo.LocalPath)

	// Drop this repo's outgoing edges; MERGE below recreates declared nodes.
	if _, err := s.cypher(ctx, `MATCH (r:Repo {identity: `+lit(repo.Identity)+`})-[e]->() DELETE e`, 1); err != nil {
		return err
	}

	rec, err := s.GetRepo(ctx, repo.Identity)
	if err != nil {
		return err
	}
	d := declared{rec: *rec, ex: ex}
	if _, err := s.createDeclared(ctx, d); err != nil {
		return err
	}

	// Other repos' surfaces come from the graph (declared at their last
	// index), not from re-extracting them.
	repos, err := s.ListRepos(ctx)
	if err != nil {
		return err
	}
	var rest []declared
	for _, r := range repos {
		if r.Identity == repo.Identity {
			continue
		}
		o := declared{rec: r, ex: extract.Extraction{Modules: r.Modules}}
		eps, err := s.endpointsOf(ctx, r.Identity)
		if err != nil {
			return err
		}
		o.ex.Endpoints = eps
		tables, err := s.schemasOf(ctx, r.Identity)
		if err != nil {
			return err
		}
		o.ex.Tables = tables
		rest = append(rest, o)
	}
	if _, err := s.crossRef(ctx, d, rest); err != nil {
		return err
	}
	return s.SetIndexed(ctx, repo.Identity, time.Now(), ex.Modules)
}

func others(all []declared, identity string) []declared {
	var out []declared
	for _, d := range all {
		if d.rec.Identity != identity {
			out = append(out, d)
		}
	}
	return out
}

type counts struct{ nodes, edges int }

// createDeclared writes a repo's own surface: Endpoint nodes, Schema nodes
// (tables and proto messages), and the owner's shares_schema(write) edge.
func (s *Store) createDeclared(ctx context.Context, d declared) (counts, error) {
	var c counts
	id := lit(d.rec.Identity)
	for _, ep := range d.ex.Endpoints {
		q := `MERGE (e:Endpoint {path: ` + lit(ep.Path) + `, method: ` + lit(ep.Method) + `, repo: ` + id + `})`
		if _, err := s.cypher(ctx, q, 1); err != nil {
			return c, err
		}
		c.nodes++
	}
	for _, name := range append(append([]string{}, d.ex.Tables...), d.ex.Messages...) {
		if _, err := s.cypher(ctx, `MERGE (sc:Schema {name: `+lit(name)+`, repo: `+id+`})`, 1); err != nil {
			return c, err
		}
		q := `MATCH (r:Repo {identity: ` + id + `}), (sc:Schema {name: ` + lit(name) + `, repo: ` + id + `})
			MERGE (r)-[:shares_schema {access_mode: 'write'}]->(sc)`
		if _, err := s.cypher(ctx, q, 1); err != nil {
			return c, err
		}
		c.nodes++
		c.edges++
	}
	return c, nil
}

// crossRef scans repo d's sources against every other repo's declared surface
// and writes imports / calls_api / shares_schema edges plus the depends_on
// rollups.
func (s *Store) crossRef(ctx context.Context, d declared, rest []declared) (counts, error) {
	var c counts
	if _, err := os.Stat(d.rec.LocalPath); err != nil {
		return c, nil // nothing to scan
	}

	type epOwner struct {
		owner  string
		method string
	}
	pathOwners := map[string][]epOwner{} // endpoint path -> declaring repos
	wordOwners := map[string][]string{}  // schema/message name -> declaring repos
	for _, o := range rest {
		for _, ep := range o.ex.Endpoints {
			pathOwners[ep.Path] = append(pathOwners[ep.Path], epOwner{owner: o.rec.Identity, method: ep.Method})
		}
		for _, t := range append(append([]string{}, o.ex.Tables...), o.ex.Messages...) {
			wordOwners[t] = append(wordOwners[t], o.rec.Identity)
		}
	}

	// imports: this repo's declared deps vs other repos' module paths.
	for _, dep := range d.ex.Deps {
		for _, o := range rest {
			for _, mod := range o.ex.Modules {
				if mod == "" || (dep != mod && !strings.HasPrefix(dep, mod+"/")) {
					continue
				}
				n, err := s.importEdge(ctx, d.rec.Identity, o.rec.Identity, dep)
				if err != nil {
					return c, err
				}
				c.nodes += n.nodes
				c.edges += n.edges
			}
		}
	}

	var paths, words []string
	for p := range pathOwners {
		paths = append(paths, p)
	}
	for w := range wordOwners {
		words = append(words, w)
	}
	pathHits, wordHits := extract.ScanRefs(d.rec.LocalPath, paths, words)

	from := lit(d.rec.Identity)
	for p := range pathHits {
		for _, eo := range pathOwners[p] {
			detail := eo.method + " " + p
			q := `MATCH (r:Repo {identity: ` + from + `}), (e:Endpoint {path: ` + lit(p) + `, method: ` + lit(eo.method) + `, repo: ` + lit(eo.owner) + `})
				MERGE (r)-[:calls_api {detail: ` + lit(detail) + `}]->(e)`
			if _, err := s.cypher(ctx, q, 1); err != nil {
				return c, err
			}
			c.edges++
			if err := s.dependsOn(ctx, d.rec.Identity, eo.owner, "calls_api", detail); err != nil {
				return c, err
			}
			c.edges++
		}
	}
	for w := range wordHits {
		for _, owner := range wordOwners[w] {
			q := `MATCH (r:Repo {identity: ` + from + `}), (sc:Schema {name: ` + lit(w) + `, repo: ` + lit(owner) + `})
				MERGE (r)-[:shares_schema {access_mode: 'read'}]->(sc)`
			if _, err := s.cypher(ctx, q, 1); err != nil {
				return c, err
			}
			c.edges++
			if err := s.dependsOn(ctx, d.rec.Identity, owner, "shares_schema", w); err != nil {
				return c, err
			}
			c.edges++
		}
	}
	return c, nil
}

func (s *Store) importEdge(ctx context.Context, from, owner, importPath string) (counts, error) {
	var c counts
	if _, err := s.cypher(ctx, `MERGE (p:Package {import_path: `+lit(importPath)+`, repo: `+lit(owner)+`})`, 1); err != nil {
		return c, err
	}
	c.nodes++
	q := `MATCH (r:Repo {identity: ` + lit(from) + `}), (p:Package {import_path: ` + lit(importPath) + `, repo: ` + lit(owner) + `})
		MERGE (r)-[:imports {detail: ` + lit(importPath) + `}]->(p)`
	if _, err := s.cypher(ctx, q, 1); err != nil {
		return c, err
	}
	c.edges++
	if err := s.dependsOn(ctx, from, owner, "imports", importPath); err != nil {
		return c, err
	}
	c.edges++
	return c, nil
}

// dependsOn writes the rolled-up Repo->Repo summary edge, one per underlying
// relation so edge types survive the rollup.
func (s *Store) dependsOn(ctx context.Context, from, to, edgeType, detail string) error {
	q := `MATCH (a:Repo {identity: ` + lit(from) + `}), (b:Repo {identity: ` + lit(to) + `})
		MERGE (a)-[:depends_on {edge_type: ` + lit(edgeType) + `, detail: ` + lit(detail) + `}]->(b)`
	_, err := s.cypher(ctx, q, 1)
	return err
}

// endpointsOf / schemasOf read a repo's declared surface back out of the graph
// (used by single-repo reindex instead of re-extracting every other repo).
func (s *Store) endpointsOf(ctx context.Context, identity string) ([]extract.Endpoint, error) {
	rows, err := s.cypher(ctx, `MATCH (e:Endpoint {repo: `+lit(identity)+`}) RETURN e.method, e.path`, 2)
	if err != nil {
		return nil, err
	}
	eps := make([]extract.Endpoint, 0, len(rows))
	for _, r := range rows {
		eps = append(eps, extract.Endpoint{Method: r[0], Path: r[1]})
	}
	return eps, nil
}

func (s *Store) schemasOf(ctx context.Context, identity string) ([]string, error) {
	rows, err := s.cypher(ctx, `MATCH (sc:Schema {repo: `+lit(identity)+`}) RETURN sc.name`, 1)
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(rows))
	for _, r := range rows {
		names = append(names, r[0])
	}
	return names, nil
}
