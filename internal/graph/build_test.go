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
		"const q2 = `SELECT * FROM orders`\n" + // distinctive, owned by friend (cross-org, corroborated)
		"const q3 = `SELECT * FROM sessions`\n" + // generic, owned by friend (corroborated)
		"const q4 = `SELECT * FROM messages`\n" + // generic, owned by stranger (not corroborated)
		"const q5 = `SELECT * FROM activity`\n" + // distinctive, owned by sibling (same org) AND rival (cross-org)
		"func f() { get(\"/health\"); post(\"/webhooks/execute\") }\n"
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}

	d := declared{
		rec: RepoRecord{Identity: "github.com/mine/me", Name: "me", LocalPath: dir},
		ex: extract.Extraction{
			Tables: []string{"users"}, // declares its own users table
			Deps:   []string{"example.com/friend"},
		},
	}
	rest := []declared{
		{rec: RepoRecord{Identity: "github.com/office/stranger"}, ex: extract.Extraction{
			Tables: []string{"users", "messages"},
		}},
		{rec: RepoRecord{Identity: "github.com/office/friend"}, ex: extract.Extraction{
			Modules: []string{"example.com/friend"},
			Tables:  []string{"orders", "sessions"},
		}},
		{rec: RepoRecord{Identity: "github.com/mine/sibling"}, ex: extract.Extraction{
			Tables: []string{"activity"}, // same org: name alone may link
		}},
		{rec: RepoRecord{Identity: "github.com/office/rival"}, ex: extract.Extraction{
			Tables: []string{"activity"}, // cross-org, uncorroborated: must not link
		}},
		{rec: RepoRecord{Identity: "github.com/office/svc"}, ex: extract.Extraction{
			Endpoints: []extract.Endpoint{
				{Method: "GET", Path: "/health"},
				{Method: "POST", Path: "/webhooks/execute"},
			},
		}},
	}

	p := computeRefs(d, rest)

	if len(p.imports) != 1 || p.imports[0].owner != "github.com/office/friend" {
		t.Errorf("imports = %+v, want one edge to friend", p.imports)
	}

	schemaTo := map[string]string{} // owner -> table
	for _, sc := range p.schemas {
		schemaTo[sc.owner+"|"+sc.name] = sc.mode
	}
	if _, ok := schemaTo["github.com/office/stranger|users"]; ok {
		t.Error("self-declared table must resolve locally, never to a stranger")
	}
	if _, ok := schemaTo["github.com/office/stranger|messages"]; ok {
		t.Error("generic table without corroboration must not link")
	}
	if _, ok := schemaTo["github.com/office/friend|orders"]; !ok {
		t.Errorf("distinctive table with corroboration should link: %+v", p.schemas)
	}
	if _, ok := schemaTo["github.com/office/friend|sessions"]; !ok {
		t.Errorf("generic table WITH an imports edge should link (corroborated): %+v", p.schemas)
	}
	if _, ok := schemaTo["github.com/mine/sibling|activity"]; !ok {
		t.Errorf("distinctive same-org table should link on name alone: %+v", p.schemas)
	}
	if _, ok := schemaTo["github.com/office/rival|activity"]; ok {
		t.Error("cross-org table without corroboration must not link (the lotus-todo bug)")
	}

	callTo := map[string]bool{}
	for _, c := range p.calls {
		callTo[c.owner+"|"+c.path] = true
	}
	if callTo["github.com/office/svc|/health"] {
		t.Error("generic endpoints like /health must never link")
	}
	if !callTo["github.com/office/svc|/webhooks/execute"] {
		t.Errorf("distinctive endpoint should link: %+v", p.calls)
	}
}

// Symbol matching (calls_symbol) follows the exact same precision rules as
// table matching: self-declaration wins, generic names need corroboration,
// distinctive names may link on name alone but only within the same org.
func TestComputeRefsSymbolPrecision(t *testing.T) {
	dir := t.TempDir()
	src := "package x\n" +
		"var c1 = Config{}\n" + // self-declared symbol
		"var c2 = Client{}\n" + // generic, owned by friend (corroborated via imports)
		"var c3 = Handler{}\n" + // generic, owned by stranger (not corroborated)
		"var c4 = ParseOrder()\n" + // distinctive, owned by stranger (not corroborated)
		"var c5 = Widget{}\n" // distinctive, owned by sibling (same org) AND rival (cross-org)
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}

	d := declared{
		rec: RepoRecord{Identity: "github.com/mine/me", Name: "me", LocalPath: dir},
		ex: extract.Extraction{
			Symbols: []string{"Config"}, // declares its own Config symbol
			Deps:    []string{"example.com/friend"},
		},
	}
	rest := []declared{
		{rec: RepoRecord{Identity: "github.com/office/stranger"}, ex: extract.Extraction{
			Symbols: []string{"Config", "Handler", "ParseOrder"},
		}},
		{rec: RepoRecord{Identity: "github.com/office/friend"}, ex: extract.Extraction{
			Modules: []string{"example.com/friend"},
			Symbols: []string{"Client"},
		}},
		{rec: RepoRecord{Identity: "github.com/mine/sibling"}, ex: extract.Extraction{
			Symbols: []string{"Widget"}, // same org: name alone may link
		}},
		{rec: RepoRecord{Identity: "github.com/office/rival"}, ex: extract.Extraction{
			Symbols: []string{"Widget"}, // cross-org, uncorroborated: must not link
		}},
	}

	p := computeRefs(d, rest)

	symTo := map[string]bool{} // owner|name
	for _, sy := range p.symbols {
		symTo[sy.owner+"|"+sy.name] = true
	}
	if symTo["github.com/office/stranger|Config"] {
		t.Error("self-declared symbol must resolve locally, never to a stranger")
	}
	if symTo["github.com/office/stranger|Handler"] {
		t.Error("generic symbol without corroboration must not link")
	}
	if symTo["github.com/office/stranger|ParseOrder"] {
		t.Error("distinctive cross-org symbol without corroboration must not link")
	}
	if !symTo["github.com/office/friend|Client"] {
		t.Errorf("generic symbol WITH an imports edge should link (corroborated): %+v", p.symbols)
	}
	if !symTo["github.com/mine/sibling|Widget"] {
		t.Errorf("distinctive same-org symbol should link on name alone: %+v", p.symbols)
	}
	if symTo["github.com/office/rival|Widget"] {
		t.Error("cross-org symbol without corroboration must not link")
	}
}
