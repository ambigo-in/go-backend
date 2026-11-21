package analytics

import (
	"context"
	"encoding/json"
	"os"
	"testing"
	"time"
)

// Live smoke test against the real Axiom API. Skipped unless explicitly run:
//
//	AXIOM_API_TOKEN=xaat-... go test ./internal/analytics/ -run TestAxiomLive -v
//
// Ships two clearly-marked synthetic events (event=smoke_test) to the
// configured dataset (default ambigo-events) and verifies the API accepts
// them. Safe to re-run; the events are filterable out of every query.
func TestAxiomLive(t *testing.T) {
	token := os.Getenv("AXIOM_API_TOKEN")
	if token == "" {
		t.Skip("AXIOM_API_TOKEN not set")
	}
	dataset := os.Getenv("AXIOM_DATASET")
	if dataset == "" {
		dataset = "ambigo-events"
	}

	sink := NewAxiomSink(token, dataset, "")
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	if err := sink.EnsureDataset(ctx); err != nil {
		t.Fatalf("EnsureDataset failed (the sink resolves the ingest URL here): %v", err)
	}
	t.Logf("resolved dataset %q -> %s", dataset, sink.ingestURL)

	events := [][]byte{}
	for i := 0; i < 2; i++ {
		ev, _ := json.Marshal(map[string]interface{}{
			"_event":      "smoke_test",
			"_channel":    "test:smoke",
			"_ts":         time.Now().UTC().Format(time.RFC3339Nano),
			"_request_id": "smoke-test-request",
			"seq":         i + 1,
		})
		events = append(events, ev)
	}

	if err := sink.WriteBatch(ctx, events); err != nil {
		t.Fatalf("WriteBatch failed: %v", err)
	}
	t.Logf("OK: shipped %d smoke events to dataset %q", len(events), dataset)
}
