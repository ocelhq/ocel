package gcp

import (
	"slices"
	"testing"
)

func TestTheTagRecordsOfAnISRPrefixAreReadThroughOneIndexOnPrefixThenWriteTime(t *testing.T) {
	t.Parallel()

	indexes := tagIndexes()
	if len(indexes) != 1 {
		t.Fatalf("tagIndexes() = %d indexes, want the one a prefix's records are read through", len(indexes))
	}
	if indexes[0].QueryScope != "COLLECTION" {
		t.Errorf("the index's query scope = %q, want COLLECTION", indexes[0].QueryScope)
	}
	var got []string
	for _, field := range indexes[0].Fields {
		got = append(got, field.FieldPath+" "+field.Order)
	}
	if want := []string{"prefix ASCENDING", "writtenAt ASCENDING"}; !slices.Equal(got, want) {
		t.Errorf("the index's fields = %q, want %q", got, want)
	}
}

func TestNoTagRecordFieldAQueryNeverFiltersOnIsIndexed(t *testing.T) {
	t.Parallel()

	want := []string{"writtenAt", "tag", "stale", "expired"}
	if got := unindexedTagFields(); !slices.Equal(got, want) {
		t.Errorf("unindexedTagFields() = %q, want %q", got, want)
	}
	if slices.Contains(unindexedTagFields(), "prefix") {
		t.Error("the prefix is exempted from indexing, and a prune ranges over it")
	}
}
