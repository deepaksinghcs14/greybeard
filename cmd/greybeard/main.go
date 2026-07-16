// greybeard: local cross-repo dependency graph for AI coding agents.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/deepaksinghcs14/greybeard/internal/discover"
	"github.com/deepaksinghcs14/greybeard/internal/graph"
	"github.com/deepaksinghcs14/greybeard/internal/mcp"
)

// version is stamped by goreleaser via -ldflags "-X main.version=..." on
// tagged releases; "dev" means a from-source build (go install / go build).
var version = "dev"

const usage = `greybeard — he remembers what your repos forgot

Usage:
  greybeard init --root <path>       scan a tree for git repos and register them
  greybeard build                    full extraction across all registered repos
  greybeard serve                    MCP server over stdio
  greybeard visualize [--port 7333]  open the graph in your browser
  greybeard update                   self-update to the latest release
  greybeard check --cwd <path>       session-start freshness check (used by hooks)

Configuration:
  GREYBEARD_DB           graph database file (default ~/.greybeard/graph.db)
  GREYBEARD_STALE_AFTER  reindex threshold for check, e.g. 24h (default)
  GREYBEARD_AUTO_UPDATE  set to "off" to stop check from self-updating daily
`

func main() {
	if len(os.Args) < 2 {
		fmt.Fprint(os.Stderr, banner()+usage)
		os.Exit(2)
	}
	ctx := context.Background()
	var err error
	switch os.Args[1] {
	case "init":
		err = cmdInit(ctx, os.Args[2:])
	case "build":
		err = cmdBuild(ctx)
	case "serve":
		err = cmdServe(ctx)
	case "visualize":
		err = cmdVisualize(ctx, os.Args[2:])
	case "update":
		err = cmdUpdate(ctx, os.Args[2:])
	case "check":
		err = cmdCheck(ctx, os.Args[2:])
	case "reindex":
		// internal: spawned detached by `check` for background extraction
		err = cmdReindex(ctx, os.Args[2:])
	case "version", "--version":
		fmt.Print(banner())
	default:
		fmt.Fprint(os.Stderr, banner()+usage)
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "greybeard:", err)
		os.Exit(1)
	}
}

func cmdInit(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("init", flag.ExitOnError)
	root := fs.String("root", "", "folder to scan for git repos")
	fs.Parse(args)
	if *root == "" {
		return fmt.Errorf("init: --root is required")
	}
	st, err := graph.Open(ctx)
	if err != nil {
		return err
	}
	defer st.Close()
	fmt.Printf("%s scanning %s for repos…\n", grey("🧔"), bold(*root))
	repos, err := discover.ScanRoot(*root)
	if err != nil {
		return err
	}
	for _, r := range repos {
		if err := st.UpsertRepo(ctx, r); err != nil {
			return err
		}
		fmt.Printf("  %s %s %s\n", green("✓"), bold(r.Name), dim(r.Identity))
	}
	fmt.Printf("\n%s repos registered — run %s for the first extraction\n",
		bold(fmt.Sprint(len(repos))), bold("greybeard build"))
	return nil
}

func cmdBuild(ctx context.Context) error {
	st, err := graph.Open(ctx)
	if err != nil {
		return err
	}
	defer st.Close()
	start := time.Now()
	res, err := st.BuildAll(ctx, func(line string) {
		if strings.HasPrefix(line, "✓") || strings.HasPrefix(line, "✗") {
			fmt.Println("  " + glyph(line))
		} else {
			fmt.Println(grey("🧔 ") + line)
		}
	})
	if err != nil {
		return err
	}
	fmt.Printf("\n%s %s repos · %s nodes · %s edges %s\n",
		grey("🧔 done."),
		bold(fmt.Sprint(res.ReposProcessed)), bold(fmt.Sprint(res.Nodes)), bold(fmt.Sprint(res.Edges)),
		dim("("+time.Since(start).Round(time.Millisecond).String()+")"))
	if len(res.Failed) > 0 {
		fmt.Printf("%s %d repos had extraction problems (listed above) — they're partially covered\n",
			red("!"), len(res.Failed))
	}
	return nil
}

func cmdServe(ctx context.Context) error {
	st, err := graph.Open(ctx)
	if err != nil {
		return err
	}
	defer st.Close()
	return mcp.Serve(ctx, st, version)
}

// cmdCheck is the session-start hook: fast, silent, and non-blocking. Any
// failure (no DB, not a git repo) exits 0 quietly — a hook must never break
// or spam an agent session.
func cmdCheck(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("check", flag.ExitOnError)
	cwd := fs.String("cwd", ".", "repo path to check")
	fs.Parse(args)

	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()

	maybeAutoUpdate() // throttled to daily; detached and silent

	repo, err := discover.RepoAt(*cwd)
	if err != nil {
		return nil
	}
	st, err := graph.Open(ctx)
	if err != nil {
		return nil
	}
	defer st.Close()
	rec, err := st.GetRepo(ctx, repo.Identity)
	if err != nil {
		return nil
	}
	if rec != nil && !rec.Stale(graph.StaleAfter()) {
		return nil // registered and fresh: no-op
	}
	if err := st.UpsertRepo(ctx, repo); err != nil {
		return nil
	}
	// A goroutine would die when this short-lived CLI exits, so background
	// extraction runs as a detached child process instead.
	exe, err := os.Executable()
	if err != nil {
		return nil
	}
	c := exec.Command(exe, "reindex", "--cwd", repo.LocalPath)
	c.Stdout, c.Stderr = nil, nil
	_ = c.Start() // deliberately not Wait()ed — check returns immediately
	return nil
}

func cmdReindex(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("reindex", flag.ExitOnError)
	cwd := fs.String("cwd", ".", "repo path to reindex")
	fs.Parse(args)
	repo, err := discover.RepoAt(*cwd)
	if err != nil {
		return err
	}
	st, err := graph.Open(ctx)
	if err != nil {
		return err
	}
	defer st.Close()
	return st.Reindex(ctx, repo)
}
