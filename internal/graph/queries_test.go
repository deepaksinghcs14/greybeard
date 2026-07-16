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
