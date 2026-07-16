// Package mcp exposes the graph over the Model Context Protocol, stdio only.
package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"

	"github.com/deepaksinghcs14/greybeard/internal/discover"
	"github.com/deepaksinghcs14/greybeard/internal/graph"
)

// RepoDiscoveryResult is what init_root returns.
type RepoDiscoveryResult struct {
	ReposFound int      `json:"repos_found"`
	Repos      []string `json:"repos"`
}

// Serve runs the MCP server over stdio until the client disconnects.
func Serve(ctx context.Context, st *graph.Store, version string) error {
	s := server.NewMCPServer("greybeard", version)

	s.AddTool(mcp.NewTool("get_related_repos",
		mcp.WithDescription("Repos connected to the given repo via imports, calls_api, or shares_schema edges, up to max_hops away. Empty result = no known cross-repo ties."),
		mcp.WithString("repo", mcp.Required(), mcp.Description("Repo short name (e.g. \"orders-svc\") or identity (e.g. \"github.com/acme/orders-svc\")")),
		mcp.WithNumber("max_hops", mcp.Description("Blast-radius width, default 1. Beyond 2-3 gets slow on dense graphs.")),
	), func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		repo, err := req.RequireString("repo")
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		return asJSON(st.GetRelatedRepos(ctx, repo, req.GetInt("max_hops", 1)))
	})

	s.AddTool(mcp.NewTool("get_callers_of",
		mcp.WithDescription("Reverse lookup: repos that call an endpoint (\"POST /orders\", \"/orders\", \"OrderService/Create\") or import a package path. Every result carries its edge_type."),
		mcp.WithString("target", mcp.Required(), mcp.Description("Endpoint (optionally method-prefixed), exported symbol, or package path")),
	), func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		target, err := req.RequireString("target")
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		return asJSON(st.GetCallersOf(ctx, target))
	})

	s.AddTool(mcp.NewTool("get_schema_dependents",
		mcp.WithDescription("Repos that read/write a shared schema (table or proto message) by name, with access_mode read|write|read_write."),
		mcp.WithString("schema", mcp.Required(), mcp.Description("Table or message name, e.g. \"orders\"")),
	), func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		schema, err := req.RequireString("schema")
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		return asJSON(st.GetSchemaDependents(ctx, schema))
	})

	s.AddTool(mcp.NewTool("init_root",
		mcp.WithDescription("Walk a directory tree for git repos and register each in the graph. Run greybeard build (or build_graph) afterwards for the first extraction."),
		mcp.WithString("path", mcp.Required(), mcp.Description("Root folder to scan, e.g. ~/code")),
	), func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		path, err := req.RequireString("path")
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		// agents pass "~/code" literally; the shell isn't here to expand it
		if strings.HasPrefix(path, "~/") {
			if home, err := os.UserHomeDir(); err == nil {
				path = filepath.Join(home, path[2:])
			}
		}
		repos, err := discover.ScanRoot(path)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		res := RepoDiscoveryResult{ReposFound: len(repos), Repos: []string{}}
		for _, r := range repos {
			if err := st.UpsertRepo(ctx, r); err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			res.Repos = append(res.Repos, r.Name)
		}
		return asJSON(res, nil)
	})

	s.AddTool(mcp.NewTool("build_graph",
		mcp.WithDescription("Full rebuild: re-extract every registered repo and repopulate all nodes/edges. Safe to rerun; not incremental."),
	), func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return asJSON(st.BuildAll(ctx, nil))
	})

	s.AddTool(mcp.NewTool("audit_graph",
		mcp.WithDescription("Read-only health report: repos with nothing extracted, and repos whose extracted data is older than the staleness threshold. Never mutates the graph."),
	), func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return asJSON(st.Audit(ctx, graph.StaleAfter()))
	})

	return server.ServeStdio(s)
}

// asJSON turns (result, err) into an MCP tool response. nil slices render as
// [] so agents see "empty result", never "null".
func asJSON[T any](v T, err error) (*mcp.CallToolResult, error) {
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("marshal result: %v", err)), nil
	}
	s := string(b)
	if s == "null" {
		s = "[]"
	}
	return mcp.NewToolResultText(s), nil
}
