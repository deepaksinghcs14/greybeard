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

// A fresh timestamp written by an older extractor epoch must still read as
// stale (the empty-symbols-table failure mode: a pre-upgrade process stamps
// "fresh" data lacking what the new extractor finds). Rows from a NEWER
// epoch must read as fresh, so an old long-lived server doesn't fight the
// updated session hook in a rebuild ping-pong.
func TestExtractorEpochStaleness(t *testing.T) {
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
	if err := st.SetIndexed(ctx, repo.Identity, time.Now(), nil); err != nil {
		t.Fatal(err)
	}

	stale := func(wantStale bool, wantCount int, context string) {
		t.Helper()
		rec, err := st.GetRepo(ctx, repo.Identity)
		if err != nil {
			t.Fatal(err)
		}
		if rec.Stale(24*time.Hour) != wantStale {
			t.Errorf("%s: Stale = %v, want %v", context, !wantStale, wantStale)
		}
		n, err := st.StaleOrUnindexedCount(ctx, 24*time.Hour)
		if err != nil {
			t.Fatal(err)
		}
		if n != wantCount {
			t.Errorf("%s: StaleOrUnindexedCount = %d, want %d", context, n, wantCount)
		}
	}

	stale(false, 0, "current epoch, fresh timestamp")

	// Simulate rows written by a pre-epoch binary (migration default 0).
	if _, err := st.db.ExecContext(ctx, `UPDATE repos SET extractor_epoch = ?`, ExtractorEpoch-1); err != nil {
		t.Fatal(err)
	}
	stale(true, 1, "older epoch despite fresh timestamp")

	// Rows from a newer epoch (this process is the outdated one): fresh.
	if _, err := st.db.ExecContext(ctx, `UPDATE repos SET extractor_epoch = ?`, ExtractorEpoch+1); err != nil {
		t.Fatal(err)
	}
	stale(false, 0, "newer epoch must not read as stale")
}
