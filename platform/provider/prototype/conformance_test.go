package prototype

import (
	"testing"

	edge "github.com/ocelhq/ocel/platform/edge/contract"
	provider "github.com/ocelhq/ocel/platform/provider/contract"
	"github.com/ocelhq/ocel/platform/provider/contract/providerconformance"
	"github.com/ocelhq/ocel/platform/provider/prototype/awsshaped"
	"github.com/ocelhq/ocel/platform/provider/prototype/vpsshaped"
)

var spec = provider.Spec{
	Slug: "conf", Class: edge.ClassProduction, Environment: "production",
	Apps:      []provider.App{{Name: "web", Framework: "next", DeploymentID: "d-1", Build: provider.Build{Root: "b", Routes: []provider.Route{{ID: "default"}}}}},
	Resources: []provider.Resource{{Name: "main", Kind: provider.ResourcePostgres}, {Name: "uploads", Kind: provider.ResourceBucket}},
}

func TestAWSShaped(t *testing.T) {
	providerconformance.Run(t, providerconformance.Suite{New: func() provider.Provider { return awsshaped.New("eu-west-1") }, Spec: spec})
}

func TestVPSShaped(t *testing.T) {
	providerconformance.Run(t, providerconformance.Suite{New: func() provider.Provider { return vpsshaped.New("box") }, Spec: spec})
}
