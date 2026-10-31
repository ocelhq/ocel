package gcp

import (
	"testing"

	"github.com/ocelhq/ocel/pkg/configdoc"
	"github.com/ocelhq/ocel/pkg/configdoc/schematest"
	"github.com/ocelhq/ocel/platform/gcp/provider/control"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/gcloud"
)

func TestProviderSchemaIsCommitted(t *testing.T) {
	facts := newProvider(&control.Provider{}).Facts()
	generated, err := configdoc.ProviderSchema(string(gcloud.Vendor), control.Options{}, facts.Edges, facts.DNSKinds)
	if err != nil {
		t.Fatalf("provider schema: %v", err)
	}
	schematest.AssertCommitted(t, schematest.ProviderSchemaFile, generated)
}
