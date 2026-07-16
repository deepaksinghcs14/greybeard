// Package discover finds git repos on disk and resolves their graph identity.
package discover

import (
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"strings"
)

// Repo is a discovered git repository.
type Repo struct {
	Identity  string // normalized remote URL, or absolute local path if no remote
	Name      string // short display name (last path segment of the identity)
	RemoteURL string // normalized remote URL, "" if none
	LocalPath string // absolute path of this clone
}

// NormalizeRemote canonicalizes a git remote URL so the same repo reached via
// ssh, https, or scp-style syntax resolves to one identity: "host/org/repo"
// (lowercased host, no scheme, no user, no port, no .git suffix).
func NormalizeRemote(raw string) string {
	s := strings.TrimSpace(raw)
	if s == "" {
		return ""
	}
	s = strings.TrimSuffix(s, "/")
	s = strings.TrimSuffix(s, ".git")

	if strings.Contains(s, "://") {
		u, err := url.Parse(s)
		if err != nil || u.Hostname() == "" {
			return s
		}
		return strings.ToLower(u.Hostname()) + strings.TrimSuffix(u.Path, "/")
	}

	// scp-like syntax: [user@]host:org/repo
	if at := strings.Index(s, "@"); at >= 0 {
		s = s[at+1:]
	}
	if colon := strings.Index(s, ":"); colon >= 0 {
		return strings.ToLower(s[:colon]) + "/" + strings.TrimPrefix(s[colon+1:], "/")
	}
	return s
}

// RepoAt resolves the repo containing (exactly at) dir. Returns an error if
// dir has no .git directory.
func RepoAt(dir string) (Repo, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return Repo{}, err
	}
	gitDir := filepath.Join(abs, ".git")
	if fi, err := os.Stat(gitDir); err != nil || !fi.IsDir() {
		return Repo{}, fmt.Errorf("%s is not a git repo root (no .git directory)", abs)
	}
	remote := NormalizeRemote(remoteURL(gitDir))
	identity := remote
	if identity == "" {
		identity = abs
	}
	return Repo{
		Identity:  identity,
		Name:      filepath.Base(strings.TrimSuffix(identity, "/")),
		RemoteURL: remote,
		LocalPath: abs,
	}, nil
}

// remoteURL reads the remote URL from .git/config without shelling out to
// git, preferring "origin", else the first remote found. Returns "" if none.
func remoteURL(gitDir string) string {
	b, err := os.ReadFile(filepath.Join(gitDir, "config"))
	if err != nil {
		return ""
	}
	var section, originURL, firstURL string
	for _, line := range strings.Split(string(b), "\n") {
		t := strings.TrimSpace(line)
		if strings.HasPrefix(t, "[") {
			section = t
			continue
		}
		if !strings.HasPrefix(section, `[remote `) {
			continue
		}
		k, v, ok := strings.Cut(t, "=")
		if !ok || strings.TrimSpace(k) != "url" {
			continue
		}
		u := strings.TrimSpace(v)
		if firstURL == "" {
			firstURL = u
		}
		if section == `[remote "origin"]` && originURL == "" {
			originURL = u
		}
	}
	if originURL != "" {
		return originURL
	}
	return firstURL
}

// ScanRoot walks root for .git directories and returns every repo found.
// Unreadable subtrees are skipped, not fatal — but a root that doesn't exist
// is an error, not "0 repos found".
func ScanRoot(root string) ([]Repo, error) {
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	if fi, err := os.Stat(abs); err != nil || !fi.IsDir() {
		return nil, fmt.Errorf("root %s is not a directory", abs)
	}
	var repos []Repo
	err = filepath.WalkDir(abs, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil // skip unreadable entries
		}
		if !d.IsDir() {
			return nil
		}
		name := d.Name()
		if name == ".git" {
			if r, err := RepoAt(filepath.Dir(path)); err == nil {
				repos = append(repos, r)
			}
			return fs.SkipDir
		}
		// Don't descend into dependency trees or other hidden dirs.
		if name == "node_modules" || name == "vendor" || (strings.HasPrefix(name, ".") && path != abs) {
			return fs.SkipDir
		}
		return nil
	})
	return repos, err
}
