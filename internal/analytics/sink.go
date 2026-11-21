// Package analytics ships business events from the EventBus to analytics
// providers. The provider-specific parts live behind the Sink interface so
// swapping a provider (Axiom today, BigQuery next, anything after) is one
// new file plus a config change — the bus, the shipper, and the rest of the
// backend never change.
package analytics

import "context"

// Sink is one analytics provider's address card. WriteBatch receives
// enveloped event JSONs (each already carrying _event/_channel/_ts/_request_id)
// and must be safe for concurrent use. A returned error means the batch was
// not accepted; the shipper retries and then drops with a metric.
type Sink interface {
	Name() string
	WriteBatch(ctx context.Context, events [][]byte) error
}
