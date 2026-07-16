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
	paths, tables, _ := ScanRefs(billingFixture,
		[]string{"/orders", "/orders/{id}", "/nothing-here"},
		[]string{"orders", "order", "order_items", "customers"},
		nil)
	if !paths["/orders"] {
		t.Error("expected path hit for /orders (quoted in client code)")
	}
	if paths["/orders/{id}"] || paths["/nothing-here"] {
		t.Errorf("unexpected path hits: %v", paths)
	}
	if !tables["orders"] {
		t.Error("expected table hit for orders (FROM orders in a query)")
	}
	if tables["order"] {
		t.Error("table match must respect word boundaries")
	}
	if tables["order_items"] {
		t.Error("a table name in a comment (no SQL context) must not count as a reference")
	}
	if tables["customers"] {
		t.Error("unexpected hit for customers")
	}
}

func TestScanRefsMessagesOnlyInProto(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "notes.go"),
		[]byte("package x\n// handles Order objects\nvar Order int\n"), 0o644)
	_, _, msgs := ScanRefs(dir, nil, nil, []string{"Order"})
	if msgs["Order"] {
		t.Error("message names outside .proto files must not count")
	}
	os.WriteFile(filepath.Join(dir, "shared.proto"),
		[]byte("syntax = \"proto3\";\nmessage Order { string id = 1; }\n"), 0o644)
	_, _, msgs = ScanRefs(dir, nil, nil, []string{"Order"})
	if !msgs["Order"] {
		t.Error("message name in a .proto file should count")
	}
}

func TestScanRefsSkipsManifests(t *testing.T) {
	// billing's go.mod contains "example.com/orders-svc", but manifests are
	// excluded so a declared dep doesn't double as an API/schema reference.
	paths, _, _ := ScanRefs(billingFixture, []string{"example.com/orders-svc"}, nil, nil)
	if paths["example.com/orders-svc"] {
		t.Error("manifest content should not count as a reference")
	}
}
