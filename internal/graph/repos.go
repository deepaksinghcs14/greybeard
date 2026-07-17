package graph

import (
	"context"
	"strings"
	"time"

	"github.com/deepaksinghcs14/greybeard/internal/discover"
)

// RepoRecord is a registered repo row.
type RepoRecord struct {
	Identity      string   `json:"identity"`
	Name          string   `json:"name"`
	RemoteURL     string   `json:"remote_url,omitempty"`
	LocalPath     string   `json:"local_path"`
	LastIndexedAt string   `json:"last_indexed_at,omitempty"` // RFC3339, "" = never
	IndexedBy     string   `json:"indexed_by,omitempty"`      // binary version that wrote the extraction
	Modules       []string `json:"modules,omitempty"`         // declared module/package names, set at index time
}

// UpsertRepo registers a repo (or refreshes its name/paths) without touching
// last_indexed_at.
func (s *Store) UpsertRepo(ctx context.Context, r discover.Repo) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO repos (identity, name, remote_url, local_path) VALUES (?, ?, ?, ?)
		ON CONFLICT (identity) DO UPDATE SET name = excluded.name,
			remote_url = excluded.remote_url, local_path = excluded.local_path`,
		r.Identity, r.Name, r.RemoteURL, r.LocalPath)
	return err
}

// SetIndexed stamps a repo as freshly extracted and records its declared
// module/package names (used to match other repos' deps during reindex).
func (s *Store) SetIndexed(ctx context.Context, identity string, at time.Time, modules []string) error {
	return setIndexedIn(ctx, s.db, identity, at, modules)
}

// setIndexedIn is SetIndexed against the pool or a transaction — builds stamp
// freshness inside the same transaction that writes the edges.
func setIndexedIn(ctx context.Context, x dbtx, identity string, at time.Time, modules []string) error {
	_, err := x.ExecContext(ctx,
		`UPDATE repos SET last_indexed_at = ?, modules = ?, indexed_by = ? WHERE identity = ?`,
		at.UTC().Format(time.RFC3339), strings.Join(modules, ","), BuilderVersion, identity)
	return err
}

// GetRepo looks a repo up by identity or short name. Returns nil if absent.
func (s *Store) GetRepo(ctx context.Context, nameOrIdentity string) (*RepoRecord, error) {
	rows, err := s.selectRepos(ctx, `WHERE identity = ?1 OR name = ?1`, nameOrIdentity)
	if err != nil || len(rows) == 0 {
		return nil, err
	}
	return &rows[0], nil
}

// ListRepos returns every registered repo.
func (s *Store) ListRepos(ctx context.Context) ([]RepoRecord, error) {
	return s.selectRepos(ctx, "")
}

func (s *Store) selectRepos(ctx context.Context, where string, args ...any) ([]RepoRecord, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT identity, name, remote_url, local_path, last_indexed_at, indexed_by, modules FROM repos `+where, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []RepoRecord
	for rows.Next() {
		var r RepoRecord
		var modules string
		if err := rows.Scan(&r.Identity, &r.Name, &r.RemoteURL, &r.LocalPath, &r.LastIndexedAt, &r.IndexedBy, &modules); err != nil {
			return nil, err
		}
		if modules != "" {
			r.Modules = strings.Split(modules, ",")
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// Stale reports whether a repo record needs (re-)extraction: never indexed,
// older than the threshold, or written by a different binary version (whose
// extraction surface may lack what the current version knows to look for).
func (r *RepoRecord) Stale(threshold time.Duration) bool {
	if r.LastIndexedAt == "" || r.IndexedBy != BuilderVersion {
		return true
	}
	t, err := time.Parse(time.RFC3339, r.LastIndexedAt)
	if err != nil {
		return true
	}
	return time.Since(t) > threshold
}
