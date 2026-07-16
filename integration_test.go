// End-to-end test: init_root -> build -> all three query tools against two
// fixture repos with a real cross-repo dependency. The store is embedded
// SQLite, so this runs everywhere with zero setup.
package greybeard_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/deepaksinghcs14/greybeard/internal/discover"
	"github.com/deepaksinghcs14/greybeard/internal/graph"
)

func TestEndToEnd(t *testing.T) {
	t.Setenv("GREYBEARD_DB", filepath.Join(t.TempDir(), "graph.db"))
	ctx := context.Background()
	st, err := graph.Open(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	// --- Fixture repos (with .git dirs, which can't live in git) -------------
	root := t.TempDir()
	remotes := map[string]string{
		"orders-svc":  "git@github.com:acme/orders-svc.git",
		"billing-svc": "https://github.com/acme/billing-svc.git",
	}
	for name, remote := range remotes {
		dst := filepath.Join(root, name)
		if err := os.CopyFS(dst, os.DirFS(filepath.Join("testdata", "repos", name))); err != nil {
			t.Fatal(err)
		}
		gitDir := filepath.Join(dst, ".git")
		if err := os.MkdirAll(gitDir, 0o755); err != nil {
			t.Fatal(err)
		}
		config := "[remote \"origin\"]\n\turl = " + remote + "\n"
		if err := os.WriteFile(filepath.Join(gitDir, "config"), []byte(config), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	// --- init_root -----------------------------------------------------------
	repos, err := discover.ScanRoot(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(repos) != 2 {
		t.Fatalf("discovered %d repos, want 2", len(repos))
	}
	var billingRepo discover.Repo
	for _, r := range repos {
		if err := st.UpsertRepo(ctx, r); err != nil {
			t.Fatal(err)
		}
		if r.Name == "billing-svc" {
			billingRepo = r
		}
	}

	// --- build ---------------------------------------------------------------
	res, err := st.BuildAll(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.ReposProcessed != 2 {
		t.Fatalf("processed %d repos, want 2 (failed: %+v)", res.ReposProcessed, res.Failed)
	}
	if len(res.Failed) != 0 {
		t.Fatalf("unexpected failures: %+v", res.Failed)
	}
	if res.Nodes == 0 || res.Edges == 0 {
		t.Fatalf("empty build: %+v", res)
	}

	// --- get_related_repos ----------------------------------------------------
	rels, err := st.GetRelatedRepos(ctx, "billing-svc", 1)
	if err != nil {
		t.Fatal(err)
	}
	types := map[string]bool{}
	for _, r := range rels {
		if r.Repo != "orders-svc" {
			t.Errorf("unexpected related repo: %+v", r)
		}
		if r.Hops != 1 {
			t.Errorf("hops = %d, want 1: %+v", r.Hops, r)
		}
		types[r.EdgeType] = true
	}
	for _, want := range []string{"imports", "calls_api", "shares_schema"} {
		if !types[want] {
			t.Errorf("missing %s relation; got %+v", want, rels)
		}
	}

	// Reverse direction: orders-svc must see billing-svc too.
	rels, err = st.GetRelatedRepos(ctx, "orders-svc", 1)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, r := range rels {
		if r.Repo == "billing-svc" {
			found = true
		}
	}
	if !found {
		t.Errorf("orders-svc should see billing-svc: %+v", rels)
	}

	if _, err := st.GetRelatedRepos(ctx, "no-such-repo", 1); err == nil {
		t.Error("unknown repo should error, not return an empty result")
	}

	// --- get_callers_of --------------------------------------------------------
	callers, err := st.GetCallersOf(ctx, "POST /orders")
	if err != nil {
		t.Fatal(err)
	}
	if len(callers) != 1 || callers[0].Repo != "billing-svc" || callers[0].EdgeType != "calls_api" {
		t.Errorf("callers of POST /orders = %+v", callers)
	}
	callers, err = st.GetCallersOf(ctx, "example.com/orders-svc")
	if err != nil {
		t.Fatal(err)
	}
	if len(callers) != 1 || callers[0].Repo != "billing-svc" || callers[0].EdgeType != "imports" {
		t.Errorf("importers of example.com/orders-svc = %+v", callers)
	}

	// --- get_schema_dependents --------------------------------------------------
	deps, err := st.GetSchemaDependents(ctx, "orders")
	if err != nil {
		t.Fatal(err)
	}
	modes := map[string]string{}
	for _, d := range deps {
		modes[d.Repo] = d.AccessMode
	}
	if modes["orders-svc"] != "write" || modes["billing-svc"] != "read" {
		t.Errorf("schema dependents of orders = %+v", deps)
	}

	// --- audit (read-only) -------------------------------------------------------
	aud, err := st.Audit(ctx, 24*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if aud.TotalRepos != 2 || len(aud.EmptyRepos) != 0 || len(aud.StaleRepos) != 0 {
		t.Errorf("audit = %+v", aud)
	}

	// --- check semantics: fresh repo no-ops, stale threshold trips ---------------
	rec, err := st.GetRepo(ctx, "billing-svc")
	if err != nil || rec == nil {
		t.Fatalf("GetRepo billing-svc: %v %v", rec, err)
	}
	if rec.Stale(24 * time.Hour) {
		t.Error("just-built repo must be fresh")
	}
	if !rec.Stale(0) {
		t.Error("zero threshold must read as stale")
	}

	// --- single-repo reindex (the check hook's background path) ------------------
	if err := st.Reindex(ctx, billingRepo); err != nil {
		t.Fatal(err)
	}
	callers, err = st.GetCallersOf(ctx, "POST /orders")
	if err != nil {
		t.Fatal(err)
	}
	if len(callers) != 1 || callers[0].Repo != "billing-svc" {
		t.Errorf("callers after reindex = %+v", callers)
	}

	// --- agent-recorded edges: provenance carried, rebuilds preserve them --------
	if err := st.RecordRelation(ctx, "billing-svc", "orders-svc", "calls_api",
		"DELETE /orders/{id}", "", "billing/cancel.go:42 — url built from cfg.OrdersBase"); err != nil {
		t.Fatal(err)
	}
	if err := st.RecordRelation(ctx, "billing-svc", "orders-svc", "calls_api",
		"POST /x", "", ""); err == nil {
		t.Error("recording without evidence must be rejected")
	}
	callers, err = st.GetCallersOf(ctx, "DELETE /orders/{id}")
	if err != nil {
		t.Fatal(err)
	}
	if len(callers) != 1 || callers[0].Source != "agent" {
		t.Errorf("agent edge = %+v, want source=agent", callers)
	}
	if callers[0].Evidence != "billing/cancel.go:42 — url built from cfg.OrdersBase" {
		t.Errorf("agent edge evidence = %q, want the recorded citation", callers[0].Evidence)
	}

	// evidence must also surface through get_related_repos and the
	// visualize snapshot, not just get_callers_of — record_relation's
	// evidence was write-only until this was fixed.
	rels, err = st.GetRelatedRepos(ctx, "billing-svc", 1)
	if err != nil {
		t.Fatal(err)
	}
	foundEvidence := false
	for _, r := range rels {
		if r.EdgeType == "calls_api" && r.Detail == "DELETE /orders/{id}" {
			if r.Evidence == "" {
				t.Errorf("get_related_repos dropped evidence for %+v", r)
			}
			foundEvidence = true
		}
	}
	if !foundEvidence {
		t.Fatal("expected the agent-recorded edge in get_related_repos")
	}
	snap, err := st.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	foundEvidence = false
	for _, e := range snap.Edges {
		if e.EdgeType == "calls_api" && e.Detail == "DELETE /orders/{id}" {
			if e.Source != "agent" || e.Evidence == "" {
				t.Errorf("visualize snapshot dropped source/evidence for %+v", e)
			}
			foundEvidence = true
		}
	}
	if !foundEvidence {
		t.Fatal("expected the agent-recorded edge in the visualize snapshot")
	}
	if _, err := st.BuildAll(ctx, nil); err != nil {
		t.Fatal(err)
	}
	callers, err = st.GetCallersOf(ctx, "DELETE /orders/{id}")
	if err != nil {
		t.Fatal(err)
	}
	if len(callers) != 1 {
		t.Errorf("agent edge must survive a rebuild, got %+v", callers)
	}

	// --- clean: relations gone, registrations kept, everything stale -------------
	cres, err := st.Clean(ctx, false)
	if err != nil {
		t.Fatal(err)
	}
	if cres.EdgesRemoved == 0 || cres.ReposKept != 2 || cres.ReposRemoved != 0 {
		t.Errorf("clean = %+v", cres)
	}
	callers, err = st.GetCallersOf(ctx, "POST /orders")
	if err != nil {
		t.Fatal(err)
	}
	if len(callers) != 0 {
		t.Errorf("callers after clean should be empty: %+v", callers)
	}
	aud, err = st.Audit(ctx, 24*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if aud.TotalRepos != 2 || len(aud.EmptyRepos) != 2 || len(aud.StaleRepos) != 2 {
		t.Errorf("audit after clean = %+v", aud)
	}

	// clean --all: full reset
	if _, err := st.Clean(ctx, true); err != nil {
		t.Fatal(err)
	}
	remaining, err := st.ListRepos(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(remaining) != 0 {
		t.Errorf("repos after clean --all: %+v", remaining)
	}
}

// TestSymbolCallers exercises get_callers_of's calls_symbol path end to end:
// a real extraction (declared symbol regex) feeding a real cross-repo scan
// (word-boundary match, corroborated via an existing imports edge) feeding a
// real query — the exact chain that answers "who calls this exported thing."
func TestSymbolCallers(t *testing.T) {
	t.Setenv("GREYBEARD_DB", filepath.Join(t.TempDir(), "graph.db"))
	ctx := context.Background()
	st, err := graph.Open(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	root := t.TempDir()
	libDir := filepath.Join(root, "config-lib")
	callerDir := filepath.Join(root, "caller-svc")
	for _, dir := range []string{libDir, callerDir} {
		if err := os.MkdirAll(filepath.Join(dir, ".git"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	os.WriteFile(filepath.Join(libDir, ".git", "config"),
		[]byte("[remote \"origin\"]\n\turl = https://github.com/acme/config-lib.git\n"), 0o644)
	os.WriteFile(filepath.Join(callerDir, ".git", "config"),
		[]byte("[remote \"origin\"]\n\turl = https://github.com/acme/caller-svc.git\n"), 0o644)

	os.WriteFile(filepath.Join(libDir, "go.mod"), []byte("module github.com/acme/config-lib\n\ngo 1.22\n"), 0o644)
	os.WriteFile(filepath.Join(libDir, "config.go"),
		[]byte("package config\n\nfunc ParseConfig() {}\n"), 0o644)

	os.WriteFile(filepath.Join(callerDir, "go.mod"),
		[]byte("module github.com/acme/caller-svc\n\ngo 1.22\n\nrequire github.com/acme/config-lib v0.0.0\n"), 0o644)
	os.WriteFile(filepath.Join(callerDir, "main.go"),
		[]byte("package main\n\nfunc main() { ParseConfig() }\n"), 0o644)

	repos, err := discover.ScanRoot(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range repos {
		if err := st.UpsertRepo(ctx, r); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := st.BuildAll(ctx, nil); err != nil {
		t.Fatal(err)
	}

	callers, err := st.GetCallersOf(ctx, "ParseConfig")
	if err != nil {
		t.Fatal(err)
	}
	if len(callers) != 1 || callers[0].Repo != "caller-svc" || callers[0].EdgeType != "calls_symbol" {
		t.Errorf("get_callers_of(ParseConfig) = %+v, want one calls_symbol edge from caller-svc", callers)
	}
	if callers[0].Source != "scanned" {
		t.Errorf("scanned symbol edge should carry source=scanned, got %+v", callers[0])
	}

	// record_relation for calls_symbol: makes an undeclared symbol queryable
	// too, same as it already does for calls_api/shares_schema.
	if err := st.RecordRelation(ctx, "caller-svc", "config-lib", "calls_symbol",
		"LoadDefaults", "", "main.go:12 — dynamically resolved, extractor can't see it"); err != nil {
		t.Fatal(err)
	}
	callers, err = st.GetCallersOf(ctx, "LoadDefaults")
	if err != nil {
		t.Fatal(err)
	}
	if len(callers) != 1 || callers[0].Source != "agent" || callers[0].Evidence == "" {
		t.Errorf("agent-recorded symbol edge = %+v, want source=agent with evidence", callers)
	}
}
