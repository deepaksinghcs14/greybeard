package graph

import (
	"context"
	"strings"
	"time"

	"github.com/deepaksinghcs14/greybeard/internal/discover"
)

// RepoRecord is a registered repo as stored on its graph node.
type RepoRecord struct {
	Identity      string   `json:"identity"`
	Name          string   `json:"name"`
	RemoteURL     string   `json:"remote_url,omitempty"`
	LocalPath     string   `json:"local_path"`
	LastIndexedAt string   `json:"last_indexed_at,omitempty"` // RFC3339, "" = never
	Modules       []string `json:"modules,omitempty"`         // declared module/package names, set at index time
}

const repoReturn = `r.identity, r.name, r.remote_url, r.local_path, r.last_indexed_at, r.modules`

func repoFromRow(row []string) RepoRecord {
	rec := RepoRecord{
		Identity: row[0], Name: row[1], RemoteURL: row[2],
		LocalPath: row[3], LastIndexedAt: row[4],
	}
	if row[5] != "" {
		rec.Modules = strings.Split(row[5], ",")
	}
	return rec
}

// UpsertRepo registers a repo node (or refreshes its name/paths) without
// touching last_indexed_at.
func (s *Store) UpsertRepo(ctx context.Context, r discover.Repo) error {
	if _, err := s.cypher(ctx, `MERGE (r:Repo {identity: `+lit(r.Identity)+`})`, 1); err != nil {
		return err
	}
	_, err := s.cypher(ctx, `MATCH (r:Repo {identity: `+lit(r.Identity)+`})
		SET r.name = `+lit(r.Name)+`, r.remote_url = `+lit(r.RemoteURL)+`, r.local_path = `+lit(r.LocalPath), 1)
	return err
}

// SetIndexed stamps a repo as freshly extracted and records its declared
// module/package names (used to match other repos' deps during reindex).
func (s *Store) SetIndexed(ctx context.Context, identity string, at time.Time, modules []string) error {
	_, err := s.cypher(ctx, `MATCH (r:Repo {identity: `+lit(identity)+`})
		SET r.last_indexed_at = `+lit(at.UTC().Format(time.RFC3339))+`, r.modules = `+lit(strings.Join(modules, ",")), 1)
	return err
}

// GetRepo looks a repo up by identity or short name. Returns nil if absent.
func (s *Store) GetRepo(ctx context.Context, nameOrIdentity string) (*RepoRecord, error) {
	rows, err := s.cypher(ctx, `MATCH (r:Repo)
		WHERE r.identity = `+lit(nameOrIdentity)+` OR r.name = `+lit(nameOrIdentity)+`
		RETURN `+repoReturn, 6)
	if err != nil || len(rows) == 0 {
		return nil, err
	}
	rec := repoFromRow(rows[0])
	return &rec, nil
}

// ListRepos returns every registered repo.
func (s *Store) ListRepos(ctx context.Context) ([]RepoRecord, error) {
	rows, err := s.cypher(ctx, `MATCH (r:Repo) RETURN `+repoReturn, 6)
	if err != nil {
		return nil, err
	}
	out := make([]RepoRecord, 0, len(rows))
	for _, row := range rows {
		out = append(out, repoFromRow(row))
	}
	return out, nil
}

// Stale reports whether a repo record needs (re-)extraction.
func (r *RepoRecord) Stale(threshold time.Duration) bool {
	if r.LastIndexedAt == "" {
		return true
	}
	t, err := time.Parse(time.RFC3339, r.LastIndexedAt)
	if err != nil {
		return true
	}
	return time.Since(t) > threshold
}
