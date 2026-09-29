package fake_test

import (
	"testing"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/provider/fake"
)

func TestAnEdgeDescribesPermissionsOnlyOnceItIsGivenADocument(t *testing.T) {
	t.Parallel()

	edges := fake.NewEdges()
	relay := edges.Edge(fake.KindRelay)
	if relay.Hooks().DescribeCredentialPermissions != nil {
		t.Fatal("the relay edge describes permissions before it is given a document")
	}

	relay.DocumentsPermissions(edge.CredentialDocument{Heading: "relay token", Document: "scripts · edit"})
	documented, err := relay.Hooks().DescribeCredentialPermissions(edge.PurposeDeploy)
	if err != nil {
		t.Fatalf("DescribeCredentialPermissions err = %v", err)
	}
	if documented.Heading != "relay token" || documented.Document != "scripts · edit for deploy" {
		t.Errorf("document = %+v, want the relay token's scopes for the deploy purpose", documented)
	}
}
