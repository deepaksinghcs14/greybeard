package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

const releaseRepo = "deepaksinghcs14/greybeard"

// cmdUpdate replaces the running binary with the latest GitHub release.
func cmdUpdate(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("update", flag.ExitOnError)
	quiet := fs.Bool("quiet", false, "no output (used by the background auto-update)")
	fs.Parse(args)
	say := func(format string, a ...any) {
		if !*quiet {
			fmt.Printf(format+"\n", a...)
		}
	}

	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()

	latest, err := latestReleaseTag(ctx)
	if err != nil {
		return fmt.Errorf("checking latest release: %w", err)
	}
	if latest == version {
		say("greybeard %s is already the latest", version)
		return nil
	}

	asset := "greybeard_" + runtime.GOOS + "_" + runtime.GOARCH
	if runtime.GOOS == "windows" {
		asset += ".exe"
	}
	url := "https://github.com/" + releaseRepo + "/releases/latest/download/" + asset

	exe, err := os.Executable()
	if err != nil {
		return err
	}
	if exe, err = filepath.EvalSymlinks(exe); err != nil {
		return err
	}

	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download %s: %s", url, resp.Status)
	}

	// Stage next to the target so the final rename is atomic (same filesystem).
	staged := exe + ".new"
	f, err := os.OpenFile(staged, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755)
	if err != nil {
		return permissionHint(err, exe)
	}
	if _, err := io.Copy(f, resp.Body); err != nil {
		f.Close()
		os.Remove(staged)
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if runtime.GOOS == "windows" {
		// Windows can't rename over a running exe; park the old one instead.
		os.Remove(exe + ".old")
		if err := os.Rename(exe, exe+".old"); err != nil {
			os.Remove(staged)
			return permissionHint(err, exe)
		}
	}
	if err := os.Rename(staged, exe); err != nil {
		if runtime.GOOS == "windows" {
			// roll the parked binary back so PATH never ends up empty
			os.Rename(exe+".old", exe)
		}
		os.Remove(staged)
		return permissionHint(err, exe)
	}
	say("updated greybeard %s → %s", version, latest)
	return nil
}

func latestReleaseTag(ctx context.Context) (string, error) {
	req, err := http.NewRequestWithContext(ctx, "GET",
		"https://api.github.com/repos/"+releaseRepo+"/releases/latest", nil)
	if err != nil {
		return "", err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("GitHub API: %s", resp.Status)
	}
	var rel struct {
		TagName string `json:"tag_name"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&rel); err != nil {
		return "", err
	}
	tag := strings.TrimPrefix(rel.TagName, "v")
	if tag == "" {
		return "", fmt.Errorf("no releases found for %s", releaseRepo)
	}
	return tag, nil
}

func permissionHint(err error, exe string) error {
	if os.IsPermission(err) {
		return fmt.Errorf("%w\ncannot write %s — retry with: sudo greybeard update", err, exe)
	}
	return err
}

// maybeAutoUpdate is called by the session-start hook path (`greybeard
// check`): at most once per 24h it spawns a detached, silent self-update, so
// agents (Claude Code, Codex) keep the binary current without anyone running
// `update` by hand. Disable with GREYBEARD_AUTO_UPDATE=off.
func maybeAutoUpdate() {
	if os.Getenv("GREYBEARD_AUTO_UPDATE") == "off" || version == "dev" {
		return // from-source builds are the developer's own business
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return
	}
	marker := filepath.Join(home, ".greybeard", "last-update-check")
	if fi, err := os.Stat(marker); err == nil && time.Since(fi.ModTime()) < 24*time.Hour {
		return
	}
	if os.MkdirAll(filepath.Dir(marker), 0o755) != nil {
		return
	}
	// Touch before spawning so a failing update doesn't retry every session.
	if os.WriteFile(marker, nil, 0o644) != nil {
		return
	}
	exe, err := os.Executable()
	if err != nil {
		return
	}
	c := exec.Command(exe, "update", "--quiet")
	c.Stdout, c.Stderr = nil, nil
	detach(c)
	_ = c.Start() // detached, same pattern as background reindex
}
