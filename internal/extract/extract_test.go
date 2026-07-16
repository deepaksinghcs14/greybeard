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
	subs, words := ScanRefs(billingFixture,
		[]string{"/orders", "/orders/{id}", "/nothing-here"},
		[]string{"orders", "order", "customers"})
	if !subs["/orders"] {
		t.Error("expected substring hit for /orders")
	}
	if subs["/orders/{id}"] || subs["/nothing-here"] {
		t.Errorf("unexpected substring hits: %v", subs)
	}
	if !words["orders"] {
		t.Error("expected word hit for orders")
	}
	if words["order"] {
		t.Error("word match must respect boundaries: 'order' should not hit inside 'orders'")
	}
	if words["customers"] {
		t.Error("unexpected hit for customers")
	}
}

func TestScanRefsSkipsManifests(t *testing.T) {
	// billing's go.mod contains "example.com/orders-svc", but manifests are
	// excluded so a declared dep doesn't double as an API/schema reference.
	subs, _ := ScanRefs(billingFixture, []string{"example.com/orders-svc"}, nil)
	if subs["example.com/orders-svc"] {
		t.Error("manifest content should not count as a reference")
	}
}
