package deploy

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsec2 "github.com/aws/aws-sdk-go-v2/service/ec2"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/aws/aws-sdk-go-v2/service/servicequotas"
	sqtypes "github.com/aws/aws-sdk-go-v2/service/servicequotas/types"
	"github.com/pulumi/pulumi/sdk/v3/go/auto"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/progress"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/fake"
	"github.com/ocelhq/ocel/pkg/records"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/pkg/stackrecords"
	"github.com/ocelhq/ocel/platform/aws/provider/edges/cloudfront"
	"github.com/ocelhq/ocel/platform/aws/provider/tier/network"
)

type networkRegion struct {
	defaultVPCNetwork
	vpcs []ec2types.Vpc
}

func (r networkRegion) DescribeVpcs(context.Context, *awsec2.DescribeVpcsInput, ...func(*awsec2.Options)) (*awsec2.DescribeVpcsOutput, error) {
	return &awsec2.DescribeVpcsOutput{Vpcs: r.vpcs}, nil
}

type roomyQuotas struct{}

func (roomyQuotas) GetServiceQuota(context.Context, *servicequotas.GetServiceQuotaInput, ...func(*servicequotas.Options)) (*servicequotas.GetServiceQuotaOutput, error) {
	return &servicequotas.GetServiceQuotaOutput{Quota: &sqtypes.ServiceQuota{Value: aws.Float64(5)}}, nil
}

func (roomyQuotas) GetAWSDefaultServiceQuota(context.Context, *servicequotas.GetAWSDefaultServiceQuotaInput, ...func(*servicequotas.Options)) (*servicequotas.GetAWSDefaultServiceQuotaOutput, error) {
	return &servicequotas.GetAWSDefaultServiceQuotaOutput{Quota: &sqtypes.ServiceQuota{Value: aws.Float64(5)}}, nil
}

func tierNetworkOutputs(cidr string) auto.OutputMap {
	encoded, _ := json.Marshal(network.Network{
		VPC:           "vpc-ocel",
		CIDR:          cidr,
		Zones:         []string{"use1-az1", "use1-az2"},
		Public:        []string{"subnet-public-a", "subnet-public-b"},
		Private:       []string{"subnet-private-a", "subnet-private-b"},
		Isolated:      []string{"subnet-isolated-a", "subnet-isolated-b"},
		PrivateRoutes: []string{"rtb-private-a", "rtb-private-b"},
		Clients:       "sg-clients",
	})
	return auto.OutputMap{"network": auto.OutputValue{Value: string(encoded)}}
}

func networkStacks(t *testing.T, cfg Config) (*Stacks, *mockedEngine, records.Store) {
	t.Helper()
	store := fake.NewKeyValues()
	cfg.KeyValues = store
	cfg.Tier = environment.TierProduction
	cfg.Region = "us-east-1"
	cfg.Namespace = "ocel"
	cfg.BackendURL = "s3://ocel-state/ocel-network"
	cfg.Passphrase = "a-passphrase"
	cfg.PulumiProject = "ocel-network"
	if cfg.Network == nil {
		cfg.Network = networkRegion{}
	}
	cfg.Quotas = roomyQuotas{}
	engine := &mockedEngine{outputs: tierNetworkOutputs(network.DefaultCIDR("ocel", environment.TierProduction))}
	return stacksWith(cfg, engine), engine, store
}

func consumerRef(project string) provider.StackRef {
	return provider.StackRef{Project: project, Tier: environment.TierProduction, Name: naming.InfraStack("prod")}
}

func TestTheFirstConsumerOfATiersNetworkProvisionsItAndTheLastToLeaveTakesItDown(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	stacks, engine, store := networkStacks(t, routedConfig(t, cloudfront.Kind))
	containers, shop := consumerRef("ocel-containers"), consumerRef("shop")

	said := &recordingProgress{}
	claimed, err := stacks.Network().Claim(ctx, containers, said)
	if err != nil {
		t.Fatalf("Claim(containers) = %v", err)
	}
	if claimed.VPC != "vpc-ocel" || claimed.Clients != "sg-clients" {
		t.Errorf("Claim() = %+v, want the network the stack exported", claimed)
	}
	if !strings.Contains(strings.Join(said.reported(), "\n"), network.DefaultCIDR("ocel", environment.TierProduction)) {
		t.Errorf("progress = %v, want it to name the block the VPC takes", said.reported())
	}
	if _, err := stacks.Network().Claim(ctx, shop, progress.Discard()); err != nil {
		t.Fatalf("Claim(shop) = %v", err)
	}
	if ran := engine.stacks(); len(ran) != 1 || ran[0] != network.StackRef(environment.TierProduction).Name.String() {
		t.Fatalf("two claims ran %v, want the network provisioned once", ran)
	}
	part, present, err := network.ReadPart(ctx, store, "us-east-1", environment.TierProduction)
	if err != nil || !present || !part.Current || !part.RetiresWithLastUser || len(part.UsedBy) != 2 || len(part.Resources) == 0 {
		t.Fatalf("ReadPart() = %+v, %v, %v, want a current part used by both consumers that retires with the last", part, present, err)
	}

	if err := stacks.Network().Release(ctx, store, containers, progress.Discard()); err != nil {
		t.Fatalf("Release(containers) = %v", err)
	}
	if torn := engine.torn(); len(torn) != 0 {
		t.Fatalf("one of two consumers leaving tore down %v, want nothing: shop's database still sits in it", torn)
	}
	if err := stacks.Network().Release(ctx, store, shop, progress.Discard()); err != nil {
		t.Fatalf("Release(shop) = %v", err)
	}
	if torn := engine.torn(); len(torn) != 1 {
		t.Fatalf("the last consumer leaving tore down %v, want the network", torn)
	}
	if _, present, err := stackrecords.ReadBootstrapPart(ctx, store, environment.TierProduction, network.Part); err != nil || present {
		t.Errorf("network part present %v (%v) after its last consumer left, want it forgotten", present, err)
	}
}

func TestAClaimWhoseConfigAsksForAnotherBlockThanTheNetworkHoldsIsRefused(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	cfg := routedConfig(t, cloudfront.Kind)
	cfg.NetworkCIDR = "10.50.0.0/16"
	stacks, _, _ := networkStacks(t, cfg)
	if _, err := stacks.Network().Claim(ctx, consumerRef("shop"), progress.Discard()); err != nil {
		t.Fatalf("Claim() = %v", err)
	}

	_, err := stacks.Network().Claim(ctx, consumerRef("admin"), progress.Discard())
	var refused refusal.Refusal
	if !errors.As(err, &refused) || !strings.Contains(refused.Message, network.DefaultCIDR("ocel", environment.TierProduction)) || !strings.Contains(refused.Message, "network.production.cidr") {
		t.Fatalf("Claim() = %v, want a refusal naming the block the VPC holds and the option asking for another", err)
	}
}

func TestANetworkWhoseBlockOverlapsAVPCInTheRegionIsRefusedBeforeAnythingIsProvisioned(t *testing.T) {
	t.Parallel()

	cfg := routedConfig(t, cloudfront.Kind)
	cfg.NetworkCIDR = "10.20.0.0/16"
	cfg.Network = networkRegion{vpcs: []ec2types.Vpc{{VpcId: aws.String("vpc-peered"), CidrBlock: aws.String("10.20.4.0/24")}}}
	stacks, engine, store := networkStacks(t, cfg)

	_, err := stacks.Network().Claim(context.Background(), consumerRef("shop"), progress.Discard())
	var refused refusal.Refusal
	if !errors.As(err, &refused) || !strings.Contains(refused.Message, "vpc-peered") {
		t.Fatalf("Claim() = %v, want a refusal naming the VPC in the way", err)
	}
	if ran := engine.stacks(); len(ran) != 0 {
		t.Errorf("ran %v, want nothing provisioned", ran)
	}
	if _, present, _ := stackrecords.ReadBootstrapPart(context.Background(), store, environment.TierProduction, network.Part); present {
		t.Error("the network part is recorded though nothing was provisioned")
	}
}
