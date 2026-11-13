package ruleset_test

import (
	"bytes"
	"os"
	"testing"

	"github.com/ocelhq/ocel/pkg/pricing/ruleset"
)

func TestTheIngestManifestIsWhatTheRulesGenerate(t *testing.T) {
	want, err := ruleset.ManifestJSON()
	if err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile("../ingest/manifest.json")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Error("pkg/pricing/ingest/manifest.json is stale; run go generate ./pkg/pricing/ruleset")
	}
}

func TestEveryVendorAndTypeHasOneRuleSet(t *testing.T) {
	vendors := map[string]bool{}
	types := map[string]string{}
	for _, rules := range ruleset.Vendors() {
		vendor := rules.Ingest.Vendor
		if vendor == "" || vendors[vendor] {
			t.Errorf("vendor %q is unnamed or appears twice", vendor)
		}
		vendors[vendor] = true
		for typ := range rules.Table {
			if other, ok := types[typ]; ok {
				t.Errorf("%s is priced by both %s and %s", typ, other, vendor)
			}
			types[typ] = vendor
		}
	}
	if len(ruleset.Table()) != len(types) {
		t.Errorf("table prices %d types, want %d", len(ruleset.Table()), len(types))
	}
}
