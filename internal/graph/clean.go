package graph

import "context"

// CleanResult reports what a Clean removed.
type CleanResult struct {
	EdgesRemoved int  `json:"edges_removed"`
	NodesRemoved int  `json:"nodes_removed"` // endpoints + schemas + packages + symbols
	ReposRemoved int  `json:"repos_removed"` // only with all=true
	ReposKept    int  `json:"repos_kept"`
	All          bool `json:"all"`
}

// Clean deletes every extracted row — all relations and declared surface.
// With all=false, repo registrations survive but are marked never-indexed
// (the next check/build re-extracts everything). With all=true the store is
// fully reset, as if init was never run.
func (s *Store) Clean(ctx context.Context, all bool) (CleanResult, error) {
	var res CleanResult
	res.All = all
	count := func(q string) (int, error) {
		var n int
		err := s.db.QueryRowContext(ctx, q).Scan(&n)
		return n, err
	}
	var err error
	if res.EdgesRemoved, err = count(`SELECT count(*) FROM edges`); err != nil {
		return res, err
	}
	if res.NodesRemoved, err = count(`SELECT
		(SELECT count(*) FROM endpoints) + (SELECT count(*) FROM schemas) +
		(SELECT count(*) FROM packages) + (SELECT count(*) FROM symbols)`); err != nil {
		return res, err
	}
	repos, err := count(`SELECT count(*) FROM repos`)
	if err != nil {
		return res, err
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return res, err
	}
	defer tx.Rollback()
	for _, q := range []string{
		`DELETE FROM edges`, `DELETE FROM endpoints`, `DELETE FROM schemas`,
		`DELETE FROM packages`, `DELETE FROM symbols`,
	} {
		if _, err := tx.ExecContext(ctx, q); err != nil {
			return res, err
		}
	}
	if all {
		if _, err := tx.ExecContext(ctx, `DELETE FROM repos`); err != nil {
			return res, err
		}
		res.ReposRemoved = repos
	} else {
		if _, err := tx.ExecContext(ctx, `UPDATE repos SET last_indexed_at = '', modules = ''`); err != nil {
			return res, err
		}
		res.ReposKept = repos
	}
	return res, tx.Commit()
}
