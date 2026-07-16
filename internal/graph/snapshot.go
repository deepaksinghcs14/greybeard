package graph

import (
	"context"
	"time"
)

// VizRepo / VizEdge / VizData feed the `greybeard visualize` web page; field
// names are the page's contract.
type VizRepo struct {
	Identity      string   `json:"identity"`
	Name          string   `json:"name"`
	LastIndexedAt string   `json:"last_indexed_at"` // human-formatted, "" = never
	Stale         bool     `json:"stale"`
	Endpoints     []string `json:"endpoints"`
	Schemas       []string `json:"schemas"`
	Packages      []string `json:"packages"` // packages this repo provides to others
}

type VizEdge struct {
	From     string `json:"from"` // repo identity
	To       string `json:"to"`
	EdgeType string `json:"edge_type"`
	Detail   string `json:"detail"`
	Source   string `json:"source"` // scanned | agent
	Evidence string `json:"evidence,omitempty"`
}

type VizData struct {
	Repos []VizRepo `json:"repos"`
	Edges []VizEdge `json:"edges"`
}

// Snapshot dumps the whole graph for visualization.
func (s *Store) Snapshot(ctx context.Context) (VizData, error) {
	data := VizData{Repos: []VizRepo{}, Edges: []VizEdge{}}
	repos, err := s.ListRepos(ctx)
	if err != nil {
		return data, err
	}
	stale := StaleAfter()
	for _, r := range repos {
		vr := VizRepo{
			Identity: r.Identity, Name: r.Name, Stale: r.Stale(stale),
			Endpoints: []string{}, Schemas: []string{}, Packages: []string{},
		}
		if t, err := time.Parse(time.RFC3339, r.LastIndexedAt); err == nil {
			vr.LastIndexedAt = t.Local().Format("Jan 2, 2006 15:04")
		}
		eps, err := s.endpointsOf(ctx, r.Identity)
		if err != nil {
			return data, err
		}
		for _, ep := range eps {
			vr.Endpoints = append(vr.Endpoints, ep.Method+" "+ep.Path)
		}
		if vr.Schemas, err = s.schemasOf(ctx, r.Identity); err != nil {
			return data, err
		}
		if vr.Schemas == nil {
			vr.Schemas = []string{}
		}
		rows, err := s.db.QueryContext(ctx, `SELECT import_path FROM packages WHERE repo = ?`, r.Identity)
		if err != nil {
			return data, err
		}
		for rows.Next() {
			var p string
			if err := rows.Scan(&p); err != nil {
				rows.Close()
				return data, err
			}
			vr.Packages = append(vr.Packages, p)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return data, err
		}
		data.Repos = append(data.Repos, vr)
	}

	rows, err := s.db.QueryContext(ctx, `SELECT from_repo, to_repo, edge_type, detail, source, evidence FROM depends_on`)
	if err != nil {
		return data, err
	}
	defer rows.Close()
	for rows.Next() {
		var e VizEdge
		if err := rows.Scan(&e.From, &e.To, &e.EdgeType, &e.Detail, &e.Source, &e.Evidence); err != nil {
			return data, err
		}
		data.Edges = append(data.Edges, e)
	}
	return data, rows.Err()
}
