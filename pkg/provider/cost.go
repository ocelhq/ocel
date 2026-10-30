package provider

import (
	"github.com/ocelhq/ocel/pkg/buildoutput"
	"github.com/ocelhq/ocel/pkg/edge"
	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"
)

const CostSource = "ocel"

type InventoryRequest struct {
	Deploy     DeploySpec
	Edge       edge.Kind
	Parts      []string
	Resources  []Resource
	Functions  map[string][]FunctionSpec
	Usages     []AppUsage
	Transforms []string
	Uses       []buildoutput.Uses
}

func ProductionHostnames(app AppEntry) []string {
	for _, domains := range app.Manifest.GetDomains() {
		if domains.GetTier() == environmentv1.Tier_TIER_PRODUCTION {
			return domains.GetHostnames()
		}
	}
	return nil
}
