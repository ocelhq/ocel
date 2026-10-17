package deploy

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsec2 "github.com/aws/aws-sdk-go-v2/service/ec2"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/pulumi/pulumi-aws/sdk/v7/go/aws/ec2"
	"github.com/pulumi/pulumi-aws/sdk/v7/go/aws/lambda"
	"github.com/pulumi/pulumi/sdk/v3/go/auto"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/keyvalue"
	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/progress"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/pkg/stackrecords"
)

const (
	FunctionNetworkSlug = "ocel-function-network"

	FunctionNetworkPart    = "function-network"
	FunctionNetworkSummary = "two private subnets of the default VPC, a NAT gateway and its elastic IP, and S3 and DynamoDB gateway endpoints, " +
		"which every serverless app in the tier that reaches a postgres or kv Ocel provisions runs its functions in"

	outputKeyPrivateSubnets   = "privateSubnets"
	outputKeyFunctionSecurity = "functionSecurityGroup"

	functionNetworkZones        = 2
	functionNetworkDeleteWait   = "40m"
	functionNetworkAnyIPv4      = "0.0.0.0/0"
	functionNetworkStackTagKey  = "ocel:stack"
	functionNetworkEndpointType = "Gateway"
)

var functionNetworkEndpointServices = []string{"s3", "dynamodb"}

var functionNetworkShare = sharedStack{
	slug:        FunctionNetworkSlug,
	part:        FunctionNetworkPart,
	subject:     "function network",
	consumer:    "serverless app that reaches a VPC-only resource",
	ref:         functionNetworkRef,
	lock:        func(r *Stacks) *sync.Mutex { return &r.functionNetworkLock },
	refuseInUse: RefuseFunctionNetworkInUse,
}

type functionNetwork struct {
	Subnets       []string
	SecurityGroup string
}

type functionNetworkWork struct {
	tier    environment.Tier
	region  string
	tags    map[string]string
	subnets awsec2.DescribeSubnetsAPIClient
	outputs auto.OutputMap
}

func (n *functionNetwork) vpcConfig() lambda.FunctionVpcConfigPtrInput {
	if n == nil {
		return nil
	}
	return &lambda.FunctionVpcConfigArgs{
		SubnetIds:        pulumi.ToStringArray(n.Subnets),
		SecurityGroupIds: pulumi.StringArray{pulumi.String(n.SecurityGroup)},
	}
}

func joinsFunctionNetwork(typ provider.BindingType, source string) bool {
	return source == "" && (typ == provider.BindingPostgres || typ == provider.BindingKV)
}

func needsFunctionNetwork(grants []provider.Binding) bool {
	return slices.ContainsFunc(grants, func(grant provider.Binding) bool {
		return joinsFunctionNetwork(grant.Type, grant.Source)
	})
}

func functionNetworkRef(tier environment.Tier) provider.StackRef {
	return provider.StackRef{Project: FunctionNetworkSlug, Tier: tier, Name: naming.InfraStack(string(tier))}
}

func functionNetworkName(tier environment.Tier, parts ...string) string {
	return naming.Join(naming.WordSeparator, append([]string{FunctionNetworkSlug, string(tier)}, parts...)...)
}

func functionNetworkTags(tier environment.Tier) map[string]string {
	return map[string]string{
		"ocel:managed-by":          "ocel",
		"ocel:project":             FunctionNetworkSlug,
		naming.EnvTierTagKey:       string(tier),
		functionNetworkStackTagKey: functionNetworkRef(tier).Name.String(),
	}
}

func (r *Stacks) readFunctionNetwork(ctx context.Context, tier environment.Tier) (functionNetwork, bool, error) {
	owner, err := r.at(ctx, functionNetworkRef(tier), "")
	if err != nil {
		return functionNetwork{}, false, err
	}
	_, present, err := stackrecords.Read(ctx, owner.cfg.KeyValues, tier, FunctionNetworkSlug, functionNetworkRef(tier).Name)
	if err != nil || !present {
		return functionNetwork{}, false, err
	}
	state, err := functionNetworkShare.readLease(ctx, owner.cfg.KeyValues, tier)
	if err != nil || state.isPartlyDown(time.Now()) {
		return functionNetwork{}, false, err
	}
	outputs, err := owner.automation.Outputs(ctx, functionNetworkRef(tier), nil)
	if err != nil {
		return functionNetwork{}, false, err
	}
	if len(outputs) == 0 {
		return functionNetwork{}, false, nil
	}
	decoded, err := decodeFunctionNetwork(outputs)
	return decoded, err == nil, err
}

func (r *Stacks) claimFunctionNetwork(ctx context.Context, ref provider.StackRef, progress progress.Log) (functionNetwork, error) {
	r.functionNetworkLock.Lock()
	defer r.functionNetworkLock.Unlock()
	tier := ref.Tier
	network, present, err := r.readFunctionNetwork(ctx, tier)
	if err != nil {
		return functionNetwork{}, err
	}
	if present {
		return network, r.claimShared(ctx, functionNetworkShare, ref)
	}
	owner, err := r.at(ctx, functionNetworkRef(tier), "")
	if err != nil {
		return functionNetwork{}, err
	}
	if progress != nil {
		progress.Say("Provisioning the shared function network for the " + string(tier) + " tier: private subnets, a NAT gateway and gateway endpoints every serverless app that reaches a VPC-only resource attaches to; the first attach can take several minutes")
	}
	work := &functionNetworkWork{tier: tier, region: owner.cfg.Region, tags: functionNetworkTags(tier), subnets: owner.cfg.Subnets}
	spec := provider.StackSpec{
		Ref:         functionNetworkRef(tier),
		Kind:        provider.StackInfra,
		Tags:        functionNetworkTags(tier),
		VendorState: work,
	}
	var decoded functionNetwork
	err = raiseShared(ctx, functionNetworkShare, owner, tier, ref.Project, func() ([]provider.PartResource, error) {
		var err error
		if decoded, err = provisionFunctionNetwork(ctx, owner, spec, work, progress); err != nil {
			return nil, err
		}
		return listFunctionNetworkResources(tier, decoded), nil
	})
	if err != nil {
		return functionNetwork{}, err
	}
	return decoded, r.claimShared(ctx, functionNetworkShare, ref)
}

func provisionFunctionNetwork(ctx context.Context, owner *release, spec provider.StackSpec, work *functionNetworkWork, progress progress.Log) (functionNetwork, error) {
	tier := spec.Ref.Tier
	if err := stackrecords.Write(ctx, owner.cfg.KeyValues, tier, FunctionNetworkSlug, spec.Ref.Name, stackrecords.Stack{
		Kind:      provider.StackInfra,
		WrittenBy: owner.cfg.WrittenBy,
	}); err != nil {
		return functionNetwork{}, err
	}
	if _, err := owner.automation.Run(ctx, spec, progress); err != nil {
		return functionNetwork{}, fmt.Errorf("provision the shared function network for the %s tier: %w", tier, err)
	}
	return decodeFunctionNetwork(work.outputs)
}

func listFunctionNetworkResources(tier environment.Tier, network functionNetwork) []provider.PartResource {
	resources := []provider.PartResource{
		{Kind: "AWS::EC2::NatGateway", ID: functionNetworkName(tier, "nat")},
		{Kind: "AWS::EC2::EIP", ID: functionNetworkName(tier, "nat")},
		{Kind: "AWS::EC2::RouteTable", ID: functionNetworkName(tier, "private")},
	}
	for _, subnet := range network.Subnets {
		resources = append(resources, provider.PartResource{Kind: "AWS::EC2::Subnet", ID: subnet})
	}
	for _, service := range functionNetworkEndpointServices {
		resources = append(resources, provider.PartResource{Kind: "AWS::EC2::VPCEndpoint", ID: functionNetworkName(tier, service)})
	}
	return append(resources, provider.PartResource{Kind: "AWS::EC2::SecurityGroup", ID: network.SecurityGroup})
}

func ReadFunctionNetworkPart(ctx context.Context, store keyvalue.Store, region string, tier environment.Tier) (provider.BootstrapPart, bool, error) {
	recorded, present, err := stackrecords.ReadBootstrapPart(ctx, store, tier, FunctionNetworkPart)
	if err != nil || !present {
		return provider.BootstrapPart{}, false, err
	}
	users, err := functionNetworkShare.listUsers(ctx, store, tier)
	if err != nil {
		return provider.BootstrapPart{}, false, err
	}
	return provider.BootstrapPart{
		Name:      FunctionNetworkPart,
		Summary:   FunctionNetworkSummary,
		Current:   recorded.State == stackrecords.PartReady,
		Resources: recorded.Resources,
		UsedBy:    users,
		Shape:     &provider.PartShape{Vendor: vendor, Region: region, Resources: ShapeFunctionNetwork()},

		RetiresWithLastUser: true,
	}, true, nil
}

func (r *Stacks) RemoveFunctionNetwork(ctx context.Context, tier environment.Tier, progress progress.Log) error {
	return r.removeShared(ctx, functionNetworkShare, tier, progress)
}

func RefuseFunctionNetworkInUse(tier environment.Tier, users []string) error {
	return refusal.Refuse(refusal.CodeInvalid,
		"%s still run serverless apps in the function network of the %s tier, and their functions hold network interfaces in its subnets.\n"+
			"Remove the postgres and kv those apps use and deploy them, or destroy their environments: the last one to leave takes it down",
		strings.Join(users, ", "), tier)
}

func (r *Stacks) releaseFunctionNetwork(ctx context.Context, store keyvalue.Store, ref provider.StackRef, progress progress.Log) error {
	return r.releaseShared(ctx, functionNetworkShare, store, ref, progress)
}

type vpcSubnet struct {
	ID      string
	CIDR    string
	Zone    string
	Default bool
	Stack   string
}

type functionSubnets struct {
	PublicID string
	Zones    []string
	Blocks   []string
}

func planFunctionSubnets(primary string, existing []vpcSubnet, own string) (functionSubnets, error) {
	var defaults []vpcSubnet
	var taken []string
	for _, subnet := range existing {
		if subnet.Default {
			defaults = append(defaults, subnet)
		}
		if own == "" || subnet.Stack != own {
			taken = append(taken, subnet.CIDR)
		}
	}
	if len(defaults) == 0 {
		return functionSubnets{}, refusal.Refuse(refusal.CodeInvalid,
			"the default VPC has no default subnet, and the NAT gateway a function network needs must sit in a public one")
	}
	slices.SortFunc(defaults, func(a, b vpcSubnet) int {
		switch {
		case a.Zone < b.Zone:
			return -1
		case a.Zone > b.Zone:
			return 1
		}
		return 0
	})
	zones := make([]string, 0, functionNetworkZones)
	for _, subnet := range defaults {
		if len(zones) < functionNetworkZones && !slices.Contains(zones, subnet.Zone) {
			zones = append(zones, subnet.Zone)
		}
	}
	blocks, err := pickPrivateBlocks(primary, taken, len(zones))
	if err != nil {
		return functionSubnets{}, err
	}
	return functionSubnets{PublicID: defaults[0].ID, Zones: zones, Blocks: blocks}, nil
}

func describeVPCSubnets(ctx context.Context, client awsec2.DescribeSubnetsAPIClient, vpcID string) ([]vpcSubnet, error) {
	var subnets []vpcSubnet
	pages := awsec2.NewDescribeSubnetsPaginator(client, &awsec2.DescribeSubnetsInput{
		Filters: []ec2types.Filter{{Name: aws.String("vpc-id"), Values: []string{vpcID}}},
	})
	for pages.HasMorePages() {
		page, err := pages.NextPage(ctx)
		if err != nil {
			return nil, fmt.Errorf("describe the default VPC's subnets: %w", err)
		}
		for _, subnet := range page.Subnets {
			described := vpcSubnet{
				ID:      aws.ToString(subnet.SubnetId),
				CIDR:    aws.ToString(subnet.CidrBlock),
				Zone:    aws.ToString(subnet.AvailabilityZone),
				Default: aws.ToBool(subnet.DefaultForAz),
			}
			for _, tag := range subnet.Tags {
				if aws.ToString(tag.Key) == functionNetworkStackTagKey {
					described.Stack = aws.ToString(tag.Value)
				}
			}
			subnets = append(subnets, described)
		}
	}
	return subnets, nil
}

func (w *functionNetworkWork) run(ctx *pulumi.Context) error {
	vpc, err := ec2.LookupVpc(ctx, &ec2.LookupVpcArgs{Default: pulumi.BoolRef(true)})
	if err != nil {
		return fmt.Errorf("look up default VPC: %w", err)
	}
	existing, err := describeVPCSubnets(ctx.Context(), w.subnets, vpc.Id)
	if err != nil {
		return err
	}
	placed, err := planFunctionSubnets(vpc.CidrBlock, existing, w.tags[functionNetworkStackTagKey])
	if err != nil {
		return err
	}
	tier := w.tier
	tagsFor := func(name string) pulumi.StringMap {
		tags := pulumi.StringMap{"Name": pulumi.String(name)}
		for key, value := range w.tags {
			tags[key] = pulumi.String(value)
		}
		return tags
	}
	slowDelete := pulumi.Timeouts(&pulumi.CustomTimeouts{Delete: functionNetworkDeleteWait})

	address, err := ec2.NewEip(ctx, naming.ResourceID(naming.KindService, "nat", "eip"), &ec2.EipArgs{
		Domain: pulumi.String("vpc"),
		Tags:   tagsFor(functionNetworkName(tier, "nat")),
	})
	if err != nil {
		return err
	}
	gateway, err := ec2.NewNatGateway(ctx, naming.ResourceID(naming.KindService, "nat"), &ec2.NatGatewayArgs{
		AllocationId: address.ID(),
		SubnetId:     pulumi.String(placed.PublicID),
		Tags:         tagsFor(functionNetworkName(tier, "nat")),
	})
	if err != nil {
		return err
	}
	routes, err := ec2.NewRouteTable(ctx, naming.ResourceID(naming.KindService, "private", "routes"), &ec2.RouteTableArgs{
		VpcId: pulumi.String(vpc.Id),
		Tags:  tagsFor(functionNetworkName(tier, "private")),
	})
	if err != nil {
		return err
	}
	if _, err := ec2.NewRoute(ctx, naming.ResourceID(naming.KindService, "private", "route"), &ec2.RouteArgs{
		RouteTableId:         routes.ID(),
		DestinationCidrBlock: pulumi.String(functionNetworkAnyIPv4),
		NatGatewayId:         gateway.ID(),
	}); err != nil {
		return err
	}

	subnetIDs := make(pulumi.StringArray, 0, len(placed.Zones))
	for i, zone := range placed.Zones {
		subnet, err := ec2.NewSubnet(ctx, naming.ResourceID(naming.KindService, "private", zone, "subnet"), &ec2.SubnetArgs{
			VpcId:               pulumi.String(vpc.Id),
			CidrBlock:           pulumi.String(placed.Blocks[i]),
			AvailabilityZone:    pulumi.String(zone),
			MapPublicIpOnLaunch: pulumi.Bool(false),
			Tags:                tagsFor(functionNetworkName(tier, "private", zone)),
		}, slowDelete)
		if err != nil {
			return err
		}
		if _, err := ec2.NewRouteTableAssociation(ctx, naming.ResourceID(naming.KindService, "private", zone, "association"), &ec2.RouteTableAssociationArgs{
			RouteTableId: routes.ID(),
			SubnetId:     subnet.ID(),
		}); err != nil {
			return err
		}
		subnetIDs = append(subnetIDs, subnet.ID())
	}

	for _, service := range functionNetworkEndpointServices {
		if _, err := ec2.NewVpcEndpoint(ctx, naming.ResourceID(naming.KindService, "endpoint", service), &ec2.VpcEndpointArgs{
			VpcId:           pulumi.String(vpc.Id),
			ServiceName:     pulumi.String("com.amazonaws." + w.region + "." + service),
			VpcEndpointType: pulumi.String(functionNetworkEndpointType),
			RouteTableIds:   pulumi.StringArray{routes.ID()},
			Tags:            tagsFor(functionNetworkName(tier, service)),
		}); err != nil {
			return err
		}
	}

	functions, err := ec2.NewSecurityGroup(ctx, naming.ResourceID(naming.KindService, "functions", "security-group"), &ec2.SecurityGroupArgs{
		Name:        pulumi.String(functionNetworkName(tier, "functions")),
		Description: pulumi.String("Ocel: the functions in the " + string(tier) + " class that reach a VPC-only resource run as"),
		VpcId:       pulumi.String(vpc.Id),
		Ingress:     ec2.SecurityGroupIngressArray{},
		Egress: ec2.SecurityGroupEgressArray{&ec2.SecurityGroupEgressArgs{
			Protocol: pulumi.String("-1"), FromPort: pulumi.Int(0), ToPort: pulumi.Int(0),
			CidrBlocks:  pulumi.StringArray{pulumi.String(functionNetworkAnyIPv4)},
			Description: pulumi.String("Ocel: a function calls out to its resources and the internet; nothing connects to it"),
		}},
		Tags: tagsFor(functionNetworkName(tier, "functions")),
	}, slowDelete)
	if err != nil {
		return err
	}

	encoded := pulumi.All(subnetIDs.ToStringArrayOutput()).ApplyT(func(resolved []any) (string, error) {
		ids, _ := resolved[0].([]string)
		blob, err := json.Marshal(ids)
		return string(blob), err
	}).(pulumi.StringOutput)
	ctx.Export(outputKeyPrivateSubnets, encoded)
	ctx.Export(outputKeyFunctionSecurity, functions.ID().ToStringOutput())
	return nil
}

func decodeFunctionNetwork(outputs auto.OutputMap) (functionNetwork, error) {
	fields := make(map[string]any, len(outputs))
	for key, value := range outputs {
		fields[key] = value.Value
	}
	var network functionNetwork
	var err error
	if network.SecurityGroup, err = requireStringField(fields, FunctionNetworkSlug, outputKeyFunctionSecurity); err != nil {
		return functionNetwork{}, err
	}
	encoded, err := requireStringField(fields, FunctionNetworkSlug, outputKeyPrivateSubnets)
	if err != nil {
		return functionNetwork{}, err
	}
	if err := json.Unmarshal([]byte(encoded), &network.Subnets); err != nil || len(network.Subnets) == 0 {
		return functionNetwork{}, fmt.Errorf("output %q for %s names no subnets to attach a function to", outputKeyPrivateSubnets, FunctionNetworkSlug)
	}
	return network, nil
}

var pendingFunctionNetwork = functionNetwork{
	Subnets:       []string{"subnet-pending-a", "subnet-pending-b"},
	SecurityGroup: "sg-pending",
}

func functionNetworkGroup(tier environment.Tier) provider.ChangeGroup {
	return provider.ChangeGroup{
		Kind:   provider.StackGroupKind,
		Name:   functionNetworkRef(tier).Name.String(),
		Action: provider.ActionCreate,
		Part:   FunctionNetworkPart,
		Reason: "the first serverless app in the " + string(tier) + " tier that reaches a VPC-only resource adds the " + FunctionNetworkPart + " part: " + FunctionNetworkSummary,
		Slow:   true,
	}
}

func (r *Stacks) recordFunctionNetworkConsumer(ctx context.Context, ref provider.StackRef) error {
	r.functionNetworkLock.Lock()
	defer r.functionNetworkLock.Unlock()
	return r.claimShared(ctx, functionNetworkShare, ref)
}

func attachesFunctionNetwork(spec provider.StackSpec, transformed *transformPatches) (functions, workers bool) {
	app := spec.App
	if !needsFunctionNetwork(app.Grants) {
		return false, false
	}
	functions = slices.ContainsFunc(app.Functions, func(fn provider.FunctionSpec) bool { return !transformed.placesInVPC(fn.Name) })
	workers = len(app.Workers) > 0 && spec.Ref.Name.Env != ""
	return functions, workers
}

func (r *release) readFunctionNetworkFor(ctx context.Context, spec provider.StackSpec, transformed *transformPatches) (*functionNetwork, bool, error) {
	functions, workers := attachesFunctionNetwork(spec, transformed)
	if !functions && !workers {
		return nil, false, nil
	}
	network, present, err := r.readFunctionNetwork(ctx, spec.Ref.Tier)
	if err != nil {
		return nil, false, err
	}
	if !present {
		network = pendingFunctionNetwork
	}
	return &network, !present, nil
}

func (r *release) claimFunctionNetworkFor(ctx context.Context, spec provider.StackSpec, transformed *transformPatches, progress progress.Log) (*functionNetwork, bool, error) {
	functions, workers := attachesFunctionNetwork(spec, transformed)
	if !functions && !workers {
		return nil, false, nil
	}
	claimant := spec.Ref
	if !functions {
		claimant = workersRef(spec.Ref)
	}
	network, err := r.claimFunctionNetwork(ctx, claimant, progress)
	if err != nil {
		return nil, false, err
	}
	return &network, functions, nil
}
