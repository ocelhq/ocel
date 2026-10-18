package functionnetwork

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strconv"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsec2 "github.com/aws/aws-sdk-go-v2/service/ec2"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/pulumi/pulumi-aws/sdk/v7/go/aws/ec2"
	"github.com/pulumi/pulumi/sdk/v3/go/auto"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/platform/aws/provider/component"
)

type work struct {
	tier    environment.Tier
	region  string
	tags    map[string]string
	subnets awsec2.DescribeSubnetsAPIClient
}

func NewProgram(tier environment.Tier, region string, subnets awsec2.DescribeSubnetsAPIClient) component.Program {
	w := &work{tier: tier, region: region, tags: stackTags(tier), subnets: subnets}
	return component.Program{Run: w.run, Tags: w.tags}
}

type vpcSubnet struct {
	ID      string
	CIDR    string
	Zone    string
	Default bool
	Stack   string
}

type functionSubnets struct {
	Public []string
	Zones  []string
	Blocks []string
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
	picked := make([]string, 0, zones)
	public := make([]string, 0, zones)
	for _, subnet := range defaults {
		if len(picked) < zones && !slices.Contains(picked, subnet.Zone) {
			picked = append(picked, subnet.Zone)
			public = append(public, subnet.ID)
		}
	}
	blocks, err := pickPrivateBlocks(primary, taken, len(picked))
	if err != nil {
		return functionSubnets{}, err
	}
	return functionSubnets{Public: public, Zones: picked, Blocks: blocks}, nil
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
				if aws.ToString(tag.Key) == stackTagKey {
					described.Stack = aws.ToString(tag.Value)
				}
			}
			subnets = append(subnets, described)
		}
	}
	return subnets, nil
}

func (w *work) run(ctx *pulumi.Context) error {
	vpc, err := ec2.LookupVpc(ctx, &ec2.LookupVpcArgs{Default: pulumi.BoolRef(true)})
	if err != nil {
		return fmt.Errorf("look up default VPC: %w", err)
	}
	existing, err := describeVPCSubnets(ctx.Context(), w.subnets, vpc.Id)
	if err != nil {
		return err
	}
	placed, err := planFunctionSubnets(vpc.CidrBlock, existing, w.tags[stackTagKey])
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
	slowDelete := pulumi.Timeouts(&pulumi.CustomTimeouts{Delete: deleteWait})

	gateways := make([]*ec2.NatGateway, 0, natGateways(tier))
	for n := range min(natGateways(tier), len(placed.Public)) {
		address, err := ec2.NewEip(ctx, naming.ResourceID(naming.KindService, "nat", strconv.Itoa(n+1), "eip"), &ec2.EipArgs{
			Domain: pulumi.String("vpc"),
			Tags:   tagsFor(natName(tier, n)),
		})
		if err != nil {
			return err
		}
		gateway, err := ec2.NewNatGateway(ctx, naming.ResourceID(naming.KindService, "nat", strconv.Itoa(n+1)), &ec2.NatGatewayArgs{
			AllocationId: address.ID(),
			SubnetId:     pulumi.String(placed.Public[n]),
			Tags:         tagsFor(natName(tier, n)),
		})
		if err != nil {
			return err
		}
		gateways = append(gateways, gateway)
	}

	subnetIDs := make(pulumi.StringArray, 0, len(placed.Zones))
	routeTables := make(pulumi.StringArray, 0, len(placed.Zones))
	for i, zone := range placed.Zones {
		routes, err := ec2.NewRouteTable(ctx, naming.ResourceID(naming.KindService, "private", zone, "routes"), &ec2.RouteTableArgs{
			VpcId: pulumi.String(vpc.Id),
			Tags:  tagsFor(nameFor(tier, "private", strconv.Itoa(i+1))),
		})
		if err != nil {
			return err
		}
		if _, err := ec2.NewRoute(ctx, naming.ResourceID(naming.KindService, "private", zone, "route"), &ec2.RouteArgs{
			RouteTableId:         routes.ID(),
			DestinationCidrBlock: pulumi.String(anyIPv4),
			NatGatewayId:         gateways[min(i, len(gateways)-1)].ID(),
		}); err != nil {
			return err
		}
		routeTables = append(routeTables, routes.ID())
		subnet, err := ec2.NewSubnet(ctx, naming.ResourceID(naming.KindService, "private", zone, "subnet"), &ec2.SubnetArgs{
			VpcId:               pulumi.String(vpc.Id),
			CidrBlock:           pulumi.String(placed.Blocks[i]),
			AvailabilityZone:    pulumi.String(zone),
			MapPublicIpOnLaunch: pulumi.Bool(false),
			Tags:                tagsFor(nameFor(tier, "private", zone)),
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

	for _, service := range endpointServices {
		if _, err := ec2.NewVpcEndpoint(ctx, naming.ResourceID(naming.KindService, "endpoint", service), &ec2.VpcEndpointArgs{
			VpcId:           pulumi.String(vpc.Id),
			ServiceName:     pulumi.String("com.amazonaws." + w.region + "." + service),
			VpcEndpointType: pulumi.String(endpointType),
			RouteTableIds:   routeTables,
			Tags:            tagsFor(nameFor(tier, service)),
		}); err != nil {
			return err
		}
	}

	functions, err := ec2.NewSecurityGroup(ctx, naming.ResourceID(naming.KindService, "functions", "security-group"), &ec2.SecurityGroupArgs{
		Name:        pulumi.String(nameFor(tier, "functions")),
		Description: pulumi.String("Ocel: the functions in the " + string(tier) + " class that reach a VPC-only resource run as"),
		VpcId:       pulumi.String(vpc.Id),
		Ingress:     ec2.SecurityGroupIngressArray{},
		Egress: ec2.SecurityGroupEgressArray{&ec2.SecurityGroupEgressArgs{
			Protocol: pulumi.String("-1"), FromPort: pulumi.Int(0), ToPort: pulumi.Int(0),
			CidrBlocks:  pulumi.StringArray{pulumi.String(anyIPv4)},
			Description: pulumi.String("Ocel: a function calls out to its resources and the internet; nothing connects to it"),
		}},
		Tags: tagsFor(nameFor(tier, "functions")),
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

func decode(outputs auto.OutputMap) (Network, error) {
	fields := make(map[string]any, len(outputs))
	for key, value := range outputs {
		fields[key] = value.Value
	}
	var network Network
	var err error
	if network.SecurityGroup, err = component.RequireStringField(fields, Slug, outputKeyFunctionSecurity); err != nil {
		return Network{}, err
	}
	encoded, err := component.RequireStringField(fields, Slug, outputKeyPrivateSubnets)
	if err != nil {
		return Network{}, err
	}
	if err := json.Unmarshal([]byte(encoded), &network.Subnets); err != nil || len(network.Subnets) == 0 {
		return Network{}, fmt.Errorf("output %q for %s names no subnets to attach a function to", outputKeyPrivateSubnets, Slug)
	}
	return network, nil
}
