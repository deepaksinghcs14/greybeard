// Package mcp exposes the graph over the Model Context Protocol, stdio only.
package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"syscall"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"

	"github.com/deepaksinghcs14/greybeard/internal/discover"
	"github.com/deepaksinghcs14/greybeard/internal/graph"
	"github.com/deepaksinghcs14/greybeard/internal/spawn"
)

// RepoDiscoveryResult is what init_root returns.
type RepoDiscoveryResult struct {
	ReposFound int      `json:"repos_found"`
	Repos      []string `json:"repos"`
}

// queryResponse wraps a graph query's results with a caveat about extraction
// gaps: Results empty and Caveat empty means "confirmed, no known ties."
// Results empty and Caveat set means "don't conclude that — part of the
// graph this query touches hasn't been extracted yet."
type queryResponse[T any] struct {
	Results []T    `json:"results"`
	Caveat  string `json:"caveat,omitempty"`
}

func newQueryResponse[T any](results []T, caveat string) queryResponse[T] {
	if results == nil {
		results = []T{} // nil marshals as null; the query tools have always promised []
	}
	return queryResponse[T]{Results: results, Caveat: caveat}
}

// repoFreshnessCaveat checks the one repo a get_related_repos call is
// anchored on — cheap, since the identity is already in hand.
func repoFreshnessCaveat(ctx context.Context, st *graph.Store, repo string) string {
	rec, err := st.GetRepo(ctx, repo)
	if err != nil || rec == nil {
		return ""
	}
	if rec.LastIndexedAt == "" {
		return fmt.Sprintf("%s has never been extracted — an empty result does not mean it has no dependencies, only that nothing is known yet. Run build_graph.", rec.Name)
	}
	if rec.Stale(graph.StaleAfter()) {
		return fmt.Sprintf("%s's extracted data is stale (last indexed %s) — results may miss recent changes. Consider running build_graph.", rec.Name, rec.LastIndexedAt)
	}
	return ""
}

// graphGapsCaveat is for queries with no single anchor repo (get_callers_of,
// get_schema_dependents match against strings, not a specific repo) — flags
// when the graph overall has gaps that could explain an empty result.
func graphGapsCaveat(ctx context.Context, st *graph.Store) string {
	n, err := st.StaleOrUnindexedCount(ctx, graph.StaleAfter())
	if err != nil || n == 0 {
		return ""
	}
	return fmt.Sprintf("%d registered repo(s) are stale or have never been extracted — an empty result may reflect that gap rather than confirmed absence. Run audit_graph to see which.", n)
}

// Serve runs the MCP server over stdio until the client disconnects.
func Serve(ctx context.Context, st *graph.Store, version string) error {
	s := server.NewMCPServer("greybeard", version)

	s.AddTool(mcp.NewTool("get_related_repos",
		mcp.WithDescription("Repos connected to the given repo via imports, calls_api, shares_schema, or calls_symbol edges, up to max_hops away. Empty result usually means no known cross-repo ties — but check the caveat field first: if the queried repo hasn't been extracted yet, empty means unknown, not confirmed absent. Each result carries the related repo's local_path (its checkout on this machine): if the user's change breaks that repo, offer to update it there too — with their explicit go-ahead, never silently."),
		mcp.WithString("repo", mcp.Required(), mcp.Description("Repo short name (e.g. \"orders-svc\") or identity (e.g. \"github.com/acme/orders-svc\")")),
		mcp.WithNumber("max_hops", mcp.Description("Blast-radius width, default 1. Beyond 2-3 gets slow on dense graphs.")),
	), func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		repo, err := req.RequireString("repo")
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		rels, err := st.GetRelatedRepos(ctx, repo, req.GetInt("max_hops", 1))
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		return asJSON(newQueryResponse(rels, repoFreshnessCaveat(ctx, st, repo)), nil)
	})

	s.AddTool(mcp.NewTool("get_callers_of",
		mcp.WithDescription("Reverse lookup: repos that call an endpoint (\"POST /orders\", \"/orders\", \"OrderService/Create\"), import a package path, or reference an exported symbol. Every result carries its edge_type and the caller's local_path (its checkout on this machine) — before changing or removing the target, surface the callers to the user and offer to update them at their local_path too, with their explicit go-ahead. Check the caveat field before reading an empty result as confirmed absence — it flags when part of the graph hasn't been extracted."),
		mcp.WithString("target", mcp.Required(), mcp.Description("Endpoint (optionally method-prefixed), exported symbol, or package path")),
	), func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		target, err := req.RequireString("target")
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		callers, err := st.GetCallersOf(ctx, target)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		return asJSON(newQueryResponse(callers, graphGapsCaveat(ctx, st)), nil)
	})

	s.AddTool(mcp.NewTool("get_schema_dependents",
		mcp.WithDescription("Repos that read/write a shared schema (table or proto message) by name, with access_mode read|write|read_write and each dependent's local_path (its checkout on this machine) — before a breaking schema change, surface the dependents to the user and offer to update them at their local_path too, with their explicit go-ahead. Check the caveat field before reading an empty result as confirmed absence — it flags when part of the graph hasn't been extracted."),
		mcp.WithString("schema", mcp.Required(), mcp.Description("Table or message name, e.g. \"orders\"")),
	), func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		schema, err := req.RequireString("schema")
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		deps, err := st.GetSchemaDependents(ctx, schema)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		return asJSON(newQueryResponse(deps, graphGapsCaveat(ctx, st)), nil)
	})

	s.AddTool(mcp.NewTool("record_relation",
		mcp.WithDescription("Record a cross-repo relationship you VERIFIED in code that extraction can't see (URL built from config, ORM table access). Requires evidence (file:line or snippet). Never record guesses — a false edge poisons every future blast-radius answer."),
		mcp.WithString("from", mcp.Required(), mcp.Description("Repo that depends/calls/reads (short name or identity)")),
		mcp.WithString("to", mcp.Required(), mcp.Description("Repo that owns the target (short name or identity)")),
		mcp.WithString("edge_type", mcp.Required(), mcp.Description("imports | calls_api | shares_schema | calls_symbol")),
		mcp.WithString("detail", mcp.Required(), mcp.Description("What exactly: import path, \"POST /orders\", table name, or exported symbol name")),
		mcp.WithString("access_mode", mcp.Description("shares_schema only: read | write | read_write (default read)")),
		mcp.WithString("evidence", mcp.Required(), mcp.Description("Where you saw it: file:line and/or the code snippet")),
	), func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		get := func(k string) string { return req.GetString(k, "") }
		err := st.RecordRelation(ctx, get("from"), get("to"), get("edge_type"),
			get("detail"), get("access_mode"), get("evidence"))
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		return mcp.NewToolResultText(`{"recorded": true, "source": "agent"}`), nil
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
		mcp.WithDescription("Full rebuild: re-extract every registered repo and repopulate all nodes/edges. Safe to rerun; not incremental. The result's progress_log lists per-repo outcomes (✓ extracted with counts, ✗ failed with reason) — relay it so the user sees which repos are covered."),
	), func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		// MCP tools can't stream, so the per-repo progress lines ride along
		// in the result for the agent to show.
		var log []string
		res, err := st.BuildAll(ctx, func(line string) { log = append(log, line) })
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		return asJSON(struct {
			graph.BuildResult
			ProgressLog []string `json:"progress_log"`
		}{res, log}, nil)
	})

	s.AddTool(mcp.NewTool("visualize_graph",
		mcp.WithDescription("Open the interactive cross-repo graph in the user's browser (force-directed, zoom/pan, click nodes for details). Starts a local server on 127.0.0.1 if one isn't already running and returns the URL — relay it to the user."),
		mcp.WithNumber("port", mcp.Description("Port for the local page, default 7333")),
	), func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		port := req.GetInt("port", 7333)
		// A bare dial can't tell a greybeard from an unrelated app squatting
		// on the port, so probe /healthz. Any greybeard counts as running —
		// it re-reads the store per request, and versions routinely differ
		// mid-session (the binary self-updates under long-lived processes);
		// spawning a sibling per version would leak a server on every call.
		client := &http.Client{Timeout: 500 * time.Millisecond}
		healthz := func(url string) (body string, free bool) {
			resp, err := client.Get(url + "/healthz")
			if err != nil {
				// Only connection-refused proves nothing is listening. A
				// timeout or a garbled response is some other process — the
				// port is taken, never a spawn target.
				return "", errors.Is(err, syscall.ECONNREFUSED)
			}
			defer resp.Body.Close()
			b, _ := io.ReadAll(io.LimitReader(resp.Body, 128))
			return strings.TrimSpace(string(b)), false
		}
		for p := port; p < port+10; p++ {
			url := fmt.Sprintf("http://127.0.0.1:%d", p)
			body, free := healthz(url)
			if strings.HasPrefix(body, "greybeard") {
				note := ""
				if body != "greybeard "+version {
					note = `, "note": "server is from an older greybeard — data shown is live, but restart it to get the newest page"`
				}
				return mcp.NewToolResultText(fmt.Sprintf(`{"url": %q, "status": "already running"%s}`, url, note)), nil
			}
			if !free {
				continue // some other app owns this port — leave it alone
			}
			exe, err := os.Executable()
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			c := exec.Command(exe, "visualize", "--port", strconv.Itoa(p))
			c.Stdout, c.Stderr = nil, nil
			spawn.Detach(c) // outlives this MCP server; user closes it from the terminal or it dies with logout
			if err := c.Start(); err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			for i := 0; i < 20; i++ { // wait for the listener, max ~2s
				if body, _ := healthz(url); strings.HasPrefix(body, "greybeard") {
					return mcp.NewToolResultText(fmt.Sprintf(`{"url": %q, "status": "started", "note": "opened in the default browser; page reloads reflect the live graph"}`, url)), nil
				}
				time.Sleep(100 * time.Millisecond)
			}
			return mcp.NewToolResultError(fmt.Sprintf("spawned `greybeard visualize --port %d` but it never answered /healthz — check if the process is running", p)), nil
		}
		return mcp.NewToolResultError(fmt.Sprintf("ports %d-%d are all taken by other processes — pass a different port", port, port+9)), nil
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
