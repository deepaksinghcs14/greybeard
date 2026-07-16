// Fixture: billing-svc's cross-repo surface. Calls orders-svc's HTTP API and
// reads its orders table directly. (Go tooling ignores testdata/, so this
// file never compiles as part of greybeard.)
package billing

// createOrder calls orders-svc over HTTP.
func createOrder(base string) error {
	return httpPost(base + "/orders")
}

// reconcileQuery reads the shared orders table for nightly reconciliation.
const reconcileQuery = `SELECT id, total FROM orders WHERE created_at > $1`

func httpPost(url string) error { return nil }
