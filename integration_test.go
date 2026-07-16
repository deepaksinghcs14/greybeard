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
}
