package discover

import (
	"os"
	"path/filepath"
	"testing"
)

func TestNormalizeRemote(t *testing.T) {
	cases := map[string]string{
		"git@github.com:acme/orders-svc.git":            "github.com/acme/orders-svc",
		"https://github.com/acme/orders-svc.git":        "github.com/acme/orders-svc",
		"https://github.com/acme/orders-svc":            "github.com/acme/orders-svc",
		"https://github.com/acme/orders-svc/":           "github.com/acme/orders-svc",
		"ssh://git@github.com/acme/orders-svc.git":      "github.com/acme/orders-svc",
		"ssh://git@github.com:2222/acme/orders-svc.git": "github.com/acme/orders-svc",
		"git@GitHub.com:acme/orders-svc.git":            "github.com/acme/orders-svc",
		"https://user@github.com/acme/orders-svc.git":   "github.com/acme/orders-svc",
		"": "",
	}
	for in, want := range cases {
		if got := NormalizeRemote(in); got != want {
			t.Errorf("NormalizeRemote(%q) = %q, want %q", in, got, want)
		}
	}
}

func writeGitConfig(t *testing.T, dir, remoteURL string) {
	t.Helper()
	gitDir := filepath.Join(dir, ".git")
	if err := os.MkdirAll(gitDir, 0o755); err != nil {
		t.Fatal(err)
	}
	config := "[core]\n\trepositoryformatversion = 0\n"
	if remoteURL != "" {
		config += "[remote \"origin\"]\n\turl = " + remoteURL + "\n\tfetch = +refs/heads/*:refs/remotes/origin/*\n"
	}
	if err := os.WriteFile(filepath.Join(gitDir, "config"), []byte(config), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestSameRepoDifferentClonesAndURLFormats(t *testing.T) {
	// Same repo cloned twice: once via ssh, once via https, different paths.
	a := t.TempDir()
	b := t.TempDir()
	writeGitConfig(t, a, "git@github.com:acme/orders-svc.git")
	writeGitConfig(t, b, "https://github.com/acme/orders-svc.git")

	ra, err := RepoAt(a)
	if err != nil {
		t.Fatal(err)
	}
	rb, err := RepoAt(b)
	if err != nil {
		t.Fatal(err)
	}
	if ra.Identity != rb.Identity {
		t.Errorf("same repo resolved to two identities: %q vs %q", ra.Identity, rb.Identity)
	}
	if ra.Identity != "github.com/acme/orders-svc" {
		t.Errorf("identity = %q", ra.Identity)
	}
	if ra.Name != "orders-svc" {
		t.Errorf("name = %q", ra.Name)
	}
	if ra.LocalPath == rb.LocalPath {
		t.Error("local paths should differ")
	}
}

func TestRepoWithoutRemoteFallsBackToPath(t *testing.T) {
	dir := t.TempDir()
	writeGitConfig(t, dir, "")
	r, err := RepoAt(dir)
	if err != nil {
		t.Fatal(err)
	}
	abs, _ := filepath.Abs(dir)
	if r.Identity != abs {
		t.Errorf("identity = %q, want local path %q", r.Identity, abs)
	}
	if r.RemoteURL != "" {
		t.Errorf("remote should be empty, got %q", r.RemoteURL)
	}
}

func TestRepoAtNonRepo(t *testing.T) {
	if _, err := RepoAt(t.TempDir()); err == nil {
		t.Fatal("expected error for a dir with no .git")
	}
}

func TestScanRoot(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"one", "nested/two"} {
		dir := filepath.Join(root, name)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		writeGitConfig(t, dir, "git@example.com:acme/"+filepath.Base(name)+".git")
	}
	// A non-repo dir and a node_modules subtree should be ignored.
	os.MkdirAll(filepath.Join(root, "not-a-repo"), 0o755)
	os.MkdirAll(filepath.Join(root, "one", "node_modules", "dep", ".git"), 0o755)

	repos, err := ScanRoot(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(repos) != 2 {
		t.Fatalf("found %d repos, want 2: %+v", len(repos), repos)
	}
}
