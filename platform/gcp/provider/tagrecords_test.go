package gcp

import (
	"slices"
	"testing"

	"github.com/ocelhq/ocel/platform/gcp/provider/internal/bootstrap"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/googletest"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/names"
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

func TestNoTierKeepsADatabaseOfRunsOrTagRecordsAndADeployMayMakeEachProjectsOwn(t *testing.T) {
	t.Parallel()

	names := names.New("ocel", "acme-prod")
	for _, tier := range googletest.BothTiers {
		for _, item := range bootstrap.Resources(names, tier, false) {
			if item.Kind == bootstrap.KindDatabase && item.Name != names.RecordsDatabase(tier) {
				t.Errorf("a %s bootstrap provisions database %s, and every database but the tier's records is a project's own", tier, item.Name)
			}
		}
	}
	for _, offered := range composed(bootstrap.Engine{ServiceStates: &bootstrap.ServiceStates{}}).Parts() {
		if offered.Name == "tasks" || offered.Name == "tag-database" {
			t.Errorf("the bootstrap offers the %s part, and runs and tag records are kept in the database each project keeps its records in", offered.Name)
		}
	}
}
