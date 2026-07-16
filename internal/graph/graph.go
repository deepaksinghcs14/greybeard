// Package graph stores and queries the cross-repo graph in Postgres +
// Apache AGE. Node/edge model is defined in
// skills/greybeard/references/graph-schema.md — keep the two in sync.
package graph

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const graphName = "greybeard"

// Store wraps a pgx pool configured for AGE.
type Store struct {
	pool *pgxpool.Pool
}

// Open connects to GREYBEARD_DB_URL (default: local greybeard database) and
// bootstraps the AGE extension + graph if missing.
func Open(ctx context.Context) (*Store, error) {
	dsn := os.Getenv("GREYBEARD_DB_URL")
	if dsn == "" {
		dsn = "postgres://localhost:5432/greybeard?sslmode=disable"
	}
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, err
	}
	// Simple protocol: AGE's cypher() doesn't play well with prepared
	// statements, and text-format results let us scan agtype as strings.
	cfg.ConnConfig.DefaultQueryExecMode = pgx.QueryExecModeSimpleProtocol
	cfg.AfterConnect = func(ctx context.Context, conn *pgx.Conn) error {
		_, err := conn.Exec(ctx, `LOAD 'age'; SET search_path = ag_catalog, "$user", public;`)
		return err
	}
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, err
	}
	s := &Store{pool: pool}
	if err := s.bootstrap(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("cannot reach or bootstrap graph store at %s: %w", dsn, err)
	}
	return s, nil
}

func (s *Store) Close() { s.pool.Close() }

func (s *Store) bootstrap(ctx context.Context) error {
	if _, err := s.pool.Exec(ctx, `CREATE EXTENSION IF NOT EXISTS age`); err != nil {
		return err
	}
	var n int
	if err := s.pool.QueryRow(ctx,
		`SELECT count(*) FROM ag_catalog.ag_graph WHERE name = '`+graphName+`'`).Scan(&n); err != nil {
		return err
	}
	if n == 0 {
		if _, err := s.pool.Exec(ctx, `SELECT ag_catalog.create_graph('`+graphName+`')`); err != nil {
			return err
		}
	}
	return nil
}

// cypher runs a cypher query and returns rows of unquoted agtype values.
// cols must match the query's RETURN arity (use 1 for queries with no RETURN).
func (s *Store) cypher(ctx context.Context, query string, cols int) ([][]string, error) {
	if cols < 1 {
		cols = 1
	}
	defs := make([]string, cols)
	for i := range defs {
		defs[i] = fmt.Sprintf("c%d agtype", i)
	}
	sqlText := fmt.Sprintf("SELECT * FROM ag_catalog.cypher('%s', $gb$ %s $gb$) AS (%s)",
		graphName, query, strings.Join(defs, ", "))
	rows, err := s.pool.Query(ctx, sqlText)
	if err != nil {
		return nil, fmt.Errorf("cypher %q: %w", query, err)
	}
	defer rows.Close()
	var out [][]string
	for rows.Next() {
		raw := make([]sql.NullString, cols)
		dest := make([]any, cols)
		for i := range raw {
			dest[i] = &raw[i]
		}
		if err := rows.Scan(dest...); err != nil {
			return nil, err
		}
		row := make([]string, cols)
		for i, r := range raw {
			row[i] = agString(r.String)
		}
		out = append(out, row)
	}
	return out, rows.Err()
}

// agString unwraps an agtype text value: JSON strings lose their quotes,
// null becomes "".
func agString(v string) string {
	if v == "" || v == "null" {
		return ""
	}
	if strings.HasPrefix(v, `"`) {
		var s string
		if json.Unmarshal([]byte(v), &s) == nil {
			return s
		}
	}
	return v
}

// lit quotes a string as a cypher literal. Values are inlined because AGE's
// cypher() takes the query as a string, not bind parameters; this is a local
// single-user store, not an untrusted-input boundary.
func lit(s string) string {
	s = strings.ReplaceAll(s, "$gb$", "") // guard the surrounding dollar-quote
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `'`, `\'`)
	return "'" + s + "'"
}

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
