package graph

import (
	"context"
	"fmt"
	"os"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/deepaksinghcs14/greybeard/internal/discover"
	"github.com/deepaksinghcs14/greybeard/internal/extract"
)

// BuildResult summarizes a full graph rebuild.
type BuildResult struct {
	ReposProcessed int           `json:"repos_processed"`
	Nodes          int           `json:"nodes"`
	Edges          int           `json:"edges"`
	Failed         []RepoFailure `json:"failed,omitempty"`
}

// RepoFailure is a repo whose extraction was skipped or partial, with the reason.
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
// nodes and edges (repo registration survives). progress, if non-nil, receives
// a human-readable line as each repo finishes a phase — plain text, ✓/✗
// prefixed; the CLI adds color.
func (s *Store) BuildAll(ctx context.Context, progress func(string)) (BuildResult, error) {
	if progress == nil {
		progress = func(string) {}
	}
	var res BuildResult
	repos, err := s.ListRepos(ctx)
	if err != nil {
		return res, err
	}
	progress(fmt.Sprintf("extracting %d repos…", len(repos)))

	// Extract everything first — cross-referencing needs all repos' surfaces.
	// Extraction is a disk walk per repo, so run them in parallel; the wall
	// clock of a build is dominated by these walks, not by SQLite.
	var (
		all []declared
		mu  sync.Mutex
		wg  sync.WaitGroup
		sem = make(chan struct{}, runtime.NumCPU())
	)
	for _, r := range repos {
		wg.Add(1)
		go func(r RepoRecord) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			if _, err := os.Stat(r.LocalPath); err != nil {
				mu.Lock()
				res.Failed = append(res.Failed, RepoFailure{Repo: r.Name, Reason: "local path missing: " + r.LocalPath})
				mu.Unlock()
				progress("✗ " + r.Name + " — local path missing: " + r.LocalPath)
				return
			}
			start := time.Now()
			ex := extract.Repo(r.LocalPath)
			progress(fmt.Sprintf("✓ %s — %d endpoints · %d schemas · %d deps (%s)",
				r.Name, len(ex.Endpoints), len(ex.Tables)+len(ex.Messages), len(ex.Deps),
				time.Since(start).Round(time.Millisecond)))
			mu.Lock()
			for _, e := range ex.Errors {
				res.Failed = append(res.Failed, RepoFailure{Repo: r.Name, Reason: e})
				progress("✗ " + r.Name + " — " + e)
			}
			all = append(all, declared{rec: r, ex: ex})
			mu.Unlock()
		}(r)
	}
	wg.Wait()

	// Full rebuild: drop all extracted rows.
	for _, table := range []string{"endpoints", "schemas", "packages", "edges"} {
		if _, err := s.db.ExecContext(ctx, "DELETE FROM "+table); err != nil {
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
	// Cross-referencing re-walks each repo's sources (the other slow phase);
	// same parallel fan-out. The single DB connection serializes the writes.
	progress("cross-referencing…")
	var firstErr error
	for _, d := range all {
		wg.Add(1)
		go func(d declared) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			start := time.Now()
			n, err := s.crossRef(ctx, d, others(all, d.rec.Identity))
			mu.Lock()
			defer mu.Unlock()
			if err != nil && firstErr == nil {
				firstErr = err
				return
			}
			if n.edges > 0 {
				progress(fmt.Sprintf("✓ %s — %d cross-repo edges (%s)",
					d.rec.Name, n.edges, time.Since(start).Round(time.Millisecond)))
			}
			res.Nodes += n.nodes
			res.Edges += n.edges
		}(d)
	}
	wg.Wait()
	if firstErr != nil {
		return res, firstErr
	}
	res.ReposProcessed = len(all)
	return res, nil
}

// Reindex re-extracts a single repo (used by the session-start hook).
// ponytail: refreshes this repo's declared rows and OUTGOING edges only;
// stale inbound edges (other repos calling endpoints this repo deleted) and
// removed declared rows are reconciled by the next full `greybeard build`.
func (s *Store) Reindex(ctx context.Context, repo discover.Repo) error {
	if err := s.UpsertRepo(ctx, repo); err != nil {
		return err
	}
	ex := extract.Repo(repo.LocalPath)

	if _, err := s.db.ExecContext(ctx, `DELETE FROM edges WHERE from_repo = ?`, repo.Identity); err != nil {
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

	// Other repos' surfaces come from the store (declared at their last
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
		if o.ex.Endpoints, err = s.endpointsOf(ctx, r.Identity); err != nil {
			return err
		}
		// ponytail: the schemas table stores tables and proto messages
		// undistinguished, so reindex treats them all as tables (SQL-context
		// matching). Proto-message links refresh on the next full build.
		if o.ex.Tables, err = s.schemasOf(ctx, r.Identity); err != nil {
			return err
		}
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

// insert runs an INSERT OR IGNORE and reports whether a row was added.
func (s *Store) insert(ctx context.Context, query string, args ...any) (bool, error) {
	r, err := s.db.ExecContext(ctx, query, args...)
	if err != nil {
		return false, err
	}
	n, _ := r.RowsAffected()
	return n > 0, nil
}

// createDeclared writes a repo's own surface: endpoints, schemas (tables and
// proto messages), and the owner's shares_schema(write) edge.
func (s *Store) createDeclared(ctx context.Context, d declared) (counts, error) {
	var c counts
	id := d.rec.Identity
	for _, ep := range d.ex.Endpoints {
		ok, err := s.insert(ctx, `INSERT OR IGNORE INTO endpoints (repo, method, path) VALUES (?, ?, ?)`,
			id, ep.Method, ep.Path)
		if err != nil {
			return c, err
		}
		if ok {
			c.nodes++
		}
	}
	for _, name := range append(append([]string{}, d.ex.Tables...), d.ex.Messages...) {
		ok, err := s.insert(ctx, `INSERT OR IGNORE INTO schemas (repo, name) VALUES (?, ?)`, id, name)
		if err != nil {
			return c, err
		}
		if ok {
			c.nodes++
		}
		ok, err = s.insert(ctx, `INSERT OR IGNORE INTO edges (from_repo, edge_type, to_repo, detail, access_mode)
			VALUES (?, 'shares_schema', ?, ?, 'write')`, id, id, name)
		if err != nil {
			return c, err
		}
		if ok {
			c.edges++
		}
	}
	return c, nil
}

// crossRef scans repo d's sources against every other repo's declared surface
// and writes imports / calls_api / shares_schema edges.
func (s *Store) crossRef(ctx context.Context, d declared, rest []declared) (counts, error) {
	var c counts
	if _, err := os.Stat(d.rec.LocalPath); err != nil {
		return c, nil // nothing to scan
	}

	type epOwner struct{ owner, method string }
	pathOwners := map[string][]epOwner{} // endpoint path -> declaring repos
	tableOwners := map[string][]string{} // table name -> declaring repos
	msgOwners := map[string][]string{}   // proto message -> declaring repos
	for _, o := range rest {
		for _, ep := range o.ex.Endpoints {
			pathOwners[ep.Path] = append(pathOwners[ep.Path], epOwner{owner: o.rec.Identity, method: ep.Method})
		}
		for _, t := range o.ex.Tables {
			tableOwners[t] = append(tableOwners[t], o.rec.Identity)
		}
		for _, m := range o.ex.Messages {
			msgOwners[m] = append(msgOwners[m], o.rec.Identity)
		}
	}

	// imports: this repo's declared deps vs other repos' module paths.
	for _, dep := range d.ex.Deps {
		for _, o := range rest {
			for _, mod := range o.ex.Modules {
				if mod == "" || (dep != mod && !strings.HasPrefix(dep, mod+"/")) {
					continue
				}
				ok, err := s.insert(ctx, `INSERT OR IGNORE INTO packages (repo, import_path) VALUES (?, ?)`,
					o.rec.Identity, dep)
				if err != nil {
					return c, err
				}
				if ok {
					c.nodes++
				}
				ok, err = s.insert(ctx, `INSERT OR IGNORE INTO edges (from_repo, edge_type, to_repo, detail)
					VALUES (?, 'imports', ?, ?)`, d.rec.Identity, o.rec.Identity, dep)
				if err != nil {
					return c, err
				}
				if ok {
					c.edges++
				}
			}
		}
	}

	var paths, tables, msgs []string
	for p := range pathOwners {
		paths = append(paths, p)
	}
	for t := range tableOwners {
		tables = append(tables, t)
	}
	for m := range msgOwners {
		msgs = append(msgs, m)
	}
	pathHits, tableHits, msgHits := extract.ScanRefs(d.rec.LocalPath, paths, tables, msgs)
	wordHits := map[string]bool{}
	wordOwners := map[string][]string{}
	for t := range tableHits {
		wordHits[t] = true
		wordOwners[t] = tableOwners[t]
	}
	for m := range msgHits {
		wordHits[m] = true
		wordOwners[m] = append(wordOwners[m], msgOwners[m]...)
	}

	for p := range pathHits {
		for _, eo := range pathOwners[p] {
			ok, err := s.insert(ctx, `INSERT OR IGNORE INTO edges (from_repo, edge_type, to_repo, detail, method, path)
				VALUES (?, 'calls_api', ?, ?, ?, ?)`,
				d.rec.Identity, eo.owner, eo.method+" "+p, eo.method, p)
			if err != nil {
				return c, err
			}
			if ok {
				c.edges++
			}
		}
	}
	for w := range wordHits {
		for _, owner := range wordOwners[w] {
			ok, err := s.insert(ctx, `INSERT OR IGNORE INTO edges (from_repo, edge_type, to_repo, detail, access_mode)
				VALUES (?, 'shares_schema', ?, ?, 'read')`, d.rec.Identity, owner, w)
			if err != nil {
				return c, err
			}
			if ok {
				c.edges++
			}
		}
	}
	return c, nil
}

// endpointsOf / schemasOf read a repo's declared surface back out of the
// store (used by single-repo reindex instead of re-extracting every repo).
func (s *Store) endpointsOf(ctx context.Context, identity string) ([]extract.Endpoint, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT method, path FROM endpoints WHERE repo = ?`, identity)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var eps []extract.Endpoint
	for rows.Next() {
		var ep extract.Endpoint
		if err := rows.Scan(&ep.Method, &ep.Path); err != nil {
			return nil, err
		}
		eps = append(eps, ep)
	}
	return eps, rows.Err()
}

func (s *Store) schemasOf(ctx context.Context, identity string) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT name FROM schemas WHERE repo = ?`, identity)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var names []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			return nil, err
		}
		names = append(names, n)
	}
	return names, rows.Err()
}
