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

var (
	createTableRe  = regexp.MustCompile(`(?i)CREATE\s+TABLE\s+(?:IF\s+NOT\s+EXISTS\s+)?["'` + "`" + `]?([A-Za-z_][\w]*)`)
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
				ex.Deps = append(ex.Deps, r.Mod.Path)
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

	walkSources(root, func(path string) {
		base := strings.ToLower(filepath.Base(path))
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
		// under other names turn out to be common.
		case strings.HasPrefix(base, "openapi") || strings.HasPrefix(base, "swagger"):
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

const maxScanFileSize = 512 * 1024

// ScanRefs walks the repo's source files once and reports which needles occur.
// Matching is contextual, not bare name matching — a table name in a comment
// or an unrelated identifier must not become a graph edge:
//   - paths (endpoints): substring match, but only on lines that contain a
//     string-literal quote — code calls URLs in quoted strings, prose doesn't
//   - tables: word match preceded by a SQL keyword (FROM/JOIN/INTO/UPDATE/
//     CREATE TABLE/DELETE FROM)
//   - messages (proto): word match in .proto files only, where sharing a
//     message is an actual contract
//
// ponytail: still text-level, no per-language AST — treat hits as "worth
// checking", not proof; that ceiling is documented in the README.
func ScanRefs(root string, paths, tables, messages []string) (pathHits, tableHits, messageHits map[string]bool) {
	pathHits = map[string]bool{}
	tableHits = map[string]bool{}
	messageHits = map[string]bool{}
	tableRes := make(map[string]*regexp.Regexp, len(tables))
	for _, t := range tables {
		tableRes[t] = regexp.MustCompile(
			`(?i)(from|join|into|update|delete\s+from|create\s+table(\s+if\s+not\s+exists)?)[\s"'` + "`" + `]+` +
				regexp.QuoteMeta(t) + `\b`)
	}
	messageRes := make(map[string]*regexp.Regexp, len(messages))
	for _, m := range messages {
		messageRes[m] = regexp.MustCompile(`\b` + regexp.QuoteMeta(m) + `\b`)
	}
	done := func() bool {
		return len(pathHits) == len(paths) && len(tableHits) == len(tables) && len(messageHits) == len(messages)
	}
	walkSources(root, func(path string) {
		if manifestFiles[filepath.Base(path)] || done() {
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
		for t, re := range tableRes {
			if !tableHits[t] && re.MatchString(s) {
				tableHits[t] = true
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

// walkSources visits every scannable source file under root, skipping VCS and
// dependency directories and oversized files.
func walkSources(root string, visit func(path string)) {
	filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			name := d.Name()
			if name == ".git" || name == "node_modules" || name == "vendor" || name == "dist" {
				return fs.SkipDir
			}
			return nil
		}
		if !sourceExts[strings.ToLower(filepath.Ext(path))] {
			return nil
		}
		if fi, err := d.Info(); err != nil || fi.Size() > maxScanFileSize {
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
