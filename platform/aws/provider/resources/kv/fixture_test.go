package kv

import (
	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/platform/aws/provider/component"
)

var testNetwork = component.Network{VPCID: "vpc-1", SubnetIDs: []string{"subnet-1", "subnet-2"}, Clients: "sg-vpc-clients"}

const testGroup = "ocel-app-shop-prod-cache-8bf31e"

func testStack(project string) component.Stack {
	return component.Stack{Ref: provider.StackRef{Tier: environment.TierProduction}, Namespace: "ocel", Project: project, Env: "prod", Network: testNetwork}
}
