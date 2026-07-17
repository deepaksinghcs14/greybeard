package graph

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/deepaksinghcs14/greybeard/internal/discover"
)

func TestStaleOrUnindexedCount(t *testing.T) {
	t.Setenv("GREYBEARD_DB", filepath.Join(t.TempDir(), "graph.db"))
	ctx := context.Background()
	st, err := Open(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	fresh := discover.Repo{Identity: "github.com/acme/fresh", Name: "fresh", LocalPath: "/tmp/fresh"}
	never := discover.Repo{Identity: "github.com/acme/never", Name: "never", LocalPath: "/tmp/never"}
	stale := discover.Repo{Identity: "github.com/acme/stale", Name: "stale", LocalPath: "/tmp/stale"}
	for _, r := range []discover.Repo{fresh, never, stale} {
		if err := st.UpsertRepo(ctx, r); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.SetIndexed(ctx, fresh.Identity, time.Now(), nil); err != nil {
		t.Fatal(err)
	}
	if err := st.SetIndexed(ctx, stale.Identity, time.Now().Add(-48*time.Hour), nil); err != nil {
		t.Fatal(err)
	}
	// never is left with last_indexed_at == "" (registered, not yet built)

	n, err := st.StaleOrUnindexedCount(ctx, 24*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Errorf("StaleOrUnindexedCount = %d, want 2 (never-indexed + stale, not fresh)", n)
	}
}

// A fresh timestamp written by a different binary version must still read as
// stale — a pre-upgrade process can stamp "fresh" data that lacks whatever
// the new version extracts (the empty-symbols-table failure mode).
func TestVersionSkewIsStale(t *testing.T) {
	t.Setenv("GREYBEARD_DB", filepath.Join(t.TempDir(), "graph.db"))
	ctx := context.Background()
	st, err := Open(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	repo := discover.Repo{Identity: "github.com/acme/orders", Name: "orders", LocalPath: "/tmp/orders"}
	if err := st.UpsertRepo(ctx, repo); err != nil {
		t.Fatal(err)
	}

	old := BuilderVersion
	defer func() { BuilderVersion = old }()

	BuilderVersion = "0.3.0"
	if err := st.SetIndexed(ctx, repo.Identity, time.Now(), nil); err != nil {
		t.Fatal(err)
	}
	rec, err := st.GetRepo(ctx, repo.Identity)
	if err != nil {
		t.Fatal(err)
	}
	if rec.Stale(24 * time.Hour) {
		t.Error("same-version fresh record should not be stale")
	}

	BuilderVersion = "0.3.1" // the binary upgraded; the rows didn't
	rec, err = st.GetRepo(ctx, repo.Identity)
	if err != nil {
		t.Fatal(err)
	}
	if !rec.Stale(24 * time.Hour) {
		t.Error("record written by an older binary version should be stale despite a fresh timestamp")
	}
	n, err := st.StaleOrUnindexedCount(ctx, 24*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("StaleOrUnindexedCount = %d, want 1 (version-skewed repo)", n)
	}
}
