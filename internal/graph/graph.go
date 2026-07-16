// Package graph stores and queries the cross-repo graph in an embedded
// SQLite database — zero setup, one file, no daemon. Node/edge model is
// defined in skills/greybeard/references/graph-schema.md — keep the two in
// sync. Nodes are typed tables; edges live in one table carrying their type,
// and the depends_on rollup is a derived view over it.
package graph

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite"
)

// Store wraps the SQLite database.
type Store struct {
	db *sql.DB
}

const schema = `
CREATE TABLE IF NOT EXISTS repos (
	identity        TEXT PRIMARY KEY, -- normalized remote URL or absolute path
	name            TEXT NOT NULL,
	remote_url      TEXT NOT NULL DEFAULT '',
	local_path      TEXT NOT NULL DEFAULT '',
	last_indexed_at TEXT NOT NULL DEFAULT '', -- RFC3339, '' = never
	modules         TEXT NOT NULL DEFAULT ''  -- comma-joined declared module/package names
);
CREATE TABLE IF NOT EXISTS endpoints (
	repo   TEXT NOT NULL, -- owning repo identity
	method TEXT NOT NULL,
	path   TEXT NOT NULL,
	PRIMARY KEY (repo, method, path)
);
CREATE TABLE IF NOT EXISTS schemas (
	repo TEXT NOT NULL, -- defining repo identity
	name TEXT NOT NULL,
	PRIMARY KEY (repo, name)
);
CREATE TABLE IF NOT EXISTS packages (
	repo        TEXT NOT NULL, -- providing repo identity
	import_path TEXT NOT NULL,
	PRIMARY KEY (repo, import_path)
);
CREATE TABLE IF NOT EXISTS edges (
	from_repo   TEXT NOT NULL,
	edge_type   TEXT NOT NULL, -- imports | calls_api | shares_schema
	to_repo     TEXT NOT NULL, -- identity of the target node's owner
	detail      TEXT NOT NULL, -- import path / "METHOD path" / schema name
	method      TEXT NOT NULL DEFAULT '', -- calls_api only
	path        TEXT NOT NULL DEFAULT '', -- calls_api only
	access_mode TEXT NOT NULL DEFAULT '', -- shares_schema only: read | write
	PRIMARY KEY (from_repo, edge_type, to_repo, detail)
);
-- The rolled-up Repo->Repo summary from the graph schema, derived rather
-- than recomputed: every underlying edge already knows both repos.
CREATE VIEW IF NOT EXISTS depends_on AS
	SELECT DISTINCT from_repo, to_repo, edge_type, detail FROM edges WHERE from_repo <> to_repo;
`

// Open opens (creating if needed) the database at GREYBEARD_DB, default
// ~/.greybeard/graph.db.
func Open(ctx context.Context) (*Store, error) {
	path := os.Getenv("GREYBEARD_DB")
	if path == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, err
		}
		path = filepath.Join(home, ".greybeard", "graph.db")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	// WAL + busy_timeout: `greybeard serve` and a background `reindex` can
	// touch the file concurrently from separate processes.
	db, err := sql.Open("sqlite", "file:"+path+"?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)")
	if err != nil {
		return nil, err
	}
	if _, err := db.ExecContext(ctx, schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("cannot open graph store at %s: %w", path, err)
	}
	return &Store{db: db}, nil
}

func (s *Store) Close() { s.db.Close() }

// StaleAfter returns the freshness threshold (GREYBEARD_STALE_AFTER, e.g.
// "24h", default 24h).
func StaleAfter() time.Duration {
	if v := os.Getenv("GREYBEARD_STALE_AFTER"); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			return d
		}
	}
	return 24 * time.Hour
}
