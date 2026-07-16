package graph

import (
	"context"
	"database/sql"
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

// refPlan is one repo's computed outgoing references — pure data, produced by
// the parallel scan phase and applied later inside a transaction.
type refPlan struct {
	from    string // repo identity
	imports []importRef
	calls   []callRef
	schemas []schemaRef
}
type importRef struct{ owner, dep string }
type callRef struct{ owner, method, path string }
type schemaRef struct{ owner, name, mode string }

func (p refPlan) count() int { return len(p.imports) + len(p.calls) + len(p.schemas) }

// dbtx lets the write helpers run against either the pool or a transaction.
type dbtx interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

// BuildAll re-extracts every registered repo and rebuilds all extracted rows.
// Extraction and cross-reference scanning run in parallel (they're disk walks
// and dominate wall clock); every write happens in ONE transaction at the end,
// with last_indexed_at stamped inside it — a failed or killed build leaves the
// previous graph intact, never a half-rebuilt one that reads as fresh.
// progress, if non-nil, receives a human-readable line per repo per phase.
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

	// Phase 1 (parallel, no DB): extract every repo with a present checkout.
	var (
		scanned []declared
		missing []RepoRecord
		mu      sync.Mutex
		wg      sync.WaitGroup
		sem     = make(chan struct{}, runtime.NumCPU())
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
				missing = append(missing, r)
				mu.Unlock()
				progress("✗ " + r.Name + " — local path missing, keeping its stored surface")
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
			scanned = append(scanned, declared{rec: r, ex: ex})
			mu.Unlock()
		}(r)
	}
	wg.Wait()

	// Missing repos keep their stored surface: they still take part in
	// cross-referencing (as targets) and their rows are not deleted.
	var stored []declared
	for _, r := range missing {
		d, err := s.storedSurface(ctx, r)
		if err != nil {
			return res, err
		}
		stored = append(stored, d)
	}
	context_ := append(append([]declared{}, scanned...), stored...)

	// Phase 2 (parallel, no DB): compute each scanned repo's outgoing refs.
	progress("cross-referencing…")
	plans := make([]refPlan, len(scanned))
	for i, d := range scanned {
		wg.Add(1)
		go func(i int, d declared) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			start := time.Now()
			plans[i] = computeRefs(d, others(context_, d.rec.Identity))
			if n := plans[i].count(); n > 0 {
				progress(fmt.Sprintf("✓ %s — %d cross-repo refs (%s)",
					d.rec.Name, n, time.Since(start).Round(time.Millisecond)))
			}
		}(i, d)
	}
	wg.Wait()

	// Phase 3 (serial, transactional): replace the scanned repos' rows.
	// The store holds a single connection, so no other reads may run between
	// BeginTx and Commit — everything below uses tx only.
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return res, err
	}
	defer tx.Rollback()
	now := time.Now()
	for _, d := range scanned {
		id := d.rec.Identity
		for _, del := range []string{
			`DELETE FROM endpoints WHERE repo = ?`,
			`DELETE FROM schemas WHERE repo = ?`,
			`DELETE FROM packages WHERE repo = ?`,
			// agent-observed edges survive rebuilds — the scanner can't
			// re-derive them, only `greybeard clean` forgets them
			`DELETE FROM edges WHERE from_repo = ? AND source = 'scanned'`,
		} {
			if _, err := tx.ExecContext(ctx, del, id); err != nil {
				return res, err
			}
		}
	}
	for _, d := range scanned {
		n, err := createDeclared(ctx, tx, d)
		if err != nil {
			return res, err
		}
		res.Nodes += n.nodes
		res.Edges += n.edges
	}
	for _, p := range plans {
		n, err := applyRefs(ctx, tx, p)
		if err != nil {
			return res, err
		}
		res.Nodes += n.nodes
		res.Edges += n.edges
	}
	for _, d := range scanned {
		if err := setIndexedIn(ctx, tx, d.rec.Identity, now, d.ex.Modules); err != nil {
			return res, err
		}
	}
	if err := tx.Commit(); err != nil {
		return res, err
	}
	res.ReposProcessed = len(scanned)
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
	rec, err := s.GetRepo(ctx, repo.Identity)
	if err != nil {
		return err
	}
	d := declared{rec: *rec, ex: ex}

	// Other repos' surfaces come from the store (declared at their last
	// index), not from re-extracting them. All reads happen before the
	// transaction: the single connection can't serve both at once.
	repos, err := s.ListRepos(ctx)
	if err != nil {
		return err
	}
	var rest []declared
	for _, r := range repos {
		if r.Identity == repo.Identity {
			continue
		}
		o, err := s.storedSurface(ctx, r)
		if err != nil {
			return err
		}
		rest = append(rest, o)
	}
	plan := computeRefs(d, rest)

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx,
		`DELETE FROM edges WHERE from_repo = ? AND source = 'scanned'`, repo.Identity); err != nil {
		return err
	}
	if _, err := createDeclared(ctx, tx, d); err != nil {
		return err
	}
	if _, err := applyRefs(ctx, tx, plan); err != nil {
		return err
	}
	if err := setIndexedIn(ctx, tx, repo.Identity, time.Now(), ex.Modules); err != nil {
		return err
	}
	return tx.Commit()
}

// storedSurface reconstructs a repo's declared surface from the store.
// ponytail: the schemas table stores tables and proto messages
// undistinguished, so they all come back as tables (SQL-context matching);
// proto-message links refresh when that repo is itself re-extracted.
func (s *Store) storedSurface(ctx context.Context, r RepoRecord) (declared, error) {
	o := declared{rec: r, ex: extract.Extraction{Modules: r.Modules}}
	var err error
	if o.ex.Endpoints, err = s.endpointsOf(ctx, r.Identity); err != nil {
		return o, err
	}
	if o.ex.Tables, err = s.schemasOf(ctx, r.Identity); err != nil {
		return o, err
	}
	return o, nil
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

// insertIn runs an INSERT OR IGNORE and reports whether a row was added.
func insertIn(ctx context.Context, x dbtx, query string, args ...any) (bool, error) {
	r, err := x.ExecContext(ctx, query, args...)
	if err != nil {
		return false, err
	}
	n, _ := r.RowsAffected()
	return n > 0, nil
}

// createDeclared writes a repo's own surface: endpoints, schemas (tables and
// proto messages), and the owner's shares_schema(write) edge.
func createDeclared(ctx context.Context, x dbtx, d declared) (counts, error) {
	var c counts
	id := d.rec.Identity
	for _, ep := range d.ex.Endpoints {
		ok, err := insertIn(ctx, x, `INSERT OR IGNORE INTO endpoints (repo, method, path) VALUES (?, ?, ?)`,
			id, ep.Method, ep.Path)
		if err != nil {
			return c, err
		}
		if ok {
			c.nodes++
		}
	}
	for _, name := range append(append([]string{}, d.ex.Tables...), d.ex.Messages...) {
		ok, err := insertIn(ctx, x, `INSERT OR IGNORE INTO schemas (repo, name) VALUES (?, ?)`, id, name)
		if err != nil {
			return c, err
		}
		if ok {
			c.nodes++
		}
		ok, err = insertIn(ctx, x, `INSERT OR IGNORE INTO edges (from_repo, edge_type, to_repo, detail, access_mode)
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

// computeRefs scans repo d's sources against every other repo's declared
// surface. Pure computation: no database access, safe to run in parallel.
//
// Precision rules (same name is NOT the same thing):
//   - self-declaration wins: a name d declares itself resolves locally and
//     never links to another repo's same-named table/endpoint/message
//   - generic endpoints (/health, /metrics, ...) never link
//   - generic table names (users, messages, ...) only link to repos d already
//     imports or calls — corroboration, not coincidence
func computeRefs(d declared, rest []declared) refPlan {
	p := refPlan{from: d.rec.Identity}
	if _, err := os.Stat(d.rec.LocalPath); err != nil {
		return p // nothing to scan
	}

	selfNames := map[string]bool{}
	for _, t := range append(append([]string{}, d.ex.Tables...), d.ex.Messages...) {
		selfNames[t] = true
	}
	selfPaths := map[string]bool{}
	for _, ep := range d.ex.Endpoints {
		selfPaths[ep.Path] = true
	}

	type epOwner struct{ owner, method string }
	pathOwners := map[string][]epOwner{}
	tableOwners := map[string][]string{}
	msgOwners := map[string][]string{}
	for _, o := range rest {
		for _, ep := range o.ex.Endpoints {
			if selfPaths[ep.Path] || extract.GenericPath(ep.Path) {
				continue
			}
			pathOwners[ep.Path] = append(pathOwners[ep.Path], epOwner{owner: o.rec.Identity, method: ep.Method})
		}
		for _, t := range o.ex.Tables {
			if selfNames[t] {
				continue
			}
			tableOwners[t] = append(tableOwners[t], o.rec.Identity)
		}
		for _, m := range o.ex.Messages {
			if selfNames[m] {
				continue
			}
			msgOwners[m] = append(msgOwners[m], o.rec.Identity)
		}
	}

	// imports: declared deps vs other repos' module paths.
	for _, dep := range d.ex.Deps {
		for _, o := range rest {
			for _, mod := range o.ex.Modules {
				if mod != "" && (dep == mod || strings.HasPrefix(dep, mod+"/")) {
					p.imports = append(p.imports, importRef{owner: o.rec.Identity, dep: dep})
				}
			}
		}
	}

	var paths, tables, msgs []string
	for pa := range pathOwners {
		paths = append(paths, pa)
	}
	for t := range tableOwners {
		tables = append(tables, t)
	}
	for m := range msgOwners {
		msgs = append(msgs, m)
	}
	pathHits, tableHits, msgHits := extract.ScanRefs(d.rec.LocalPath, paths, tables, msgs)

	for pa := range pathHits {
		for _, eo := range pathOwners[pa] {
			p.calls = append(p.calls, callRef{owner: eo.owner, method: eo.method, path: pa})
		}
	}

	// corroborated owners: repos d already relates to via a harder edge
	corroborated := map[string]bool{}
	for _, im := range p.imports {
		corroborated[im.owner] = true
	}
	for _, cl := range p.calls {
		corroborated[cl.owner] = true
	}

	for t, mode := range tableHits {
		for _, owner := range tableOwners[t] {
			if extract.GenericTable(t) && !corroborated[owner] {
				continue // "users" alone proves nothing about strangers
			}
			p.schemas = append(p.schemas, schemaRef{owner: owner, name: t, mode: mode})
		}
	}
	for m := range msgHits {
		for _, owner := range msgOwners[m] {
			p.schemas = append(p.schemas, schemaRef{owner: owner, name: m, mode: "read"})
		}
	}
	return p
}

// applyRefs writes a plan's edges (and any Package rows imports need).
func applyRefs(ctx context.Context, x dbtx, p refPlan) (counts, error) {
	var c counts
	for _, im := range p.imports {
		ok, err := insertIn(ctx, x, `INSERT OR IGNORE INTO packages (repo, import_path) VALUES (?, ?)`,
			im.owner, im.dep)
		if err != nil {
			return c, err
		}
		if ok {
			c.nodes++
		}
		ok, err = insertIn(ctx, x, `INSERT OR IGNORE INTO edges (from_repo, edge_type, to_repo, detail)
			VALUES (?, 'imports', ?, ?)`, p.from, im.owner, im.dep)
		if err != nil {
			return c, err
		}
		if ok {
			c.edges++
		}
	}
	for _, cl := range p.calls {
		ok, err := insertIn(ctx, x, `INSERT OR IGNORE INTO edges (from_repo, edge_type, to_repo, detail, method, path)
			VALUES (?, 'calls_api', ?, ?, ?, ?)`,
			p.from, cl.owner, cl.method+" "+cl.path, cl.method, cl.path)
		if err != nil {
			return c, err
		}
		if ok {
			c.edges++
		}
	}
	for _, sc := range p.schemas {
		ok, err := insertIn(ctx, x, `INSERT OR IGNORE INTO edges (from_repo, edge_type, to_repo, detail, access_mode)
			VALUES (?, 'shares_schema', ?, ?, ?)`, p.from, sc.owner, sc.name, sc.mode)
		if err != nil {
			return c, err
		}
		if ok {
			c.edges++
		}
	}
	return c, nil
}

// endpointsOf / schemasOf read a repo's declared surface back out of the store.
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
