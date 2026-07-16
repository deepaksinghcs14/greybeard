package mcp

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/deepaksinghcs14/greybeard/internal/discover"
	"github.com/deepaksinghcs14/greybeard/internal/graph"
)

func openTestStore(t *testing.T) *graph.Store {
	t.Helper()
	t.Setenv("GREYBEARD_DB", filepath.Join(t.TempDir(), "graph.db"))
	st, err := graph.Open(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

func TestRepoFreshnessCaveat(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()
	repo := discover.Repo{Identity: "github.com/acme/orders-svc", Name: "orders-svc", LocalPath: "/tmp/orders-svc"}
	if err := st.UpsertRepo(ctx, repo); err != nil {
		t.Fatal(err)
	}

	if c := repoFreshnessCaveat(ctx, st, "orders-svc"); !strings.Contains(c, "never been extracted") {
		t.Errorf("never-indexed repo: caveat = %q, want it to mention never being extracted", c)
	}

	if err := st.SetIndexed(ctx, repo.Identity, time.Now(), nil); err != nil {
		t.Fatal(err)
	}
	if c := repoFreshnessCaveat(ctx, st, "orders-svc"); c != "" {
		t.Errorf("freshly indexed repo: caveat = %q, want empty", c)
	}

	if err := st.SetIndexed(ctx, repo.Identity, time.Now().Add(-48*time.Hour), nil); err != nil {
		t.Fatal(err)
	}
	if c := repoFreshnessCaveat(ctx, st, "orders-svc"); !strings.Contains(c, "stale") {
		t.Errorf("stale repo: caveat = %q, want it to mention staleness", c)
	}

	if c := repoFreshnessCaveat(ctx, st, "no-such-repo"); c != "" {
		t.Errorf("unregistered repo: caveat = %q, want empty (GetRelatedRepos itself errors on this case)", c)
	}
}

func TestGraphGapsCaveat(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()

	if c := graphGapsCaveat(ctx, st); c != "" {
		t.Errorf("empty graph: caveat = %q, want empty", c)
	}

	repo := discover.Repo{Identity: "github.com/acme/orders-svc", Name: "orders-svc", LocalPath: "/tmp/orders-svc"}
	if err := st.UpsertRepo(ctx, repo); err != nil {
		t.Fatal(err)
	}
	if c := graphGapsCaveat(ctx, st); !strings.Contains(c, "1 registered repo") {
		t.Errorf("one never-indexed repo: caveat = %q, want it to mention the count", c)
	}

	if err := st.SetIndexed(ctx, repo.Identity, time.Now(), nil); err != nil {
		t.Fatal(err)
	}
	if c := graphGapsCaveat(ctx, st); c != "" {
		t.Errorf("all repos fresh: caveat = %q, want empty", c)
	}
}

func TestNewQueryResponseNilResultsMarshalToEmptyArray(t *testing.T) {
	var nilResults []graph.Caller
	resp := newQueryResponse(nilResults, "")
	b, err := json.Marshal(resp)
	if err != nil {
		t.Fatal(err)
	}
	if got := string(b); got != `{"results":[]}` {
		t.Errorf("marshaled nil results = %s, want {\"results\":[]} (never null, no caveat key when empty)", got)
	}
}
