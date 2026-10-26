package deploy

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

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
	"github.com/ocelhq/ocel/pkg/provider/transform"
	"github.com/ocelhq/ocel/pkg/records"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/pkg/stackrecords"
	"github.com/ocelhq/ocel/platform/aws/provider/edges/cloudfront"
	"github.com/ocelhq/ocel/platform/aws/provider/tier/containers"
	"github.com/ocelhq/ocel/platform/aws/provider/tier/functionnetwork"
	"github.com/ocelhq/ocel/platform/aws/provider/tier/network"
	"github.com/ocelhq/ocel/platform/aws/provider/tier/sharedstack"
)

type networkRegion struct {
	defaultVPCNetwork
	vpcs []ec2types.Vpc
}

func (r networkRegion) DescribeVpcs(context.Context, *awsec2.DescribeVpcsInput, ...func(*awsec2.Options)) (*awsec2.DescribeVpcsOutput, error) {
	return &awsec2.DescribeVpcsOutput{Vpcs: r.vpcs}, nil
}

type changingRegion struct {
	defaultVPCNetwork
	vpcs *[]ec2types.Vpc
}

func (r changingRegion) DescribeVpcs(context.Context, *awsec2.DescribeVpcsInput, ...func(*awsec2.Options)) (*awsec2.DescribeVpcsOutput, error) {
	return &awsec2.DescribeVpcsOutput{Vpcs: *r.vpcs}, nil
}

type roomyQuotas struct{}

func (roomyQuotas) GetServiceQuota(context.Context, *servicequotas.GetServiceQuotaInput, ...func(*servicequotas.Options)) (*servicequotas.GetServiceQuotaOutput, error) {
	return &servicequotas.GetServiceQuotaOutput{Quota: &sqtypes.ServiceQuota{Value: aws.Float64(5)}}, nil
}

func (roomyQuotas) GetAWSDefaultServiceQuota(context.Context, *servicequotas.GetAWSDefaultServiceQuotaInput, ...func(*servicequotas.Options)) (*servicequotas.GetAWSDefaultServiceQuotaOutput, error) {
	return &servicequotas.GetAWSDefaultServiceQuotaOutput{Quota: &sqtypes.ServiceQuota{Value: aws.Float64(5)}}, nil
}

func tierNetworkOutputs(cidr string) auto.OutputMap {
	return patchedNetworkOutputs(cidr, "")
}

func patchedNetworkOutputs(cidr, digest string) auto.OutputMap {
	encoded, _ := json.Marshal(network.Network{
		PatchDigest:   digest,
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
	if ups, _ := engine.networkRuns(); ups != 1 {
		t.Fatalf("two claims ran the network %d times, want it provisioned once", ups)
	}
	part, present, err := network.ReadPart(ctx, store, "us-east-1", environment.TierProduction)
	if err != nil || !present || !part.Current || !part.RetiresWithLastUser || len(part.UsedBy) != 2 || len(part.Resources) == 0 {
		t.Fatalf("ReadPart() = %+v, %v, %v, want a current part used by both consumers that retires with the last", part, present, err)
	}

	if err := stacks.Network().Release(ctx, store, containers, progress.Discard()); err != nil {
		t.Fatalf("Release(containers) = %v", err)
	}
	if _, downs := engine.networkRuns(); downs != 0 {
		t.Fatalf("one of two consumers leaving tore the network down %d times, want never: shop's database still sits in it", downs)
	}
	if err := stacks.Network().Release(ctx, store, shop, progress.Discard()); err != nil {
		t.Fatalf("Release(shop) = %v", err)
	}
	if _, downs := engine.networkRuns(); downs != 1 {
		t.Fatalf("the last consumer leaving tore the network down %d times, want once", downs)
	}
	if _, present, err := stackrecords.ReadBootstrapPart(ctx, store, environment.TierProduction, network.Part); err != nil || present {
		t.Errorf("network part present %v (%v) after its last consumer left, want it forgotten", present, err)
	}
}

func blockPatchedTo(cidr string) *fixedPass {
	return &fixedPass{result: transform.Result{Patches: transform.Patches{"vpc": {"cidrBlock": cidr}}}}
}

func TestANetworkIsRaisedOnTheBlockATransformSets(t *testing.T) {
	t.Parallel()

	cfg := routedConfig(t, cloudfront.Kind)
	cfg.Transform = blockPatchedTo("172.20.0.0/16")
	stacks, _, _ := networkStacks(t, cfg)

	said := &recordingProgress{}
	if _, err := stacks.Network().Claim(context.Background(), consumerRef("shop"), said); err != nil {
		t.Fatalf("Claim() = %v", err)
	}
	if !strings.Contains(strings.Join(said.reported(), "\n"), "a VPC of 172.20.0.0/16") {
		t.Errorf("progress = %v, want the VPC raised on the block the transform sets", said.reported())
	}
}

func TestABlockOutsideWhatOcelCanCarveIsRefusedBeforeAnythingIsProvisioned(t *testing.T) {
	t.Parallel()

	for _, cidr := range []string{"10.50.0.0/25", "10.50.0.0/12", "100.64.0.0/16", "not-a-block"} {
		cfg := routedConfig(t, cloudfront.Kind)
		cfg.Transform = blockPatchedTo(cidr)
		stacks, engine, _ := networkStacks(t, cfg)

		_, err := stacks.Network().Claim(context.Background(), consumerRef("shop"), progress.Discard())
		var refused refusal.Refusal
		if !errors.As(err, &refused) || !strings.Contains(refused.Message, cidr) {
			t.Errorf("Claim() with %s = %v, want a refusal naming the block", cidr, err)
		}
		if ups, _ := engine.networkRuns(); ups != 0 {
			t.Errorf("ran the network %d times for %s, want nothing provisioned", ups, cidr)
		}
	}
}

func TestAClaimWhoseTransformAsksForAnotherBlockThanTheNetworkHoldsIsRefused(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	cfg := routedConfig(t, cloudfront.Kind)
	cfg.Transform = blockPatchedTo("10.50.0.0/16")
	stacks, _, _ := networkStacks(t, cfg)
	if _, err := stacks.Network().Claim(ctx, consumerRef("shop"), progress.Discard()); err != nil {
		t.Fatalf("Claim() = %v", err)
	}

	_, err := stacks.Network().Claim(ctx, consumerRef("admin"), progress.Discard())
	var refused refusal.Refusal
	if !errors.As(err, &refused) || !strings.Contains(refused.Message, network.DefaultCIDR("ocel", environment.TierProduction)) || !strings.Contains(refused.Message, "vpc.cidrBlock") {
		t.Fatalf("Claim() = %v, want a refusal naming the block the VPC holds and the patch asking for another", err)
	}
}

func TestARaisedNetworkKeepsTheBlockItWasRaisedWithThoughAnotherVPCNowOverlapsIt(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	held := network.DefaultCIDR("ocel", environment.TierProduction)
	vpcs := []ec2types.Vpc{}
	cfg := routedConfig(t, cloudfront.Kind)
	cfg.Network = changingRegion{vpcs: &vpcs}
	stacks, engine, _ := networkStacks(t, cfg)
	if _, err := stacks.Network().Claim(ctx, consumerRef("shop"), progress.Discard()); err != nil {
		t.Fatalf("Claim(shop) = %v", err)
	}

	vpcs = append(vpcs, ec2types.Vpc{VpcId: aws.String("vpc-later"), CidrBlock: aws.String(held)})
	claimed, err := stacks.Network().Claim(ctx, consumerRef("admin"), progress.Discard())
	if err != nil || claimed.CIDR != held {
		t.Fatalf("Claim(admin) = %+v, %v, want the network on %s it was raised with: a VPC made since does not move it", claimed, err, held)
	}
	if ups, _ := engine.networkRuns(); ups != 1 {
		t.Errorf("the network was provisioned %d times, want once", ups)
	}
}

func TestANetworkWhoseBlockOverlapsAVPCInTheRegionIsRefusedBeforeAnythingIsProvisioned(t *testing.T) {
	t.Parallel()

	cfg := routedConfig(t, cloudfront.Kind)
	cfg.Transform = blockPatchedTo("10.20.0.0/16")
	cfg.Network = networkRegion{vpcs: []ec2types.Vpc{{VpcId: aws.String("vpc-peered"), CidrBlock: aws.String("10.20.4.0/24")}}}
	stacks, engine, store := networkStacks(t, cfg)

	_, err := stacks.Network().Claim(context.Background(), consumerRef("shop"), progress.Discard())
	var refused refusal.Refusal
	if !errors.As(err, &refused) || !strings.Contains(refused.Message, "vpc-peered") {
		t.Fatalf("Claim() = %v, want a refusal naming the VPC in the way", err)
	}
	if ups, _ := engine.networkRuns(); ups != 0 {
		t.Errorf("ran the network %d times, want nothing provisioned", ups)
	}
	if _, present, _ := stackrecords.ReadBootstrapPart(context.Background(), store, environment.TierProduction, network.Part); present {
		t.Error("the network part is recorded though nothing was provisioned")
	}
}

func TestTheContainerInfraRunsInTheTiersNetworkAndLetsGoOfItWhenItGoes(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	cfg, spec := containerPartSpec(t, "v1.0.0")
	engine := &mockedEngine{outputs: containerPartOutputs()}
	stacks := stacksWith(cfg, engine)
	if _, err := stacks.Provision(ctx, spec, progress.Discard()); err != nil {
		t.Fatalf("Provision() = %v", err)
	}
	if ups, _ := engine.networkRuns(); ups != 1 {
		t.Fatalf("the first container deploy raised the network %d times, want once: the cluster's load balancer sits in its public subnets", ups)
	}
	users, err := network.Shared.ListUsers(ctx, cfg.KeyValues, environment.TierProduction)
	if err != nil || len(users) != 1 || users[0] != containers.Slug {
		t.Fatalf("the network is used by %v (%v), want the containers part alone: a container app holds the containers part, not the network", users, err)
	}

	if err := stacks.Destroy(ctx, provider.StackDestroy{Ref: spec.Ref}, progress.Discard()); err != nil {
		t.Fatalf("Destroy() = %v", err)
	}
	if _, downs := engine.networkRuns(); downs != 1 {
		t.Fatalf("the last container app leaving tore the network down %d times, want once, after the containers part it held", downs)
	}
	if _, present, err := stackrecords.ReadBootstrapPart(ctx, cfg.KeyValues, environment.TierProduction, network.Part); err != nil || present {
		t.Errorf("network part present %v (%v) after the containers part went, want it forgotten", present, err)
	}
}

func TestADatabaseSitsInTheIsolatedSubnetsOfTheTiersNetworkAndAdmitsItsClientsGroup(t *testing.T) {
	t.Parallel()

	rec := &inputRecorder{}
	engine := &mockedEngine{outputs: provisionedOutputs(), mocks: secretedCluster{rec}}
	spec := provider.StackSpec{
		Ref:       provider.StackRef{Project: "conformance", Tier: environment.TierProduction, Name: naming.InfraStack("conformance")},
		Kind:      provider.StackInfra,
		Recorded:  true,
		Resources: []provider.Resource{{Name: "c-postgres", Type: provider.BindingPostgres, Postgres: &provider.PostgresSpec{}}},
	}
	if _, err := conformingStacks(engine).Provision(context.Background(), spec, nil); err != nil {
		t.Fatalf("Provision() = %v", err)
	}

	groups := rec.registered("aws:rds/subnetGroup:SubnetGroup")
	if len(groups) != 1 {
		t.Fatalf("declared subnet groups %v, want the database's", groups)
	}
	if got := stringsAt(rec.inputs("aws:rds/subnetGroup:SubnetGroup", groups[0]), "subnetIds"); !slices.Equal(got, []string{"subnet-isolated-a", "subnet-isolated-b"}) {
		t.Errorf("the database's subnets are %v, want the network's isolated subnets, which have no route out", got)
	}
	for _, name := range rec.registered("aws:ec2/securityGroup:SecurityGroup") {
		group := rec.inputs("aws:ec2/securityGroup:SecurityGroup", name)
		if group["vpcId"].StringValue() != "vpc-ocel" {
			t.Errorf("security group %s sits in %q, want the tier's network", name, group["vpcId"].StringValue())
		}
		admitted := group["ingress"].ArrayValue()[0].ObjectValue()
		if got := stringsAt(admitted, "securityGroups"); !slices.Equal(got, []string{"sg-clients"}) {
			t.Errorf("security group %s admits %v, want the network's clients group alone", name, got)
		}
	}
}

func TestThePlanOfTheFirstDatabaseInATierShowsTheNetworkItRaises(t *testing.T) {
	t.Parallel()

	engine := &mockedEngine{outputs: provisionedOutputs()}
	plan, err := conformingStacks(engine).Plan(context.Background(), provider.StackSpec{
		Ref:       provider.StackRef{Project: "conformance", Tier: environment.TierProduction, Name: naming.InfraStack("conformance")},
		Kind:      provider.StackInfra,
		Recorded:  true,
		Resources: []provider.Resource{{Name: "c-postgres", Type: provider.BindingPostgres, Postgres: &provider.PostgresSpec{}}},
	}, nil)
	if err != nil {
		t.Fatalf("Plan() = %v", err)
	}
	if len(plan.Groups) == 0 || plan.Groups[0].Part != network.Part || plan.Groups[0].Action != provider.ActionCreate {
		t.Fatalf("plan groups = %+v, want the network part created first", plan.Groups)
	}
	if ups, _ := engine.networkRuns(); ups != 0 {
		t.Errorf("planning raised the network %d times, want never", ups)
	}
}

func TestThePlanOfTheFirstDatabaseInATierRefusesABlockOcelCannotCarveTheNetworkFrom(t *testing.T) {
	t.Parallel()

	engine := &mockedEngine{outputs: provisionedOutputs()}
	cfg := conformingConfig(&fakeArtifactStore{})
	cfg.Transform = blockPatchedTo("10.50.0.0/12")
	_, err := stacksWith(cfg, engine).Plan(context.Background(), provider.StackSpec{
		Ref:       provider.StackRef{Project: "conformance", Tier: environment.TierProduction, Name: naming.InfraStack("conformance")},
		Kind:      provider.StackInfra,
		Recorded:  true,
		Resources: []provider.Resource{{Name: "c-postgres", Type: provider.BindingPostgres, Postgres: &provider.PostgresSpec{}}},
	}, nil)
	var refused refusal.Refusal
	if !errors.As(err, &refused) || !strings.Contains(refused.Message, "vpc.cidrBlock") || !strings.Contains(refused.Message, "10.50.0.0/12") {
		t.Fatalf("Plan() = %v, want a refusal naming vpc.cidrBlock and the block: the deploy would raise the network on it", err)
	}
}

func TestThePlanOfADatabaseWhoseTransformAsksForAnotherBlockThanTheNetworkHoldsIsRefused(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	engine := &mockedEngine{outputs: provisionedOutputs()}
	engine.outputs["network"] = patchedNetworkOutputs("10.20.0.0/16", "")["network"]
	cfg := conformingConfig(&fakeArtifactStore{})
	cfg.Transform = blockPatchedTo("10.50.0.0/16")
	stacks := stacksWith(cfg, engine)
	if _, err := stacks.Network().ClaimWithoutRepatch(ctx, consumerRef("blog"), progress.Discard()); err != nil {
		t.Fatalf("ClaimWithoutRepatch(blog) = %v", err)
	}

	_, err := stacks.Plan(ctx, provider.StackSpec{
		Ref:       provider.StackRef{Project: "conformance", Tier: environment.TierProduction, Name: naming.InfraStack("conformance")},
		Kind:      provider.StackInfra,
		Recorded:  true,
		Resources: []provider.Resource{{Name: "c-postgres", Type: provider.BindingPostgres, Postgres: &provider.PostgresSpec{}}},
	}, nil)
	var refused refusal.Refusal
	if !errors.As(err, &refused) || !strings.Contains(refused.Message, "10.20.0.0/16") || !strings.Contains(refused.Message, "vpc.cidrBlock") {
		t.Fatalf("Plan() = %v, want a refusal naming the block the VPC holds and the patch asking for another", err)
	}
}

func TestThePlanOfADatabaseWhoseTransformsPatchTheNetworkOtherwiseThanItCarriesShowsTheNetworkProvisionedAgain(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	engine := &mockedEngine{outputs: provisionedOutputs()}
	engine.outputs["network"] = patchedNetworkOutputs("10.20.0.0/16", "patched-by-another-project")["network"]
	cfg := conformingConfig(&fakeArtifactStore{})
	cfg.Transform = &fixedPass{result: transform.Result{Patches: transform.Patches{"vpc": {"enableNetworkAddressUsageMetrics": true}}}}
	stacks := stacksWith(cfg, engine)
	spec := provider.StackSpec{
		Ref:       provider.StackRef{Project: "conformance", Tier: environment.TierProduction, Name: naming.InfraStack("conformance")},
		Kind:      provider.StackInfra,
		Recorded:  true,
		Resources: []provider.Resource{{Name: "c-postgres", Type: provider.BindingPostgres, Postgres: &provider.PostgresSpec{}}},
	}
	if _, err := stacks.Provision(ctx, spec, nil); err != nil {
		t.Fatalf("Provision() = %v", err)
	}

	plan, err := stacks.Plan(ctx, spec, nil)
	if err != nil {
		t.Fatalf("Plan() = %v", err)
	}
	if !slices.ContainsFunc(plan.Groups, func(group provider.ChangeGroup) bool {
		return group.Part == network.Part && group.Action == provider.ActionUpdate
	}) {
		t.Errorf("plan groups = %+v, want the network provisioned again: the deploy repatches it for every project in the tier", plan.Groups)
	}
}

func TestAnInfraStackThatDropsItsLastDatabaseLetsGoOfTheTiersNetwork(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	engine := &mockedEngine{outputs: provisionedOutputs()}
	stacks := conformingStacks(engine)
	spec := provider.StackSpec{
		Ref:       provider.StackRef{Project: "conformance", Tier: environment.TierProduction, Name: naming.InfraStack("conformance")},
		Kind:      provider.StackInfra,
		Recorded:  true,
		Resources: []provider.Resource{{Name: "c-postgres", Type: provider.BindingPostgres, Postgres: &provider.PostgresSpec{}}},
	}
	if _, err := stacks.Provision(ctx, spec, nil); err != nil {
		t.Fatalf("Provision(with a database) = %v", err)
	}
	spec.Resources = []provider.Resource{{Name: "c-bucket", Type: provider.BindingBucket, Bucket: &provider.BucketSpec{}}}
	if _, err := stacks.Provision(ctx, spec, nil); err != nil {
		t.Fatalf("Provision(without one) = %v", err)
	}
	if _, downs := engine.networkRuns(); downs != 1 {
		t.Errorf("dropping the only database tore the network down %d times, want once: nothing else runs in it", downs)
	}
}

func TestTheFunctionNetworkLetsGoOfTheTiersNetworkWhenItsLastAppLeaves(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	stacks, _, store := functionNetworkStacks(t, "web")
	web := usingPostgres(t, "web")
	if _, err := stacks.Provision(ctx, web, progress.Discard()); err != nil {
		t.Fatalf("Provision() = %v", err)
	}
	users, err := network.Shared.ListUsers(ctx, store, environment.TierProduction)
	if err != nil || !slices.Contains(users, functionnetwork.Slug) {
		t.Fatalf("the network is used by %v (%v), want the function network among them", users, err)
	}
	if err := stacks.Destroy(ctx, provider.StackDestroy{Ref: web.Ref}, progress.Discard()); err != nil {
		t.Fatalf("Destroy() = %v", err)
	}
	if users, err := network.Shared.ListUsers(ctx, store, environment.TierProduction); err != nil || slices.Contains(users, functionnetwork.Slug) {
		t.Errorf("the network is used by %v (%v) after the function network went, want it let go of", users, err)
	}
}

type addressesHeld struct {
	networkRegion
	held int
	own  int
}

func (a addressesHeld) DescribeAddresses(context.Context, *awsec2.DescribeAddressesInput, ...func(*awsec2.Options)) (*awsec2.DescribeAddressesOutput, error) {
	addresses := make([]ec2types.Address, a.held)
	for range a.own {
		addresses = append(addresses, ec2types.Address{Tags: []ec2types.Tag{
			{Key: aws.String("ocel:project"), Value: aws.String(functionnetwork.Slug)},
			{Key: aws.String(naming.ManagedByTagKey), Value: aws.String(testNamespace)},
			{Key: aws.String(naming.EnvTierTagKey), Value: aws.String(string(environment.TierProduction))},
		}})
	}
	return &awsec2.DescribeAddressesOutput{Addresses: addresses}, nil
}

func TestAFunctionNetworkRaisedAgainCountsNoneOfTheElasticIPsItsFailedRaiseHoldsAgainstTheQuota(t *testing.T) {
	t.Parallel()

	stacks, engine, _ := functionNetworkStacksOver(t, addressesHeld{held: 3, own: 2}, nil, "web")
	if _, err := stacks.Provision(context.Background(), usingPostgres(t, "web"), progress.Discard()); err != nil {
		t.Fatalf("Provision() = %v, want the function network raised: its own two addresses are the two it needs, beside three others of five", err)
	}
	if ran := engine.stacks(); !slices.Contains(ran, functionnetwork.StackRef(environment.TierProduction).Name.String()) {
		t.Errorf("ran %v, want the function network provisioned", ran)
	}
}

func TestAFunctionNetworkTheAccountHasNoElasticIPsLeftForIsRefusedNamingTheQuota(t *testing.T) {
	t.Parallel()

	stacks, engine, _ := functionNetworkStacksOver(t, addressesHeld{held: 4}, nil, "web")
	_, err := stacks.Provision(context.Background(), usingPostgres(t, "web"), progress.Discard())
	var refused refusal.Refusal
	if !errors.As(err, &refused) || !strings.Contains(refused.Message, "L-0263D0A3") {
		t.Fatalf("Provision() = %v, want a refusal naming quota L-0263D0A3: two NAT gateways need two more of five", err)
	}
	if ran := engine.stacks(); len(ran) != 0 {
		t.Errorf("ran %v, want nothing provisioned", ran)
	}
}

func TestThePlanOfTheFirstContainerAppInATierShowsTheNetworkBeforeTheContainerInfra(t *testing.T) {
	t.Parallel()

	cfg, spec := containerPartSpec(t, "v1.0.0")
	engine := &mockedEngine{outputs: containerPartOutputs()}
	plan, err := stacksWith(cfg, engine).Plan(context.Background(), spec, progress.Discard())
	if err != nil {
		t.Fatalf("Plan() = %v", err)
	}
	if len(plan.Groups) < 2 || plan.Groups[0].Part != network.Part || plan.Groups[1].Name != containers.StackRef(environment.TierProduction).Name.String() {
		t.Fatalf("plan groups = %+v, want the network part and then the container infrastructure it runs in", plan.Groups)
	}
}

func TestThePlanOfTheFirstContainerAppInATierRefusesABlockOcelCannotCarveTheNetworkFrom(t *testing.T) {
	t.Parallel()

	cfg, spec := containerPartSpec(t, "v1.0.0")
	cfg.Transform = blockPatchedTo("not-a-block")
	_, err := stacksWith(cfg, &mockedEngine{outputs: containerPartOutputs()}).Plan(context.Background(), spec, progress.Discard())
	var refused refusal.Refusal
	if !errors.As(err, &refused) || !strings.Contains(refused.Message, "vpc.cidrBlock") || !strings.Contains(refused.Message, "not-a-block") {
		t.Fatalf("Plan() = %v, want a refusal naming vpc.cidrBlock and the block: the deploy would raise the network on it", err)
	}
}

func TestTheNetworkIsProvisionedAgainOnlyWhenTheTransformsThatPatchItDiffer(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	pass := &fixedPass{result: transform.Result{Patches: transform.Patches{"vpc": {"enableNetworkAddressUsageMetrics": true}}}}
	cfg := routedConfig(t, cloudfront.Kind)
	cfg.Transform = pass
	stacks, engine, _ := networkStacks(t, cfg)
	engine.outputs = patchedNetworkOutputs(network.DefaultCIDR("ocel", environment.TierProduction), "patched-by-another-project")

	if _, err := stacks.Network().Claim(ctx, consumerRef("shop"), progress.Discard()); err != nil {
		t.Fatalf("Claim(shop) = %v", err)
	}
	said := &recordingProgress{}
	if _, err := stacks.Network().Claim(ctx, consumerRef("admin"), said); err != nil {
		t.Fatalf("Claim(admin) = %v", err)
	}
	if ups, _ := engine.networkRuns(); ups != 2 {
		t.Fatalf("ran the network %d times, want it raised and then provisioned again with this project's patches", ups)
	}
	if !strings.Contains(strings.Join(said.reported(), "\n"), "transforms") {
		t.Errorf("progress = %v, want it to say the transforms changed the network", said.reported())
	}

	evaluated, err := evaluateTierPatches(ctx, &fixedPass{result: pass.result}, testNamespace, environment.TierProduction)
	if err != nil {
		t.Fatal(err)
	}
	engine.outputs = patchedNetworkOutputs(network.DefaultCIDR("ocel", environment.TierProduction), evaluated.network.Digest)
	if _, err := stacks.Network().Claim(ctx, consumerRef("blog"), progress.Discard()); err != nil {
		t.Fatalf("Claim(blog) = %v", err)
	}
	if ups, _ := engine.networkRuns(); ups != 2 {
		t.Errorf("ran the network %d times, want no run for patches it already carries", ups)
	}
	if len(pass.asked) != 1 {
		t.Errorf("evaluated the transforms %d times, want once for the tier", len(pass.asked))
	}
}

func TestTheFunctionNetworkIsProvisionedAgainWhenTheTransformsThatPatchItsNATGatewaysDiffer(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	pass := &fixedPass{result: transform.Result{Patches: transform.Patches{"natGateway": {"secondaryPrivateIpAddressCount": float64(2)}}}}
	stacks, engine, _ := functionNetworkStacksOver(t, defaultVPCNetwork{}, pass, "web", "admin")
	if _, err := stacks.Provision(ctx, usingPostgres(t, "web"), progress.Discard()); err != nil {
		t.Fatalf("Provision(web) = %v", err)
	}
	if _, err := stacks.Provision(ctx, usingPostgres(t, "admin"), progress.Discard()); err != nil {
		t.Fatalf("Provision(admin) = %v", err)
	}
	runs := 0
	for _, ran := range engine.stacks() {
		if ran == functionnetwork.StackRef(environment.TierProduction).Name.String() {
			runs++
		}
	}
	if runs != 2 {
		t.Errorf("ran the function network %d times, want it provisioned again: the patch it records is not this deploy's", runs)
	}
}

func repatchingNetwork(t *testing.T) (*Stacks, *mockedEngine, records.Store) {
	t.Helper()
	cfg := routedConfig(t, cloudfront.Kind)
	cfg.Transform = &fixedPass{result: transform.Result{Patches: transform.Patches{"vpc": {"enableNetworkAddressUsageMetrics": true}}}}
	stacks, engine, store := networkStacks(t, cfg)
	engine.outputs = patchedNetworkOutputs(network.DefaultCIDR("ocel", environment.TierProduction), "patched-by-another-project")
	if _, err := stacks.Network().Claim(context.Background(), consumerRef("shop"), progress.Discard()); err != nil {
		t.Fatalf("Claim(shop) = %v", err)
	}
	return stacks, engine, store
}

func TestANetworkBeingTakenDownIsNotProvisionedAgainByAClaimWhosePatchesDiffer(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	stacks, engine, store := repatchingNetwork(t)
	leased, err := records.ReadOrEmpty(ctx, store, network.Shared.LeaseAt(environment.TierProduction))
	if err != nil {
		t.Fatal(err)
	}
	if err := network.Shared.WriteLease(ctx, store, leased, sharedstack.Lease{Destroying: true, DestroyingSince: time.Now()}); err != nil {
		t.Fatal(err)
	}

	_, err = stacks.Network().Claim(ctx, consumerRef("admin"), progress.Discard())
	if code, refused := provider.RefusedCode(err); !refused || code != refusal.CodeBusy {
		t.Fatalf("Claim(admin) = %v, want it refused as busy: another deploy is taking the network down", err)
	}
	if ups, _ := engine.networkRuns(); ups != 1 {
		t.Errorf("the network ran %d times, want only its raise: a claim the lease refuses provisions nothing under the takedown", ups)
	}
}

func TestAPortForwardClaimsTheNetworkWithoutProvisioningItAgainForThisProjectsPatches(t *testing.T) {
	t.Parallel()

	stacks, engine, _ := repatchingNetwork(t)
	if _, err := stacks.Network().ClaimWithoutRepatch(context.Background(), consumerRef("ocel-bastion"), progress.Discard()); err != nil {
		t.Fatalf("ClaimWithoutRepatch() = %v", err)
	}
	if ups, _ := engine.networkRuns(); ups != 1 {
		t.Errorf("the network ran %d times, want only its raise: a port forward has no plan that consents to provisioning it again", ups)
	}
}

func TestThePlanOfTheFirstContainerAppShowsTheNetworkProvisionedAgainWhenItsTransformsPatchItOtherwise(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	cfg, spec := containerPartSpec(t, "v1.0.0")
	cfg.Transform = &fixedPass{result: transform.Result{Patches: transform.Patches{"vpc": {"enableNetworkAddressUsageMetrics": true}}}}
	cfg.Network = networkRegion{}
	cfg.Quotas = roomyQuotas{}
	stacks := stacksWith(cfg, &mockedEngine{outputs: containerPartOutputs()})
	if _, err := stacks.Network().ClaimWithoutRepatch(ctx, consumerRef("blog"), progress.Discard()); err != nil {
		t.Fatalf("ClaimWithoutRepatch(blog) = %v", err)
	}

	plan, err := stacks.Plan(ctx, spec, progress.Discard())
	if err != nil {
		t.Fatalf("Plan() = %v", err)
	}
	if !slices.ContainsFunc(plan.Groups, func(group provider.ChangeGroup) bool {
		return group.Part == network.Part && group.Action == provider.ActionUpdate
	}) {
		t.Errorf("plan groups = %+v, want the network provisioned again: raising the container infrastructure claims it with this project's patches", plan.Groups)
	}

	if _, err := stacks.Provision(ctx, spec, progress.Discard()); err != nil {
		t.Fatalf("Provision() = %v", err)
	}
	plan, err = stacks.Plan(ctx, spec, progress.Discard())
	if err != nil {
		t.Fatalf("Plan() once the container infrastructure stands = %v", err)
	}
	if !slices.ContainsFunc(plan.Groups, func(group provider.ChangeGroup) bool {
		return group.Part == network.Part && group.Action == provider.ActionUpdate
	}) {
		t.Errorf("plan groups = %+v, want the network provisioned again: a container deploy claims it with this project's patches", plan.Groups)
	}
}
