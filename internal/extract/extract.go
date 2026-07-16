// Package extract parses a repo's manifests into declared surface: packages,
// endpoints, and schemas. Pure parsing — no database access.
package extract

import (
	"encoding/json"
	"encoding/xml"
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
	Symbols   []string // top-level exported function/type/class names
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

	pyDepNameRe   = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*`) // bare package name, strips version specifiers/extras
	tomlNameRe    = regexp.MustCompile(`(?m)^\s*name\s*=\s*"([^"]+)"`)
	tomlDepsRe    = regexp.MustCompile(`(?s)dependencies\s*=\s*\[(.*?)\]`)
	quotedRe      = regexp.MustCompile(`"([^"]+)"`)
	gemRe         = regexp.MustCompile(`(?m)^\s*gem\s+["']([\w.-]+)["']`)
	gemspecNameRe = regexp.MustCompile(`\.name\s*=\s*["']([\w.-]+)["']`)
	gradleNameRe  = regexp.MustCompile(`rootProject\.name\s*=\s*["']([\w.-]+)["']`)
	// ponytail: only the `"group:artifact:version"` string form; the
	// `group:`/`name:`/`version:` map form is rarer, add if it shows up.
	gradleDepRe = regexp.MustCompile(`(?:implementation|api|compile|testImplementation|runtimeOnly|compileOnly)\s*\(?\s*["']([\w.-]+:[\w.-]+):[\w.+-]+["']`)
)

// symbolRes maps a source extension to the regexes that find top-level
// exported declarations in it — one line-anchored pattern family per
// language, same tradeoff as parseProto below: methods, nested/local
// declarations, and non-idiomatic layouts are out of scope. Extraction is
// text-level everywhere else in this file; this is no different.
var symbolRes = map[string][]*regexp.Regexp{
	".go": {
		regexp.MustCompile(`(?m)^func\s+([A-Z]\w*)\s*\(`),
		regexp.MustCompile(`(?m)^type\s+([A-Z]\w*)\s`),
	},
	".py": {
		regexp.MustCompile(`(?m)^def\s+([A-Za-z_]\w*)`),
		regexp.MustCompile(`(?m)^class\s+([A-Za-z_]\w*)`),
	},
	".rb": {
		regexp.MustCompile(`(?m)^\s*def\s+([a-z_]\w*[?!]?)`),
		regexp.MustCompile(`(?m)^\s*class\s+([A-Z]\w*)`),
		regexp.MustCompile(`(?m)^\s*module\s+([A-Z]\w*)`),
	},
	".java": {regexp.MustCompile(`public\s+(?:static\s+|final\s+|abstract\s+)*(?:class|interface|enum|record)\s+(\w+)`)},
	".kt":   {regexp.MustCompile(`public\s+(?:static\s+|final\s+|abstract\s+)*(?:class|interface|enum|record)\s+(\w+)`)},
	".cs":   {regexp.MustCompile(`public\s+(?:static\s+|sealed\s+|abstract\s+|partial\s+)*(?:class|interface|enum|record)\s+(\w+)`)},
	".php": {
		regexp.MustCompile(`(?m)^\s*function\s+(\w+)\s*\(`),
		regexp.MustCompile(`(?m)^\s*class\s+(\w+)`),
	},
	".rs": {
		regexp.MustCompile(`(?m)^pub\s+(?:async\s+)?fn\s+(\w+)`),
		regexp.MustCompile(`(?m)^pub\s+struct\s+(\w+)`),
		regexp.MustCompile(`(?m)^pub\s+enum\s+(\w+)`),
	},
}
var jsExportRes = []*regexp.Regexp{
	regexp.MustCompile(`(?m)^export\s+(?:default\s+)?(?:async\s+)?function\s+(\w+)`),
	regexp.MustCompile(`(?m)^export\s+(?:default\s+)?class\s+(\w+)`),
	regexp.MustCompile(`(?m)^export\s+const\s+(\w+)`),
}

func init() {
	for _, ext := range []string{".js", ".ts", ".jsx", ".tsx"} {
		symbolRes[ext] = jsExportRes
	}
}

// parseSymbols pulls top-level exported names out of a source file by
// extension. Python/Ruby names starting with "_" (private by convention)
// are dropped.
func parseSymbols(path, ext string) []string {
	res, ok := symbolRes[ext]
	if !ok {
		return nil
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	s := string(b)
	var syms []string
	for _, re := range res {
		for _, m := range re.FindAllStringSubmatch(s, -1) {
			if name := m[1]; !strings.HasPrefix(name, "_") {
				syms = append(syms, name)
			}
		}
	}
	return syms
}

// pomXML is the subset of a Maven pom.xml this cares about.
type pomXML struct {
	GroupID    string `xml:"groupId"`
	ArtifactID string `xml:"artifactId"`
	Parent     struct {
		GroupID string `xml:"groupId"`
	} `xml:"parent"`
	Dependencies struct {
		Dependency []struct {
			GroupID    string `xml:"groupId"`
			ArtifactID string `xml:"artifactId"`
		} `xml:"dependency"`
	} `xml:"dependencies"`
}

// csprojXML is the subset of a .NET .csproj this cares about.
type csprojXML struct {
	ItemGroups []struct {
		PackageReference []struct {
			Include string `xml:"Include,attr"`
		} `xml:"PackageReference"`
	} `xml:"ItemGroup"`
}

// Repo extracts the declared surface of the repo at root. Missing or
// unparseable files are recorded in Errors and skipped.
func Repo(root string) Extraction {
	var ex Extraction

	// Manifests are read anywhere in the tree, not just the root — a
	// monorepo keeps each service's go.mod/package.json/etc. next to its own
	// code, and root-only reads silently missed it. Same skip rules as
	// walkSources (vendor/build/hidden dirs excluded).
	walkFiles(root, maxDeclaredFileSize, isManifestFile, func(path string) {
		modules, deps, errStr := parseManifestFile(path)
		ex.Modules = append(ex.Modules, modules...)
		ex.Deps = append(ex.Deps, deps...)
		if errStr != "" {
			ex.Errors = append(ex.Errors, errStr)
		}
	})

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
		case symbolRes[ext] != nil:
			ex.Symbols = append(ex.Symbols, parseSymbols(path, ext)...)
		}
	})

	ex.Modules = dedupe(ex.Modules)
	ex.Deps = dedupe(ex.Deps)
	ex.Tables = dedupe(ex.Tables)
	ex.Messages = dedupe(ex.Messages)
	ex.Symbols = dedupe(ex.Symbols)
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

// manifestNames are exact filenames that declare a package's identity and/or
// dependencies. Checked in every non-skipped directory (not just root) so
// monorepo services keep their own Package identity.
var manifestNames = map[string]bool{
	"go.mod": true, "package.json": true, "requirements.txt": true,
	"pyproject.toml": true, "Cargo.toml": true, "composer.json": true,
	"Gemfile": true, "pom.xml": true,
	"build.gradle": true, "build.gradle.kts": true,
	"settings.gradle": true, "settings.gradle.kts": true,
}

func isManifestFile(base string) bool {
	return manifestNames[base] || strings.HasSuffix(base, ".csproj") || strings.HasSuffix(base, ".gemspec")
}

// parseManifestFile parses one manifest file into whatever module identity
// and/or dependencies it declares. Unrecognized names (isManifestFile
// already filtered the walk, so this shouldn't hit) return zero values.
func parseManifestFile(path string) (modules, deps []string, errStr string) {
	base := filepath.Base(path)
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, nil, ""
	}
	switch {
	case base == "go.mod":
		f, err := modfile.Parse("go.mod", b, nil)
		if err != nil {
			return nil, nil, "go.mod: " + err.Error()
		}
		if f.Module != nil {
			modules = append(modules, f.Module.Mod.Path)
		}
		for _, r := range f.Require {
			if !r.Indirect { // transitive deps are not a direct imports edge
				deps = append(deps, r.Mod.Path)
			}
		}
	case base == "package.json":
		name, d, err := parsePackageJSON(b)
		if err != nil {
			return nil, nil, "package.json: " + err.Error()
		}
		if name != "" {
			modules = append(modules, name)
		}
		deps = d
	case base == "requirements.txt":
		deps = parseRequirementsTxt(b)
	case base == "pyproject.toml":
		name, d := parsePyproject(b)
		if name != "" {
			modules = append(modules, name)
		}
		deps = d
	case base == "Cargo.toml":
		name, d := parseCargoToml(b)
		if name != "" {
			modules = append(modules, name)
		}
		deps = d
	case base == "composer.json":
		name, d, err := parseComposerJSON(b)
		if err != nil {
			return nil, nil, "composer.json: " + err.Error()
		}
		if name != "" {
			modules = append(modules, name)
		}
		deps = d
	case base == "Gemfile":
		for _, m := range gemRe.FindAllStringSubmatch(string(b), -1) {
			deps = append(deps, m[1])
		}
	case strings.HasSuffix(base, ".gemspec"):
		if m := gemspecNameRe.FindStringSubmatch(string(b)); m != nil {
			modules = append(modules, m[1])
		}
	case base == "pom.xml":
		name, d, err := parsePomXML(b)
		if err != nil {
			return nil, nil, "pom.xml: " + err.Error()
		}
		if name != "" {
			modules = append(modules, name)
		}
		deps = d
	case base == "build.gradle", base == "build.gradle.kts":
		for _, m := range gradleDepRe.FindAllStringSubmatch(string(b), -1) {
			deps = append(deps, m[1])
		}
	case base == "settings.gradle", base == "settings.gradle.kts":
		if m := gradleNameRe.FindStringSubmatch(string(b)); m != nil {
			modules = append(modules, m[1])
		}
	case strings.HasSuffix(base, ".csproj"):
		name, d, err := parseCsproj(base, b)
		if err != nil {
			return nil, nil, base + ": " + err.Error()
		}
		if name != "" {
			modules = append(modules, name)
		}
		deps = d
	}
	return modules, deps, ""
}

func parsePackageJSON(b []byte) (name string, deps []string, err error) {
	var pkg struct {
		Name         string            `json:"name"`
		Dependencies map[string]string `json:"dependencies"`
		DevDeps      map[string]string `json:"devDependencies"`
	}
	if err := json.Unmarshal(b, &pkg); err != nil {
		return "", nil, err
	}
	for d := range pkg.Dependencies {
		deps = append(deps, d)
	}
	for d := range pkg.DevDeps {
		deps = append(deps, d)
	}
	return pkg.Name, deps, nil
}

func parseComposerJSON(b []byte) (name string, deps []string, err error) {
	var pkg struct {
		Name       string            `json:"name"`
		Require    map[string]string `json:"require"`
		RequireDev map[string]string `json:"require-dev"`
	}
	if err := json.Unmarshal(b, &pkg); err != nil {
		return "", nil, err
	}
	for d := range pkg.Require {
		if d != "php" && !strings.HasPrefix(d, "ext-") { // platform requirements, not packages
			deps = append(deps, d)
		}
	}
	for d := range pkg.RequireDev {
		deps = append(deps, d)
	}
	return pkg.Name, deps, nil
}

func parsePomXML(b []byte) (module string, deps []string, err error) {
	var pom pomXML
	if err := xml.Unmarshal(b, &pom); err != nil {
		return "", nil, err
	}
	groupID := pom.GroupID
	if groupID == "" {
		groupID = pom.Parent.GroupID
	}
	if pom.ArtifactID != "" {
		module = groupID + ":" + pom.ArtifactID
	}
	for _, d := range pom.Dependencies.Dependency {
		if d.ArtifactID != "" {
			deps = append(deps, d.GroupID+":"+d.ArtifactID)
		}
	}
	return module, deps, nil
}

func parseCsproj(base string, b []byte) (module string, deps []string, err error) {
	var proj csprojXML
	if err := xml.Unmarshal(b, &proj); err != nil {
		return "", nil, err
	}
	module = strings.TrimSuffix(base, filepath.Ext(base))
	for _, ig := range proj.ItemGroups {
		for _, pr := range ig.PackageReference {
			if pr.Include != "" {
				deps = append(deps, pr.Include)
			}
		}
	}
	return module, deps, nil
}

// parseRequirementsTxt pulls bare package names out of a pip requirements
// file, stripping version specifiers, extras, env markers, and options
// (-r, --index-url, ...).
func parseRequirementsTxt(b []byte) []string {
	var deps []string
	for _, line := range strings.Split(string(b), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, "-") {
			continue
		}
		if i := strings.IndexAny(line, ";#"); i >= 0 {
			line = strings.TrimSpace(line[:i])
		}
		if d := pyDepNameRe.FindString(line); d != "" {
			deps = append(deps, d)
		}
	}
	return deps
}

// parsePyproject reads a pyproject.toml for the project name and its
// dependencies, supporting PEP 621 (`[project]`) and Poetry
// (`[tool.poetry]`) layouts.
func parsePyproject(b []byte) (name string, deps []string) {
	s := string(b)
	project := sectionBlock(s, "project")
	if m := tomlNameRe.FindStringSubmatch(project); m != nil {
		name = m[1]
	}
	if m := tomlDepsRe.FindStringSubmatch(project); m != nil {
		for _, q := range quotedRe.FindAllStringSubmatch(m[1], -1) {
			if d := pyDepNameRe.FindString(q[1]); d != "" {
				deps = append(deps, d)
			}
		}
	}
	if name == "" {
		if m := tomlNameRe.FindStringSubmatch(sectionBlock(s, "tool.poetry")); m != nil {
			name = m[1]
		}
	}
	for _, line := range strings.Split(sectionBlock(s, "tool.poetry.dependencies"), "\n") {
		key := strings.TrimSpace(strings.SplitN(line, "=", 2)[0])
		if key != "" && key != "python" {
			deps = append(deps, key)
		}
	}
	return name, deps
}

// parseCargoToml reads a Cargo.toml for the crate name and its dependency
// keys across [dependencies], [dev-dependencies], and [build-dependencies].
func parseCargoToml(b []byte) (name string, deps []string) {
	s := string(b)
	if m := tomlNameRe.FindStringSubmatch(sectionBlock(s, "package")); m != nil {
		name = m[1]
	}
	for _, section := range []string{"dependencies", "dev-dependencies", "build-dependencies"} {
		for _, line := range strings.Split(sectionBlock(s, section), "\n") {
			key := strings.TrimSpace(strings.SplitN(line, "=", 2)[0])
			if key != "" && !strings.HasPrefix(key, "#") {
				deps = append(deps, key)
			}
		}
	}
	return name, deps
}

// sectionBlock returns the body of a top-level TOML `[header]` table: the
// text between that header line and the next top-level `[...]`/`[[...]]` or
// EOF. Not a general TOML parser — just enough to pull a name and a flat
// list of dependency keys out of the handful of tables extraction cares
// about.
func sectionBlock(s, header string) string {
	re := regexp.MustCompile(`(?m)^\[` + regexp.QuoteMeta(header) + `\]\s*$`)
	loc := re.FindStringIndex(s)
	if loc == nil {
		return ""
	}
	rest := s[loc[1]:]
	if next := regexp.MustCompile(`(?m)^\[`).FindStringIndex(rest); next != nil {
		return rest[:next[0]]
	}
	return rest
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

// genericSymbols are exported names so common across codebases that a match
// means nothing on its own — same corroboration rule as genericTables.
var genericSymbols = map[string]bool{
	"new": true, "init": true, "run": true, "start": true, "stop": true,
	"close": true, "get": true, "set": true, "parse": true, "load": true,
	"save": true, "open": true, "read": true, "write": true, "config": true,
	"client": true, "server": true, "handler": true, "request": true,
	"response": true, "error": true, "context": true, "options": true,
	"result": true, "builder": true, "manager": true, "service": true,
	"controller": true, "model": true, "base": true, "main": true, "test": true,
	"helper": true, "util": true, "utils": true, "default": true, "create": true,
	"delete": true, "update": true, "list": true, "validate": true, "process": true,
}

// GenericSymbol reports whether an exported name is too common to link
// repos on the name alone.
func GenericSymbol(name string) bool { return genericSymbols[strings.ToLower(name)] }

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
	"composer.json": true, "composer.lock": true,
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
func ScanRefs(root string, paths, tables, messages, symbols []string) (pathHits map[string]bool, tableHits map[string]string, messageHits map[string]bool, symbolHits map[string]bool) {
	pathHits = map[string]bool{}
	tableHits = map[string]string{}
	messageHits = map[string]bool{}
	symbolHits = map[string]bool{}
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
	// word-boundary only, no call-site requirement (unlike paths, which need
	// a quote) — genericity + org corroboration in computeRefs carries the
	// precision burden instead, same as table names.
	symRes := make(map[string]*regexp.Regexp, len(symbols))
	for _, sym := range symbols {
		symRes[sym] = regexp.MustCompile(`\b` + regexp.QuoteMeta(sym) + `\b`)
	}
	walkSources(root, maxScanFileSize, func(path string) {
		base := filepath.Base(path)
		// test code references tables/endpoints via fixtures and mocks, not
		// real dependencies — it must not create edges (testdata/ dirs are
		// skipped by the walk itself)
		if manifestFiles[base] || strings.HasSuffix(base, "_test.go") {
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
		for sym, re := range symRes {
			if !symbolHits[sym] && re.MatchString(s) {
				symbolHits[sym] = true
			}
		}
	})
	return pathHits, tableHits, messageHits, symbolHits
}

// skipDirs are dependency/build trees that would be slow to walk and full of
// third-party text that produces false cross-repo references. Hidden dirs
// (.venv, .terraform, ...) are skipped by rule; these are the common unhidden
// ones.
var skipDirs = map[string]bool{
	"node_modules": true, "vendor": true, "dist": true, "build": true,
	"target": true, "out": true, "__pycache__": true, "coverage": true,
	"venv": true, "env": true, "Pods": true, "third_party": true,
	"bower_components": true, "testdata": true,
}

// walkSources visits every scannable source file under root, skipping VCS,
// hidden, and dependency/build directories, and files over maxSize bytes.
func walkSources(root string, maxSize int64, visit func(path string)) {
	walkFiles(root, maxSize, func(base string) bool {
		return sourceExts[strings.ToLower(filepath.Ext(base))]
	}, visit)
}

// walkFiles visits every file under root whose base name satisfies match,
// skipping VCS, hidden, and dependency/build directories, and files over
// maxSize bytes.
func walkFiles(root string, maxSize int64, match func(base string) bool, visit func(path string)) {
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
		if !match(d.Name()) {
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
