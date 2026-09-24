package gcp

import (
	"testing"

	"github.com/ocelhq/ocel/pkg/configdoc"
	"github.com/ocelhq/ocel/pkg/configdoc/schematest"
)

func TestProviderSchemaIsCommitted(t *testing.T) {
	generated, err := configdoc.ProviderSchema(string(Vendor), Options{}, edges{}.Supported(), dns{}.Supported())
	if err != nil {
		t.Fatalf("provider schema: %v", err)
	}
	schematest.AssertCommitted(t, schematest.ProviderSchemaFile, generated)
}
