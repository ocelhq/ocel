package functionnetwork

import (
	"slices"
	"testing"

	"github.com/ocelhq/ocel/pkg/environment"
)

func TestEveryFunctionNetworkResourceIsNamedForWhatItIsInTheAccount(t *testing.T) {
	t.Parallel()

	cloud := runFunctionNetwork(t, "ocel", environment.TierProduction)

	for token, want := range map[string][]string{
		"aws:ec2/eip:Eip":                     {"ocel-prod-functions-nat-1/eip", "ocel-prod-functions-nat-2/eip"},
		"aws:ec2/natGateway:NatGateway":       {"ocel-prod-functions-nat-1", "ocel-prod-functions-nat-2"},
		"aws:ec2/route:Route":                 {"ocel-prod-functions-private-1/nat", "ocel-prod-functions-private-2/nat"},
		"aws:ec2/securityGroup:SecurityGroup": {"ocel-prod-functions-sg"},
	} {
		got := cloud.registered(token)
		slices.Sort(got)
		if !slices.Equal(got, want) {
			t.Errorf("the program declares %s %v, want %v: a Pulumi name is the name the resource carries, or <parent>/<part> where it carries none", token, got, want)
		}
	}
}
