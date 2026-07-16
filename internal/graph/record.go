package graph

import (
	"context"
	"fmt"
	"strings"
)

// RecordRelation stores an agent-observed edge — a relationship Claude (or a
// human) verified in code that the text scanner can't see (URLs built from
// config, ORM table access, ...). Provenance stays 'agent' so consumers can
// distinguish a model's verified observation from parsed declarations, and
// rebuilds preserve these edges instead of wiping them.
func (s *Store) RecordRelation(ctx context.Context, from, to, edgeType, detail, accessMode, evidence string) error {
	switch edgeType {
	case "imports", "calls_api", "shares_schema", "calls_symbol":
	default:
		return fmt.Errorf("edge_type must be imports, calls_api, shares_schema, or calls_symbol (got %q)", edgeType)
	}
	if strings.TrimSpace(evidence) == "" {
		return fmt.Errorf("evidence is required — cite what you saw (file:line or a snippet)")
	}
	fromRec, err := s.GetRepo(ctx, from)
	if err != nil {
		return err
	}
	if fromRec == nil {
		return fmt.Errorf("repo %q is not registered in the graph", from)
	}
	toRec, err := s.GetRepo(ctx, to)
	if err != nil {
		return err
	}
	if toRec == nil {
		return fmt.Errorf("repo %q is not registered in the graph", to)
	}

	method, path := "", ""
	if edgeType == "calls_api" {
		if m, p, ok := strings.Cut(detail, " "); ok && isHTTPMethod(m) {
			method, path = strings.ToUpper(m), strings.TrimSpace(p)
		} else {
			path = detail
		}
		// make the endpoint queryable via get_callers_of even if the owner
		// never declared it in a spec
		if _, err := s.db.ExecContext(ctx,
			`INSERT OR IGNORE INTO endpoints (repo, method, path) VALUES (?, ?, ?)`,
			toRec.Identity, method, path); err != nil {
			return err
		}
	}
	if edgeType == "shares_schema" {
		switch accessMode {
		case "read", "write", "read_write":
		case "":
			accessMode = "read"
		default:
			return fmt.Errorf("access_mode must be read, write, or read_write (got %q)", accessMode)
		}
		if _, err := s.db.ExecContext(ctx,
			`INSERT OR IGNORE INTO schemas (repo, name) VALUES (?, ?)`,
			toRec.Identity, detail); err != nil {
			return err
		}
	}
	if edgeType == "calls_symbol" {
		// make the symbol queryable via get_callers_of even if the owner's
		// declaration wasn't caught by the regex-based extractor
		if _, err := s.db.ExecContext(ctx,
			`INSERT OR IGNORE INTO symbols (repo, name) VALUES (?, ?)`,
			toRec.Identity, detail); err != nil {
			return err
		}
	}
	// INSERT OR REPLACE: an agent observation upgrades a scanned edge's
	// provenance and attaches its evidence.
	_, err = s.db.ExecContext(ctx, `INSERT OR REPLACE INTO edges
		(from_repo, edge_type, to_repo, detail, method, path, access_mode, source, evidence)
		VALUES (?, ?, ?, ?, ?, ?, ?, 'agent', ?)`,
		fromRec.Identity, edgeType, toRec.Identity, detail, method, path, accessMode, evidence)
	return err
}
