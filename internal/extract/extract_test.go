package extract

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
)

const ordersFixture = "../../testdata/repos/orders-svc"
const billingFixture = "../../testdata/repos/billing-svc"

func TestExtractGoRepoWithOpenAPIAndMigrations(t *testing.T) {
	ex := Repo(ordersFixture)
	if len(ex.Errors) != 0 {
		t.Fatalf("unexpected errors: %v", ex.Errors)
	}
	if !slices.Contains(ex.Modules, "example.com/orders-svc") {
		t.Errorf("modules = %v", ex.Modules)
	}
	if !slices.Contains(ex.Deps, "github.com/jackc/pgx/v5") {
		t.Errorf("deps = %v", ex.Deps)
	}
	if !slices.Contains(ex.Endpoints, Endpoint{Method: "POST", Path: "/orders"}) ||
		!slices.Contains(ex.Endpoints, Endpoint{Method: "GET", Path: "/orders/{id}"}) {
		t.Errorf("endpoints = %v", ex.Endpoints)
	}
	if !slices.Contains(ex.Tables, "orders") || !slices.Contains(ex.Tables, "order_items") {
		t.Errorf("tables = %v", ex.Tables)
	}
}

func TestExtractPackageJSON(t *testing.T) {
	dir := t.TempDir()
	pkg := `{
  "name": "@acme/web-app",
  "dependencies": { "react": "^18.0.0", "@acme/api-client": "1.2.0" },
  "devDependencies": { "vitest": "^1.0.0" }
}`
	if err := os.WriteFile(filepath.Join(dir, "package.json"), []byte(pkg), 0o644); err != nil {
		t.Fatal(err)
	}
	ex := Repo(dir)
	if !slices.Contains(ex.Modules, "@acme/web-app") {
		t.Errorf("modules = %v", ex.Modules)
	}
	for _, want := range []string{"react", "@acme/api-client", "vitest"} {
		if !slices.Contains(ex.Deps, want) {
			t.Errorf("deps missing %q: %v", want, ex.Deps)
		}
	}
}

func TestExtractRequirementsTxt(t *testing.T) {
	dir := t.TempDir()
	req := "requests>=2.28.0\nclick==8.1.0\n# a comment\n-r base.txt\nflask; python_version >= \"3.8\"\n"
	if err := os.WriteFile(filepath.Join(dir, "requirements.txt"), []byte(req), 0o644); err != nil {
		t.Fatal(err)
	}
	ex := Repo(dir)
	for _, want := range []string{"requests", "click", "flask"} {
		if !slices.Contains(ex.Deps, want) {
			t.Errorf("deps missing %q: %v", want, ex.Deps)
		}
	}
	if slices.Contains(ex.Deps, "base.txt") {
		t.Errorf("-r option should not be treated as a dep: %v", ex.Deps)
	}
}

func TestExtractPyprojectPEP621(t *testing.T) {
	dir := t.TempDir()
	toml := `[project]
name = "acme-service"
dependencies = [
    "requests>=2.28.0",
    "click",
]

[tool.other]
name = "should-not-win"
`
	if err := os.WriteFile(filepath.Join(dir, "pyproject.toml"), []byte(toml), 0o644); err != nil {
		t.Fatal(err)
	}
	ex := Repo(dir)
	if !slices.Contains(ex.Modules, "acme-service") {
		t.Errorf("modules = %v", ex.Modules)
	}
	for _, want := range []string{"requests", "click"} {
		if !slices.Contains(ex.Deps, want) {
			t.Errorf("deps missing %q: %v", want, ex.Deps)
		}
	}
}

func TestExtractPyprojectPoetry(t *testing.T) {
	dir := t.TempDir()
	toml := `[tool.poetry]
name = "acme-worker"
version = "0.1.0"

[tool.poetry.dependencies]
python = "^3.10"
requests = "^2.28.0"
click = "*"
`
	if err := os.WriteFile(filepath.Join(dir, "pyproject.toml"), []byte(toml), 0o644); err != nil {
		t.Fatal(err)
	}
	ex := Repo(dir)
	if !slices.Contains(ex.Modules, "acme-worker") {
		t.Errorf("modules = %v", ex.Modules)
	}
	for _, want := range []string{"requests", "click"} {
		if !slices.Contains(ex.Deps, want) {
			t.Errorf("deps missing %q: %v", want, ex.Deps)
		}
	}
	if slices.Contains(ex.Deps, "python") {
		t.Errorf("python itself should not be a dep: %v", ex.Deps)
	}
}

func TestExtractCargoToml(t *testing.T) {
	dir := t.TempDir()
	toml := `[package]
name = "acme-cli"
version = "0.1.0"

[dependencies]
serde = "1.0"
tokio = { version = "1", features = ["full"] }

[dev-dependencies]
proptest = "1"
`
	if err := os.WriteFile(filepath.Join(dir, "Cargo.toml"), []byte(toml), 0o644); err != nil {
		t.Fatal(err)
	}
	ex := Repo(dir)
	if !slices.Contains(ex.Modules, "acme-cli") {
		t.Errorf("modules = %v", ex.Modules)
	}
	for _, want := range []string{"serde", "tokio", "proptest"} {
		if !slices.Contains(ex.Deps, want) {
			t.Errorf("deps missing %q: %v", want, ex.Deps)
		}
	}
}

func TestExtractComposerJSON(t *testing.T) {
	dir := t.TempDir()
	pkg := `{
  "name": "acme/billing",
  "require": { "guzzlehttp/guzzle": "^7.0", "php": ">=8.1", "ext-json": "*" },
  "require-dev": { "phpunit/phpunit": "^10.0" }
}`
	if err := os.WriteFile(filepath.Join(dir, "composer.json"), []byte(pkg), 0o644); err != nil {
		t.Fatal(err)
	}
	ex := Repo(dir)
	if !slices.Contains(ex.Modules, "acme/billing") {
		t.Errorf("modules = %v", ex.Modules)
	}
	for _, want := range []string{"guzzlehttp/guzzle", "phpunit/phpunit"} {
		if !slices.Contains(ex.Deps, want) {
			t.Errorf("deps missing %q: %v", want, ex.Deps)
		}
	}
	for _, notWant := range []string{"php", "ext-json"} {
		if slices.Contains(ex.Deps, notWant) {
			t.Errorf("platform requirement %q should not be a dep: %v", notWant, ex.Deps)
		}
	}
}

func TestExtractGemfile(t *testing.T) {
	dir := t.TempDir()
	gemfile := "source 'https://rubygems.org'\n\ngem \"rails\", \"~> 7.0\"\ngem 'sidekiq'\n"
	gemspec := `Gem::Specification.new do |spec|
  spec.name = "acme-worker"
  spec.version = "0.1.0"
end
`
	if err := os.WriteFile(filepath.Join(dir, "Gemfile"), []byte(gemfile), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "acme-worker.gemspec"), []byte(gemspec), 0o644); err != nil {
		t.Fatal(err)
	}
	ex := Repo(dir)
	if !slices.Contains(ex.Modules, "acme-worker") {
		t.Errorf("modules = %v", ex.Modules)
	}
	for _, want := range []string{"rails", "sidekiq"} {
		if !slices.Contains(ex.Deps, want) {
			t.Errorf("deps missing %q: %v", want, ex.Deps)
		}
	}
}

func TestExtractPomXML(t *testing.T) {
	dir := t.TempDir()
	pom := `<project>
  <groupId>com.acme</groupId>
  <artifactId>billing-service</artifactId>
  <version>1.0.0</version>
  <dependencies>
    <dependency>
      <groupId>com.squareup.okhttp3</groupId>
      <artifactId>okhttp</artifactId>
      <version>4.9.0</version>
    </dependency>
  </dependencies>
</project>
`
	if err := os.WriteFile(filepath.Join(dir, "pom.xml"), []byte(pom), 0o644); err != nil {
		t.Fatal(err)
	}
	ex := Repo(dir)
	if !slices.Contains(ex.Modules, "com.acme:billing-service") {
		t.Errorf("modules = %v", ex.Modules)
	}
	if !slices.Contains(ex.Deps, "com.squareup.okhttp3:okhttp") {
		t.Errorf("deps = %v", ex.Deps)
	}
}

func TestExtractGradle(t *testing.T) {
	dir := t.TempDir()
	settings := `rootProject.name = 'billing-service'`
	build := `dependencies {
    implementation("com.squareup.okhttp3:okhttp:4.9.0")
    testImplementation 'junit:junit:4.13.2'
}
`
	if err := os.WriteFile(filepath.Join(dir, "settings.gradle"), []byte(settings), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "build.gradle"), []byte(build), 0o644); err != nil {
		t.Fatal(err)
	}
	ex := Repo(dir)
	if !slices.Contains(ex.Modules, "billing-service") {
		t.Errorf("modules = %v", ex.Modules)
	}
	for _, want := range []string{"com.squareup.okhttp3:okhttp", "junit:junit"} {
		if !slices.Contains(ex.Deps, want) {
			t.Errorf("deps missing %q: %v", want, ex.Deps)
		}
	}
}

func TestExtractCsproj(t *testing.T) {
	dir := t.TempDir()
	csproj := `<Project Sdk="Microsoft.NET.Sdk">
  <ItemGroup>
    <PackageReference Include="Newtonsoft.Json" Version="13.0.1" />
    <PackageReference Include="Serilog" Version="2.10.0" />
  </ItemGroup>
</Project>
`
	if err := os.WriteFile(filepath.Join(dir, "Acme.Billing.csproj"), []byte(csproj), 0o644); err != nil {
		t.Fatal(err)
	}
	ex := Repo(dir)
	if !slices.Contains(ex.Modules, "Acme.Billing") {
		t.Errorf("modules = %v", ex.Modules)
	}
	for _, want := range []string{"Newtonsoft.Json", "Serilog"} {
		if !slices.Contains(ex.Deps, want) {
			t.Errorf("deps missing %q: %v", want, ex.Deps)
		}
	}
}

func TestExtractUnparseableXMLIsNonFatal(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "pom.xml"), []byte("<project><unterminated"), 0o644)
	ex := Repo(dir)
	if len(ex.Errors) == 0 {
		t.Error("expected a recorded error for malformed pom.xml")
	}
}

func TestExtractMonorepoNestedManifests(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "services", "orders"), 0o755)
	os.MkdirAll(filepath.Join(dir, "services", "billing"), 0o755)
	os.MkdirAll(filepath.Join(dir, "node_modules", "some-lib"), 0o755)
	os.WriteFile(filepath.Join(dir, "services", "orders", "go.mod"),
		[]byte("module example.com/orders\n\ngo 1.22\n\nrequire github.com/jackc/pgx/v5 v5.5.0\n"), 0o644)
	os.WriteFile(filepath.Join(dir, "services", "billing", "package.json"),
		[]byte(`{"name": "@acme/billing", "dependencies": {"stripe": "^14.0.0"}}`), 0o644)
	os.WriteFile(filepath.Join(dir, "node_modules", "some-lib", "package.json"),
		[]byte(`{"name": "should-not-appear", "dependencies": {"also-not-appear": "1.0.0"}}`), 0o644)

	ex := Repo(dir)
	if !slices.Contains(ex.Modules, "example.com/orders") {
		t.Errorf("nested go.mod not found: modules = %v", ex.Modules)
	}
	if !slices.Contains(ex.Modules, "@acme/billing") {
		t.Errorf("nested package.json not found: modules = %v", ex.Modules)
	}
	if !slices.Contains(ex.Deps, "github.com/jackc/pgx/v5") || !slices.Contains(ex.Deps, "stripe") {
		t.Errorf("nested deps missing: %v", ex.Deps)
	}
	if slices.Contains(ex.Modules, "should-not-appear") || slices.Contains(ex.Deps, "also-not-appear") {
		t.Errorf("node_modules manifest should be skipped: modules=%v deps=%v", ex.Modules, ex.Deps)
	}
}

func TestExtractSymbolsGo(t *testing.T) {
	dir := t.TempDir()
	src := "package orders\n\n" +
		"func CreateOrder() {}\n" +
		"func lowerCaseHelper() {}\n" +
		"type Order struct{}\n" +
		"type internalState struct{}\n"
	os.WriteFile(filepath.Join(dir, "orders.go"), []byte(src), 0o644)
	ex := Repo(dir)
	for _, want := range []string{"CreateOrder", "Order"} {
		if !slices.Contains(ex.Symbols, want) {
			t.Errorf("symbols missing %q: %v", want, ex.Symbols)
		}
	}
	for _, notWant := range []string{"lowerCaseHelper", "internalState"} {
		if slices.Contains(ex.Symbols, notWant) {
			t.Errorf("unexported %q should not be a symbol: %v", notWant, ex.Symbols)
		}
	}
}

func TestExtractSymbolsPython(t *testing.T) {
	dir := t.TempDir()
	src := "def parse_config():\n    pass\n\n" +
		"def _internal_helper():\n    pass\n\n" +
		"class OrderProcessor:\n    pass\n"
	os.WriteFile(filepath.Join(dir, "orders.py"), []byte(src), 0o644)
	ex := Repo(dir)
	for _, want := range []string{"parse_config", "OrderProcessor"} {
		if !slices.Contains(ex.Symbols, want) {
			t.Errorf("symbols missing %q: %v", want, ex.Symbols)
		}
	}
	if slices.Contains(ex.Symbols, "_internal_helper") {
		t.Errorf("leading-underscore (private convention) should not be a symbol: %v", ex.Symbols)
	}
}

func TestExtractSymbolsJavaScript(t *testing.T) {
	dir := t.TempDir()
	src := "export function createOrder() {}\n" +
		"export class OrderClient {}\n" +
		"export const DEFAULT_TIMEOUT = 30;\n" +
		"function localHelper() {}\n"
	os.WriteFile(filepath.Join(dir, "orders.js"), []byte(src), 0o644)
	ex := Repo(dir)
	for _, want := range []string{"createOrder", "OrderClient", "DEFAULT_TIMEOUT"} {
		if !slices.Contains(ex.Symbols, want) {
			t.Errorf("symbols missing %q: %v", want, ex.Symbols)
		}
	}
	if slices.Contains(ex.Symbols, "localHelper") {
		t.Errorf("non-exported function should not be a symbol: %v", ex.Symbols)
	}
}

func TestExtractSymbolsRust(t *testing.T) {
	dir := t.TempDir()
	src := "pub fn create_order() {}\n" +
		"fn private_helper() {}\n" +
		"pub struct Order {}\n" +
		"pub enum OrderStatus {}\n"
	os.WriteFile(filepath.Join(dir, "orders.rs"), []byte(src), 0o644)
	ex := Repo(dir)
	for _, want := range []string{"create_order", "Order", "OrderStatus"} {
		if !slices.Contains(ex.Symbols, want) {
			t.Errorf("symbols missing %q: %v", want, ex.Symbols)
		}
	}
	if slices.Contains(ex.Symbols, "private_helper") {
		t.Errorf("non-pub function should not be a symbol: %v", ex.Symbols)
	}
}

func TestExtractSymbolsSkipsTestFiles(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "orders.go"), []byte("package orders\n\nfunc CreateOrder() {}\n"), 0o644)
	os.WriteFile(filepath.Join(dir, "orders_test.go"),
		[]byte("package orders\n\nimport \"testing\"\n\nfunc TestCreateOrder(t *testing.T) {}\nfunc BenchmarkParseOrder(b *testing.B) {}\n"), 0o644)
	os.WriteFile(filepath.Join(dir, "orders.py"), []byte("def create_order():\n    pass\n"), 0o644)
	os.WriteFile(filepath.Join(dir, "test_orders.py"), []byte("def test_create_order():\n    pass\n"), 0o644)
	os.WriteFile(filepath.Join(dir, "Latest.cs"), []byte("public class Latest {}\n"), 0o644)
	os.WriteFile(filepath.Join(dir, "OrderServiceTest.java"), []byte("public class OrderServiceTest {}\n"), 0o644)

	ex := Repo(dir)
	for _, want := range []string{"CreateOrder", "create_order", "Latest"} {
		if !slices.Contains(ex.Symbols, want) {
			t.Errorf("real symbol missing %q: %v", want, ex.Symbols)
		}
	}
	for _, notWant := range []string{"TestCreateOrder", "BenchmarkParseOrder", "test_create_order", "OrderServiceTest"} {
		if slices.Contains(ex.Symbols, notWant) {
			t.Errorf("test-file declaration %q leaked into symbols: %v", notWant, ex.Symbols)
		}
	}
}

func TestExtractProto(t *testing.T) {
	dir := t.TempDir()
	proto := `syntax = "proto3";
package orders.v1;

message Order {
  string id = 1;
}

message CreateOrderRequest {
  Order order = 1;
}

service OrderService {
  rpc CreateOrder(CreateOrderRequest) returns (Order);
  rpc GetOrder(GetOrderRequest) returns (Order);
}
`
	if err := os.WriteFile(filepath.Join(dir, "orders.proto"), []byte(proto), 0o644); err != nil {
		t.Fatal(err)
	}
	ex := Repo(dir)
	if !slices.Contains(ex.Endpoints, Endpoint{Method: "GRPC", Path: "OrderService/CreateOrder"}) {
		t.Errorf("endpoints = %v", ex.Endpoints)
	}
	if !slices.Contains(ex.Messages, "Order") || !slices.Contains(ex.Messages, "CreateOrderRequest") {
		t.Errorf("messages = %v", ex.Messages)
	}
}

func TestExtractUnparseableManifestIsNonFatal(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "go.mod"), []byte("this is not a go.mod"), 0o644)
	os.WriteFile(filepath.Join(dir, "package.json"), []byte(`{"name": "still-works"}`), 0o644)
	ex := Repo(dir)
	if len(ex.Errors) == 0 {
		t.Error("expected a recorded error for the bad go.mod")
	}
	if !slices.Contains(ex.Modules, "still-works") {
		t.Errorf("good manifest should still parse: %v", ex.Modules)
	}
}

func TestScanRefs(t *testing.T) {
	paths, tables, _, _ := ScanRefs(billingFixture,
		[]string{"/orders", "/orders/{id}", "/nothing-here"},
		[]string{"orders", "order", "order_items", "customers"},
		nil, nil)
	if !paths["/orders"] {
		t.Error("expected path hit for /orders (quoted in client code)")
	}
	if paths["/orders/{id}"] || paths["/nothing-here"] {
		t.Errorf("unexpected path hits: %v", paths)
	}
	if tables["orders"] != "read" {
		t.Errorf("orders should be a read (FROM orders), got %q", tables["orders"])
	}
	if _, ok := tables["order"]; ok {
		t.Error("table match must respect word boundaries")
	}
	if _, ok := tables["order_items"]; ok {
		t.Error("a table name in a comment (no SQL context) must not count as a reference")
	}
	if _, ok := tables["customers"]; ok {
		t.Error("unexpected hit for customers")
	}
}

func TestScanRefsQualifiedNamesAndWrites(t *testing.T) {
	dir := t.TempDir()
	src := "package x\n" +
		"const q1 = `SELECT * FROM public.orders`\n" +
		"const q2 = `INSERT INTO app.events (id) VALUES ($1)`\n" +
		"const q3 = `UPDATE ledger SET total = 0`\n"
	os.WriteFile(filepath.Join(dir, "db.go"), []byte(src), 0o644)
	_, tables, _, _ := ScanRefs(dir, nil, []string{"orders", "events", "ledger"}, nil, nil)
	if tables["orders"] != "read" {
		t.Errorf("qualified FROM public.orders should read, got %q", tables["orders"])
	}
	if tables["events"] != "write" {
		t.Errorf("INSERT INTO app.events should write, got %q", tables["events"])
	}
	if tables["ledger"] != "write" {
		t.Errorf("UPDATE ledger should write, got %q", tables["ledger"])
	}
}

func TestCreateTableCapturesUnqualifiedName(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "migrations"), 0o755)
	sql := "CREATE TABLE public.orders (id uuid);\nCREATE TABLE IF NOT EXISTS \"app\".invoices (id uuid);\n"
	os.WriteFile(filepath.Join(dir, "migrations", "001.sql"), []byte(sql), 0o644)
	ex := Repo(dir)
	if !slices.Contains(ex.Tables, "orders") || !slices.Contains(ex.Tables, "invoices") {
		t.Errorf("tables = %v, want orders and invoices (not schema qualifiers)", ex.Tables)
	}
	if slices.Contains(ex.Tables, "public") || slices.Contains(ex.Tables, "app") {
		t.Errorf("schema qualifier captured as table: %v", ex.Tables)
	}
}

func TestScanRefsMessagesOnlyInProto(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "notes.go"),
		[]byte("package x\n// handles Order objects\nvar Order int\n"), 0o644)
	_, _, msgs, _ := ScanRefs(dir, nil, nil, []string{"Order"}, nil)
	if msgs["Order"] {
		t.Error("message names outside .proto files must not count")
	}
	os.WriteFile(filepath.Join(dir, "shared.proto"),
		[]byte("syntax = \"proto3\";\nmessage Order { string id = 1; }\n"), 0o644)
	_, _, msgs, _ = ScanRefs(dir, nil, nil, []string{"Order"}, nil)
	if !msgs["Order"] {
		t.Error("message name in a .proto file should count")
	}
}

func TestScanRefsSkipsManifests(t *testing.T) {
	// billing's go.mod contains "example.com/orders-svc", but manifests are
	// excluded so a declared dep doesn't double as an API/schema reference.
	paths, _, _, _ := ScanRefs(billingFixture, []string{"example.com/orders-svc"}, nil, nil, nil)
	if paths["example.com/orders-svc"] {
		t.Error("manifest content should not count as a reference")
	}
}
