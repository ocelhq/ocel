package gcp

import (
	"testing"

	"github.com/ocelhq/ocel/pkg/configdoc"
	"github.com/ocelhq/ocel/pkg/configdoc/schematest"
)

func TestOptionsSchemaIsCommitted(t *testing.T) {
	generated, err := configdoc.OptionsSchema(string(Vendor), Options{})
	if err != nil {
		t.Fatalf("options schema: %v", err)
	}
	schematest.AssertCommitted(t, schematest.OptionsSchemaFile, generated)
}
