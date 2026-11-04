package deploy

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/ocelhq/ocel/platform/aws/provider/tier/sharedparts"

	"github.com/pulumi/pulumi/sdk/v3/go/auto"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/records"
	"github.com/ocelhq/ocel/pkg/stackrecords"
	"github.com/ocelhq/ocel/platform/aws/provider/tier/network"
)

func functionNetworkOutputs() auto.OutputMap {
	subnets, _ := json.Marshal([]string{"subnet-private-a", "subnet-private-b"})
	outputs := auto.OutputMap{
		"privateSubnets":        auto.OutputValue{Value: string(subnets)},
		"functionSecurityGroup": auto.OutputValue{Value: "sg-functions"},
		"clientsSecurityGroup":  auto.OutputValue{Value: "sg-clients"},
	}
	for key, value := range tierNetworkOutputs("10.20.0.0/16") {
		outputs[key] = value
	}
	return outputs
}

func seedTierNetwork(t *testing.T, store records.Store, holder string) {
	t.Helper()
	ctx := context.Background()
	tier := environment.TierProduction
	if err := stackrecords.Write(ctx, store, tier, sharedparts.NetworkSlug, network.StackRef(tier).Name, stackrecords.Stack{Kind: provider.StackInfra, WrittenBy: "1.0.0"}); err != nil {
		t.Fatal(err)
	}
	held := network.Shared.ConsumerAt(provider.StackRef{Project: holder, Tier: tier, Name: naming.InfraStack("prod")})
	if _, err := store.Write(ctx, records.Entry{Key: held, Value: []byte(`{}`)}); err != nil {
		t.Fatal(err)
	}
}
