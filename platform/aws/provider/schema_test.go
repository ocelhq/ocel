package provider

import (
	"testing"

	"github.com/ocelhq/ocel/pkg/configdoc"
	"github.com/ocelhq/ocel/pkg/configdoc/schematest"
	"github.com/ocelhq/ocel/platform/aws/provider/dns"
	"github.com/ocelhq/ocel/platform/aws/provider/edges"
)

func TestProviderSchemaIsCommitted(t *testing.T) {
	generated, err := configdoc.ProviderSchema(string(Vendor), Options{}, edges.SupportedEdges(), dns.Kinds())
	if err != nil {
		t.Fatalf("provider schema: %v", err)
	}
	schematest.AssertCommitted(t, schematest.ProviderSchemaFile, generated)
}
