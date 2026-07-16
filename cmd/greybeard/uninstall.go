package main

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// cmdUninstall removes the binary (and with --purge, the graph data). The
// Claude Code plugin is uninstalled separately via /plugin — we print the
// reminder since we can't reach into the agent's config.
func cmdUninstall(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("uninstall", flag.ExitOnError)
	purge := fs.Bool("purge", false, "also delete the graph data (~/.greybeard)")
	yes := fs.Bool("yes", false, "skip the confirmation prompt")
	fs.Parse(args)

	exe, err := os.Executable()
	if err != nil {
		return err
	}
	if exe, err = filepath.EvalSymlinks(exe); err != nil {
		return err
	}
	home, _ := os.UserHomeDir()
	dataDir := filepath.Join(home, ".greybeard")

	fmt.Printf("This removes %s", bold(exe))
	if *purge {
		fmt.Printf(" and %s", bold(dataDir))
	}
	fmt.Println()
	if !*yes {
		fmt.Print("Proceed? [y/N] ")
		line, _ := bufio.NewReader(os.Stdin).ReadString('\n')
		if !strings.HasPrefix(strings.ToLower(strings.TrimSpace(line)), "y") {
			fmt.Println("aborted — nothing removed")
			return nil
		}
	}

	if *purge {
		if err := os.RemoveAll(dataDir); err != nil {
			return err
		}
		fmt.Printf("  %s %s\n", green("✓"), dataDir)
	} else {
		fmt.Printf("  %s graph data kept at %s (rerun with --purge to remove)\n", grey("·"), dataDir)
	}

	// The binary goes last — the point of no return.
	if runtime.GOOS == "windows" {
		// Windows can't delete a running exe; park it out of PATH instead.
		if err := os.Rename(exe, exe+".uninstalled"); err != nil {
			return permissionHint(err, exe)
		}
		fmt.Printf("  %s %s renamed to .uninstalled — delete it after this window closes\n", green("✓"), exe)
	} else {
		if err := os.Remove(exe); err != nil {
			return permissionHint(err, exe) // suggests sudo when /usr/local/bin needs it
		}
		fmt.Printf("  %s %s\n", green("✓"), exe)
	}

	fmt.Printf("\n%s the old man forgets you. If the Claude Code plugin is installed, remove it with %s\n",
		grey("🧔"), bold("/plugin uninstall greybeard@greybeard"))
	fmt.Println("Note: other copies on PATH (e.g. ~/go/bin/greybeard from `go install`) are not touched.")
	return nil
}
