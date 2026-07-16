// Package extract parses a repo's manifests into declared surface: packages,
// endpoints, and schemas. Pure parsing — no database access.
package extract

import (
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"golang.org/x/mod/modfile"
	"gopkg.in/yaml.v3"
)

// Endpoint is a declared API endpoint (HTTP path from OpenAPI, or gRPC
// Service/Method from proto).
type Endpoint struct {
	Method string // "GET", "POST", ... or "GRPC"
	Path   string // "/orders" or "OrderService/CreateOrder"
}

// Extraction is everything a repo declares.
type Extraction struct {
	Modules   []string // go module path and/or npm package name
	Deps      []string // declared dependency import paths / package names
	Endpoints []Endpoint
	Tables    []string // tables created by SQL migrations
	Messages  []string // proto message names
	Notes     string   // llms.txt-style human-readable description, if present
	Errors    []string // per-file parse failures (logged, never fatal)
}

var httpMethods = map[string]bool{
	"get": true, "post": true, "put": true, "patch": true, "delete": true, "head": true, "options": true,
}

// qualPrefix optionally matches a schema qualifier like `public.` or "app".
const qualPrefix = `(?:["'` + "`" + `]?[A-Za-z_]\w*["'` + "`" + `]?\.)?`

var (
	// captures the TABLE name, not the schema qualifier: CREATE TABLE
	// public.orders must record "orders", never "public".
	createTableRe  = regexp.MustCompile(`(?i)CREATE\s+TABLE\s+(?:IF\s+NOT\s+EXISTS\s+)?` + qualPrefix + `["'` + "`" + `]?([A-Za-z_]\w*)`)
	protoServiceRe = regexp.MustCompile(`^\s*service\s+(\w+)`)
	protoRPCRe     = regexp.MustCompile(`^\s*rpc\s+(\w+)`)
	protoMessageRe = regexp.MustCompile(`^\s*message\s+(\w+)`)
)

// Repo extracts the declared surface of the repo at root. Missing or
// unparseable files are recorded in Errors and skipped.
func Repo(root string) Extraction {
	var ex Extraction

	if b, err := os.ReadFile(filepath.Join(root, "go.mod")); err == nil {
		if f, err := modfile.Parse("go.mod", b, nil); err != nil {
			ex.Errors = append(ex.Errors, "go.mod: "+err.Error())
		} else {
			if f.Module != nil {
				ex.Modules = append(ex.Modules, f.Module.Mod.Path)
			}
			for _, r := range f.Require {
				if !r.Indirect { // transitive deps are not a direct imports edge
					ex.Deps = append(ex.Deps, r.Mod.Path)
				}
			}
		}
	}

	if b, err := os.ReadFile(filepath.Join(root, "package.json")); err == nil {
		var pkg struct {
			Name         string            `json:"name"`
			Dependencies map[string]string `json:"dependencies"`
			DevDeps      map[string]string `json:"devDependencies"`
		}
		if err := json.Unmarshal(b, &pkg); err != nil {
			ex.Errors = append(ex.Errors, "package.json: "+err.Error())
		} else {
			if pkg.Name != "" {
				ex.Modules = append(ex.Modules, pkg.Name)
			}
			for d := range pkg.Dependencies {
				ex.Deps = append(ex.Deps, d)
			}
			for d := range pkg.DevDeps {
				ex.Deps = append(ex.Deps, d)
			}
		}
	}

	if b, err := os.ReadFile(filepath.Join(root, "llms.txt")); err == nil {
		ex.Notes = strings.TrimSpace(string(b))
		if len(ex.Notes) > 1000 {
			ex.Notes = ex.Notes[:1000]
		}
	}

	walkSources(root, maxDeclaredFileSize, func(path string) {
		base := strings.ToLower(filepath.Base(path))
		ext := filepath.Ext(base)
		switch {
		case strings.HasSuffix(base, ".proto"):
			eps, msgs := parseProto(path)
			ex.Endpoints = append(ex.Endpoints, eps...)
			ex.Messages = append(ex.Messages, msgs...)
		case strings.HasSuffix(base, ".sql") && strings.Contains(strings.ToLower(path), "migrat"):
			b, err := os.ReadFile(path)
			if err != nil {
				return
			}
			for _, m := range createTableRe.FindAllStringSubmatch(string(b), -1) {
				ex.Tables = append(ex.Tables, m[1])
			}
		// ponytail: OpenAPI specs found by filename convention (openapi*/swagger*),
		// not by parsing every YAML in the tree; add content sniffing if specs
		// under other names turn out to be common. Spec extensions only, and
		// never swagger-ui assets — those are vendored JS, not specs.
		case (strings.HasPrefix(base, "openapi") || strings.HasPrefix(base, "swagger")) &&
			(ext == ".yaml" || ext == ".yml" || ext == ".json") &&
			!strings.Contains(base, "-ui"):
			eps, err := parseOpenAPI(path)
			if err != nil {
				ex.Errors = append(ex.Errors, filepath.Base(path)+": "+err.Error())
				return
			}
			ex.Endpoints = append(ex.Endpoints, eps...)
		}
	})

	ex.Tables = dedupe(ex.Tables)
	ex.Messages = dedupe(ex.Messages)
	return ex
}

// parseOpenAPI pulls path+method pairs out of an OpenAPI/Swagger spec
// (YAML or JSON — yaml.v3 parses both).
func parseOpenAPI(path string) ([]Endpoint, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var doc struct {
		Paths map[string]map[string]any `yaml:"paths"`
	}
	if err := yaml.Unmarshal(b, &doc); err != nil {
		return nil, err
	}
	var eps []Endpoint
	for p, ops := range doc.Paths {
		for method := range ops {
			if httpMethods[strings.ToLower(method)] {
				eps = append(eps, Endpoint{Method: strings.ToUpper(method), Path: p})
			}
		}
	}
	return eps, nil
}

// parseProto line-scans a .proto file for service RPCs and message names.
// ponytail: no real proto parser — a line scan covers standard formatting;
// swap in protoparse if repos with exotic layouts show up.
func parseProto(path string) ([]Endpoint, []string) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, nil
	}
	var eps []Endpoint
	var msgs []string
	service := ""
	for _, line := range strings.Split(string(b), "\n") {
		if m := protoServiceRe.FindStringSubmatch(line); m != nil {
			service = m[1]
		} else if m := protoRPCRe.FindStringSubmatch(line); m != nil && service != "" {
			eps = append(eps, Endpoint{Method: "GRPC", Path: service + "/" + m[1]})
		} else if m := protoMessageRe.FindStringSubmatch(line); m != nil {
			msgs = append(msgs, m[1])
		}
	}
	return eps, msgs
}

// genericTables are table names so common that a name collision means
// nothing: two unrelated projects both having a "users" table is the norm,
// not a shared schema. These only link repos that already have another
// relationship (an imports or calls_api edge) to corroborate the claim.
var genericTables = map[string]bool{
	"users": true, "accounts": true, "sessions": true, "messages": true,
	"events": true, "logs": true, "settings": true, "jobs": true, "tasks": true,
	"notifications": true, "roles": true, "permissions": true, "tags": true,
	"comments": true, "files": true, "tokens": true, "config": true,
	"metadata": true, "items": true, "migrations": true, "schema_migrations": true,
	"audit_log": true, "cache": true, "queue": true, "state": true,
	"status": true, "history": true, "versions": true,
}

// GenericTable reports whether a table name is too common to link repos on
// the name alone.
func GenericTable(name string) bool { return genericTables[strings.ToLower(name)] }

// genericPaths are endpoints every service declares; a match on them says
// nothing about who calls whom.
var genericPaths = map[string]bool{
	"/health": true, "/healthz": true, "/status": true, "/metrics": true,
	"/ready": true, "/readyz": true, "/live": true, "/livez": true,
	"/ping": true, "/version": true, "/info": true, "/favicon.ico": true,
}

// GenericPath reports whether an endpoint path is too universal to link on.
func GenericPath(p string) bool {
	p = strings.ToLower(strings.TrimSuffix(p, "/"))
	return p == "" || genericPaths[p]
}

// sourceExts are the file types scanned for cross-repo references.
var sourceExts = map[string]bool{
	".go": true, ".js": true, ".ts": true, ".tsx": true, ".jsx": true, ".py": true,
	".rb": true, ".java": true, ".kt": true, ".cs": true, ".php": true, ".rs": true,
	".sql": true, ".yaml": true, ".yml": true, ".json": true, ".proto": true, ".sh": true,
}

// manifest files declare deps rather than use them — excluded from reference
// scans so a module path like "example.com/orders-svc" doesn't read as a
// schema/API reference.
var manifestFiles = map[string]bool{
	"go.mod": true, "go.sum": true, "package.json": true,
	"package-lock.json": true, "yarn.lock": true, "pnpm-lock.yaml": true,
}

const (
	maxScanFileSize     = 512 * 1024      // cross-repo reference scanning
	maxDeclaredFileSize = 8 * 1024 * 1024 // declared surface (generated specs get big)
)

// ScanRefs walks the repo's source files once and reports which needles occur.
// Matching is contextual, not bare name matching — a table name in a comment
// or an unrelated identifier must not become a graph edge:
//   - paths (endpoints): substring match, but only on lines that contain a
//     string-literal quote — code calls URLs in quoted strings, prose doesn't
//   - tables: word match preceded by a SQL keyword; FROM/JOIN report "read",
//     INSERT INTO/UPDATE/DELETE FROM/CREATE TABLE report "write", both
//     report "read_write" (qualified names like public.orders match too)
//   - messages (proto): word match in .proto files only, where sharing a
//     message is an actual contract
//
// ponytail: still text-level, no per-language AST — treat hits as "worth
// checking", not proof; that ceiling is documented in the README.
func ScanRefs(root string, paths, tables, messages []string) (pathHits map[string]bool, tableHits map[string]string, messageHits map[string]bool) {
	pathHits = map[string]bool{}
	tableHits = map[string]string{}
	messageHits = map[string]bool{}
	nameRe := func(kw, t string) *regexp.Regexp {
		return regexp.MustCompile(`(?i)\b(` + kw + `)[\s"'` + "`" + `]+` + qualPrefix + `["'` + "`" + `]?` + regexp.QuoteMeta(t) + `\b`)
	}
	readRes := make(map[string]*regexp.Regexp, len(tables))
	writeRes := make(map[string]*regexp.Regexp, len(tables))
	for _, t := range tables {
		readRes[t] = nameRe(`from|join`, t)
		writeRes[t] = nameRe(`insert\s+into|update|delete\s+from|merge\s+into|create\s+table(\s+if\s+not\s+exists)?`, t)
	}
	messageRes := make(map[string]*regexp.Regexp, len(messages))
	for _, m := range messages {
		messageRes[m] = regexp.MustCompile(`\b` + regexp.QuoteMeta(m) + `\b`)
	}
	walkSources(root, maxScanFileSize, func(path string) {
		if manifestFiles[filepath.Base(path)] {
			return
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return
		}
		s := string(b)
		isProto := strings.HasSuffix(strings.ToLower(path), ".proto")
		for _, line := range strings.Split(s, "\n") {
			if strings.ContainsAny(line, "\"'`") {
				for _, p := range paths {
					if !pathHits[p] && strings.Contains(line, p) {
						pathHits[p] = true
					}
				}
			}
		}
		for _, t := range tables {
			if mode := tableHits[t]; mode != "read_write" {
				read := strings.Contains(mode, "read") || readRes[t].MatchString(s)
				write := strings.Contains(mode, "write") || writeRes[t].MatchString(s)
				switch {
				case read && write:
					tableHits[t] = "read_write"
				case write:
					tableHits[t] = "write"
				case read:
					tableHits[t] = "read"
				}
			}
		}
		if isProto {
			for m, re := range messageRes {
				if !messageHits[m] && re.MatchString(s) {
					messageHits[m] = true
				}
			}
		}
	})
	return pathHits, tableHits, messageHits
}

// skipDirs are dependency/build trees that would be slow to walk and full of
// third-party text that produces false cross-repo references. Hidden dirs
// (.venv, .terraform, ...) are skipped by rule; these are the common unhidden
// ones.
var skipDirs = map[string]bool{
	"node_modules": true, "vendor": true, "dist": true, "build": true,
	"target": true, "out": true, "__pycache__": true, "coverage": true,
	"venv": true, "env": true, "Pods": true, "third_party": true,
	"bower_components": true,
}

// walkSources visits every scannable source file under root, skipping VCS,
// hidden, and dependency/build directories, and files over maxSize bytes.
func walkSources(root string, maxSize int64, visit func(path string)) {
	filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			name := d.Name()
			// hidden dirs (.git, .venv, .terraform, .next, ...) and known
			// dependency trees; the repo root itself is exempt from the
			// hidden-dir rule so a checkout under a dotted path still scans
			if (strings.HasPrefix(name, ".") && path != root) || skipDirs[name] {
				return fs.SkipDir
			}
			return nil
		}
		if !sourceExts[strings.ToLower(filepath.Ext(path))] {
			return nil
		}
		if fi, err := d.Info(); err != nil || fi.Size() > maxSize {
			return nil
		}
		visit(path)
		return nil
	})
}

func dedupe(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range in {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}
