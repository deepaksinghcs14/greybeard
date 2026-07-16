package graph

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/deepaksinghcs14/greybeard/internal/extract"
)

// computeRefs is pure (no DB), so the precision rules get direct coverage:
// same name is not the same thing.
func TestComputeRefsPrecision(t *testing.T) {
	dir := t.TempDir()
	src := "package x\n" +
		"const q1 = `SELECT * FROM users`\n" + // self-declared table
		"const q2 = `SELECT * FROM orders`\n" + // distinctive, owned by friend
		"const q3 = `SELECT * FROM sessions`\n" + // generic, owned by friend (corroborated)
		"const q4 = `SELECT * FROM messages`\n" + // generic, owned by stranger (not corroborated)
		"func f() { get(\"/health\"); post(\"/webhooks/execute\") }\n"
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}

	d := declared{
		rec: RepoRecord{Identity: "me", Name: "me", LocalPath: dir},
		ex: extract.Extraction{
			Tables: []string{"users"}, // declares its own users table
			Deps:   []string{"example.com/friend"},
		},
	}
	rest := []declared{
		{rec: RepoRecord{Identity: "stranger"}, ex: extract.Extraction{
			Tables: []string{"users", "messages"},
		}},
		{rec: RepoRecord{Identity: "friend"}, ex: extract.Extraction{
			Modules: []string{"example.com/friend"},
			Tables:  []string{"orders", "sessions"},
		}},
		{rec: RepoRecord{Identity: "svc"}, ex: extract.Extraction{
			Endpoints: []extract.Endpoint{
				{Method: "GET", Path: "/health"},
				{Method: "POST", Path: "/webhooks/execute"},
			},
		}},
	}

	p := computeRefs(d, rest)

	if len(p.imports) != 1 || p.imports[0].owner != "friend" {
		t.Errorf("imports = %+v, want one edge to friend", p.imports)
	}

	schemaTo := map[string]string{} // owner -> table
	for _, sc := range p.schemas {
		schemaTo[sc.owner+"|"+sc.name] = sc.mode
	}
	if _, ok := schemaTo["stranger|users"]; ok {
		t.Error("self-declared table must resolve locally, never to a stranger")
	}
	if _, ok := schemaTo["stranger|messages"]; ok {
		t.Error("generic table without corroboration must not link")
	}
	if _, ok := schemaTo["friend|orders"]; !ok {
		t.Errorf("distinctive table should link: %+v", p.schemas)
	}
	if _, ok := schemaTo["friend|sessions"]; !ok {
		t.Errorf("generic table WITH an imports edge should link (corroborated): %+v", p.schemas)
	}

	callTo := map[string]bool{}
	for _, c := range p.calls {
		callTo[c.owner+"|"+c.path] = true
	}
	if callTo["svc|/health"] {
		t.Error("generic endpoints like /health must never link")
	}
	if !callTo["svc|/webhooks/execute"] {
		t.Errorf("distinctive endpoint should link: %+v", p.calls)
	}
}
