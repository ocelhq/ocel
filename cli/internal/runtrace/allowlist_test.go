package runtrace

import (
	"slices"
	"testing"

	"go.opentelemetry.io/otel/attribute"
)

func TestTheNetworkExportKeepsProgressKeysAndTheKeysOnlyATraceSetsAndNothingElse(t *testing.T) {
	got := filterAttributes([]attribute.KeyValue{
		attribute.String("ocel.command", "ocel deploy"),
		attribute.Int("ocel.app_count", 2),
		attribute.Int("ocel.import_count", 9),
		attribute.Int("ocel.registration_count", 4),
		attribute.String("ocel.worker", "orders"),
		attribute.String("ocel.framework", "next"),
		attribute.Int("ocel.dropped_span_count", 3),
		attribute.String("http.request.header.authorization", "Bearer sk_live"),
		attribute.String("ocel.secret_note", "orders-db-password"),
	})

	var keys []string
	for _, keyValue := range got {
		keys = append(keys, string(keyValue.Key))
	}
	want := []string{"ocel.command", "ocel.app_count", "ocel.import_count", "ocel.registration_count", "ocel.worker", "ocel.framework", "ocel.dropped_span_count"}
	if !slices.Equal(keys, want) {
		t.Errorf("kept %q, want %q", keys, want)
	}
}
