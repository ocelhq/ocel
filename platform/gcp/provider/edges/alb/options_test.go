package alb

import (
	"testing"

	"github.com/ocelhq/ocel/pkg/configdoc"
	"github.com/ocelhq/ocel/pkg/configdoc/schematest"
	"github.com/ocelhq/ocel/platform/gcp/provider/edges/albresources"
)

func TestEdgeSchemaIsCommitted(t *testing.T) {
	generated, err := configdoc.EdgeSchema(albresources.Kind, "Alb", Options{})
	if err != nil {
		t.Fatalf("edge schema: %v", err)
	}
	schematest.AssertCommitted(t, schematest.EdgeSchemaFile, generated)
}
