package functionnetwork

import (
	"testing"

	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"

	"github.com/ocelhq/ocel/pkg/provider"
)

func TestOnlyProvisionedPostgresAndKVNeedTheFunctionNetwork(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name   string
		grants []provider.Binding
		want   bool
	}{
		{"no grant", nil, false},
		{"a bucket", []provider.Binding{{Type: provider.BindingBucket, Name: "bucket--uploads"}}, false},
		{"a topic and a task", []provider.Binding{{Type: provider.BindingTopic, Name: "topic--a"}, {Type: provider.BindingTask, Name: "task--b"}}, false},
		{"provisioned postgres", []provider.Binding{{Type: provider.BindingPostgres, Name: "db--main"}}, true},
		{"provisioned kv", []provider.Binding{{Type: provider.BindingKV, Name: "kv--cache"}}, true},
		{"postgres the user's own IaC provisions", []provider.Binding{{Type: provider.BindingPostgres, Name: "db--main", Source: "terraform"}}, false},
		{"kv another project published", []provider.Binding{{Type: provider.BindingKV, Name: "kv--cache", Source: "ocel"}}, false},
		{"a bucket beside provisioned postgres", []provider.Binding{{Type: provider.BindingBucket, Name: "bucket--uploads"}, {Type: provider.BindingPostgres, Name: "db--main"}}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			if got := IsNeededForAny(tc.grants); got != tc.want {
				t.Errorf("IsNeededForAny() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestAFunctionInTheNetworkJoinsTheTiersVPCClientsSoItsDatabasesAdmitIt(t *testing.T) {
	t.Parallel()

	joined := (&Network{SecurityGroup: "sg-functions", Clients: "sg-vpc-clients"}).securityGroups()
	if len(joined) != 2 || joined[0] != pulumi.String("sg-functions") || joined[1] != pulumi.String("sg-vpc-clients") {
		t.Errorf("a function joins %v, want its own group and the tier's vpc-clients group", joined)
	}
	if alone := (&Network{SecurityGroup: "sg-functions"}).securityGroups(); len(alone) != 1 {
		t.Errorf("a function joins %v with no clients group recorded, want its own group alone", alone)
	}
}
