package functionnetwork

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsec2 "github.com/aws/aws-sdk-go-v2/service/ec2"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"

	"github.com/pulumi/pulumi/sdk/v3/go/auto"
	"github.com/pulumi/pulumi/sdk/v3/go/common/resource"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/refusal"
)

type networkSubnet struct {
	id      string
	zone    string
	cidr    string
	primary bool
	stack   string
	owner   string
}

type functionNetworkCloud struct {
	*inputRecorder
	vpcCIDR   string
	subnets   []networkSubnet
	invoked   sync.Map
	described atomic.Int32
}

func (c *functionNetworkCloud) DescribeSubnets(_ context.Context, in *awsec2.DescribeSubnetsInput, _ ...func(*awsec2.Options)) (*awsec2.DescribeSubnetsOutput, error) {
	c.described.Add(1)
	if len(in.Filters) != 1 || aws.ToString(in.Filters[0].Name) != "vpc-id" || !slices.Equal(in.Filters[0].Values, []string{"vpc-123"}) {
		return nil, fmt.Errorf("DescribeSubnets filtered by %v, want the default VPC alone", in.Filters)
	}
	out := &awsec2.DescribeSubnetsOutput{}
	for _, subnet := range c.subnets {
		described := ec2types.Subnet{
			SubnetId:         aws.String(subnet.id),
			AvailabilityZone: aws.String(subnet.zone),
			CidrBlock:        aws.String(subnet.cidr),
			DefaultForAz:     aws.Bool(subnet.primary),
		}
		if subnet.stack != "" {
			described.Tags = []ec2types.Tag{{Key: aws.String("ocel:stack"), Value: aws.String(subnet.stack)}}
		}
		if subnet.owner != "" {
			described.Tags = append(described.Tags, ec2types.Tag{Key: aws.String("ocel:managed-by"), Value: aws.String(subnet.owner)})
		}
		out.Subnets = append(out.Subnets, described)
	}
	return out, nil
}

func defaultVPCSubnets() []networkSubnet {
	return []networkSubnet{
		{id: "subnet-default-b", zone: "us-east-1b", cidr: "172.31.16.0/20", primary: true},
		{id: "subnet-default-a", zone: "us-east-1a", cidr: "172.31.0.0/20", primary: true},
		{id: "subnet-default-c", zone: "us-east-1c", cidr: "172.31.32.0/20", primary: true},
	}
}

func (c *functionNetworkCloud) Call(args pulumi.MockCallArgs) (resource.PropertyMap, error) {
	c.invoked.Store(args.Token, true)
	switch args.Token {
	case "aws:ec2/getVpc:getVpc":
		return resource.PropertyMap{
			"id":        resource.NewStringProperty("vpc-123"),
			"cidrBlock": resource.NewStringProperty(c.vpcCIDR),
		}, nil
	case "aws:ec2/getSubnets:getSubnets":
		ids := make([]resource.PropertyValue, 0, len(c.subnets))
		for _, subnet := range c.subnets {
			ids = append(ids, resource.NewStringProperty(subnet.id))
		}
		return resource.PropertyMap{"ids": resource.NewArrayProperty(ids)}, nil
	}
	return c.inputRecorder.Call(args)
}

func runFunctionNetwork(t *testing.T, tier environment.Tier, cloud *functionNetworkCloud) *functionNetworkCloud {
	t.Helper()
	if cloud.inputRecorder == nil {
		cloud.inputRecorder = &inputRecorder{}
	}
	if cloud.vpcCIDR == "" {
		cloud.vpcCIDR = "172.31.0.0/16"
	}
	if cloud.subnets == nil {
		cloud.subnets = defaultVPCSubnets()
	}
	network := &work{namespace: "ocel", tier: tier, region: "us-east-1", tags: stackTags("ocel", tier), subnets: cloud}
	if err := pulumi.RunErr(func(pctx *pulumi.Context) error { return network.run(pctx) }, pulumi.WithMocks("ocel-function-network", "production--infra", cloud)); err != nil {
		t.Fatalf("run the function network program: %v", err)
	}
	return cloud
}

func TestTheFunctionNetworkCarvesTwoPrivateSubnetsInTwoZonesBelowNothingTheVPCUses(t *testing.T) {
	t.Parallel()

	cloud := runFunctionNetwork(t, environment.TierProduction, &functionNetworkCloud{})

	subnets := cloud.registered("aws:ec2/subnet:Subnet")
	if len(subnets) != 2 {
		t.Fatalf("declared %d subnets, want two", len(subnets))
	}
	got := map[string]string{}
	for _, name := range subnets {
		inputs := cloud.inputs("aws:ec2/subnet:Subnet", name)
		if inputs["mapPublicIpOnLaunch"].BoolValue() {
			t.Errorf("subnet %s hands out public addresses, want a private subnet", name)
		}
		got[inputs["availabilityZone"].StringValue()] = inputs["cidrBlock"].StringValue()
	}
	want := map[string]string{"us-east-1a": "172.31.240.0/20", "us-east-1b": "172.31.224.0/20"}
	if len(got) != len(want) || got["us-east-1a"] != want["us-east-1a"] || got["us-east-1b"] != want["us-east-1b"] {
		t.Errorf("subnets = %v, want %v: the first two zones, in the first two free blocks from the top", got, want)
	}
}

func TestTheFunctionNetworkReadsTheVPCsSubnetsInOneRequestHoweverManyThereAre(t *testing.T) {
	t.Parallel()

	cloud := runFunctionNetwork(t, environment.TierProduction, &functionNetworkCloud{})

	if got := cloud.described.Load(); got != 1 {
		t.Errorf("DescribeSubnets ran %d times, want once", got)
	}
	if _, perSubnet := cloud.invoked.Load("aws:ec2/getSubnet:getSubnet"); perSubnet {
		t.Error("the program looked up a subnet at a time, so its requests grow with the subnets in the account")
	}
}

func TestTheFunctionNetworkKeepsItsSubnetsWhenItRunsAgain(t *testing.T) {
	t.Parallel()

	own := stackTags("ocel", environment.TierProduction)["ocel:stack"]
	cloud := runFunctionNetwork(t, environment.TierProduction, &functionNetworkCloud{subnets: append(defaultVPCSubnets(),
		networkSubnet{id: "subnet-own-a", zone: "us-east-1a", cidr: "172.31.240.0/20", stack: own, owner: "ocel"},
		networkSubnet{id: "subnet-own-b", zone: "us-east-1b", cidr: "172.31.224.0/20", stack: own, owner: "ocel"},
	)})

	var got []string
	for _, name := range cloud.registered("aws:ec2/subnet:Subnet") {
		got = append(got, cloud.inputs("aws:ec2/subnet:Subnet", name)["cidrBlock"].StringValue())
	}
	slices.Sort(got)
	if want := []string{"172.31.224.0/20", "172.31.240.0/20"}; !slices.Equal(got, want) {
		t.Errorf("subnets = %v, want %v: its own subnets are not taken space, or every run would move them", got, want)
	}
}

func TestTheFunctionNetworkCarvesAroundTheSubnetsOfAnotherNamespacesNetworkInTheSameVPC(t *testing.T) {
	t.Parallel()

	sameStack := stackTags("ocel", environment.TierProduction)["ocel:stack"]
	cloud := runFunctionNetwork(t, environment.TierProduction, &functionNetworkCloud{subnets: append(defaultVPCSubnets(),
		networkSubnet{id: "subnet-other-a", zone: "us-east-1a", cidr: "172.31.240.0/20", stack: sameStack, owner: "other"},
		networkSubnet{id: "subnet-other-b", zone: "us-east-1b", cidr: "172.31.224.0/20", stack: sameStack, owner: "other"},
	)})

	for _, name := range cloud.registered("aws:ec2/subnet:Subnet") {
		if cidr := cloud.inputs("aws:ec2/subnet:Subnet", name)["cidrBlock"].StringValue(); cidr == "172.31.240.0/20" || cidr == "172.31.224.0/20" {
			t.Errorf("subnet %s takes %s, which namespace other's function network already holds in the same VPC", name, cidr)
		}
	}
}

func TestTheProductionFunctionNetworkRoutesEachZoneThroughANATGatewayOfItsOwn(t *testing.T) {
	t.Parallel()

	cloud := runFunctionNetwork(t, environment.TierProduction, &functionNetworkCloud{})

	gateways := cloud.registered("aws:ec2/natGateway:NatGateway")
	if len(gateways) != 2 {
		t.Fatalf("declared %d NAT gateways, want one per zone: a single gateway takes both subnets offline with its zone", len(gateways))
	}
	if got := len(cloud.registered("aws:ec2/eip:Eip")); got != 2 {
		t.Errorf("declared %d elastic IPs, want one for each NAT gateway", got)
	}
	var placed []string
	for _, gateway := range gateways {
		placed = append(placed, cloud.inputs("aws:ec2/natGateway:NatGateway", gateway)["subnetId"].StringValue())
	}
	slices.Sort(placed)
	if want := []string{"subnet-default-a", "subnet-default-b"}; !slices.Equal(placed, want) {
		t.Errorf("NAT gateways sit in %v, want the default subnet of each zone the private subnets use %v", placed, want)
	}
	requireDefaultRoutes(t, cloud, 2)
}

func TestAPreviewFunctionNetworkRoutesBothZonesThroughOneNATGateway(t *testing.T) {
	t.Parallel()

	cloud := runFunctionNetwork(t, environment.TierPreview, &functionNetworkCloud{})

	gateways := cloud.registered("aws:ec2/natGateway:NatGateway")
	if len(gateways) != 1 {
		t.Fatalf("declared %d NAT gateways, want one: a preview trades its zone redundancy for half the gateway bill", len(gateways))
	}
	if nat := cloud.inputs("aws:ec2/natGateway:NatGateway", gateways[0]); nat["subnetId"].StringValue() != "subnet-default-a" {
		t.Errorf("NAT gateway sits in %q, want the default subnet of the first zone: only a public subnet reaches the internet gateway", nat["subnetId"].StringValue())
	}
	requireDefaultRoutes(t, cloud, 2)
}

func requireDefaultRoutes(t *testing.T, cloud *functionNetworkCloud, subnets int) {
	t.Helper()
	routes := cloud.registered("aws:ec2/route:Route")
	if len(routes) != subnets {
		t.Fatalf("declared %d routes, want a default route for each private subnet", len(routes))
	}
	for _, name := range routes {
		route := cloud.inputs("aws:ec2/route:Route", name)
		if route["destinationCidrBlock"].StringValue() != "0.0.0.0/0" || route["natGatewayId"].StringValue() == "" {
			t.Errorf("route = %v, want 0.0.0.0/0 through a NAT gateway", route)
		}
	}
	if got := len(cloud.registered("aws:ec2/routeTableAssociation:RouteTableAssociation")); got != subnets {
		t.Errorf("declared %d route table associations, want one per private subnet", got)
	}
}

func TestTheFunctionNetworkReachesS3AndDynamoDBThroughFreeGatewayEndpoints(t *testing.T) {
	t.Parallel()

	cloud := runFunctionNetwork(t, environment.TierProduction, &functionNetworkCloud{})

	var services []string
	for _, name := range cloud.registered("aws:ec2/vpcEndpoint:VpcEndpoint") {
		endpoint := cloud.inputs("aws:ec2/vpcEndpoint:VpcEndpoint", name)
		if endpoint["vpcEndpointType"].StringValue() != "Gateway" {
			t.Errorf("endpoint %s is a %q, want a gateway endpoint: an interface endpoint bills by the hour", name, endpoint["vpcEndpointType"].StringValue())
		}
		if len(stringsAt(endpoint, "routeTableIds")) != 2 {
			t.Errorf("endpoint %s is attached to %v, want the route table of each private subnet", name, stringsAt(endpoint, "routeTableIds"))
		}
		services = append(services, endpoint["serviceName"].StringValue())
	}
	slices.Sort(services)
	if want := []string{"com.amazonaws.us-east-1.dynamodb", "com.amazonaws.us-east-1.s3"}; !slices.Equal(services, want) {
		t.Errorf("endpoints = %v, want %v", services, want)
	}
}

func TestTheFunctionNetworkSecurityGroupAdmitsNothingAndAllowsEgress(t *testing.T) {
	t.Parallel()

	cloud := runFunctionNetwork(t, environment.TierProduction, &functionNetworkCloud{})

	groups := cloud.registered("aws:ec2/securityGroup:SecurityGroup")
	if len(groups) != 1 {
		t.Fatalf("declared %d security groups, want one for the functions", len(groups))
	}
	group := cloud.inputs("aws:ec2/securityGroup:SecurityGroup", groups[0])
	if ingress := group["ingress"]; ingress.IsArray() && len(ingress.ArrayValue()) > 0 {
		t.Errorf("ingress = %v, want none: nothing connects to a function through its network interface", ingress)
	}
	if len(group["egress"].ArrayValue()) != 1 {
		t.Errorf("egress = %v, want the one rule that lets a function call out", group["egress"])
	}
}

func TestEveryFunctionNetworkResourceIsTaggedAsOcelsAndNamedForItsTier(t *testing.T) {
	t.Parallel()

	cloud := runFunctionNetwork(t, environment.TierPreview, &functionNetworkCloud{})

	for _, token := range []string{
		"aws:ec2/subnet:Subnet", "aws:ec2/eip:Eip", "aws:ec2/natGateway:NatGateway",
		"aws:ec2/routeTable:RouteTable", "aws:ec2/vpcEndpoint:VpcEndpoint", "aws:ec2/securityGroup:SecurityGroup",
	} {
		for _, name := range cloud.registered(token) {
			tags := cloud.object(t, token, name, "tags")
			if tags["ocel:managed-by"].StringValue() != "ocel" || tags[naming.EnvTierTagKey].StringValue() != "preview" {
				t.Errorf("%s %s is tagged %v, want Ocel's tags and its tier: the deployer may only create and mutate what carries them", token, name, tags)
			}
		}
	}
	group := cloud.inputs("aws:ec2/securityGroup:SecurityGroup", cloud.registered("aws:ec2/securityGroup:SecurityGroup")[0])
	if !strings.Contains(group["name"].StringValue(), "preview") {
		t.Errorf("security group name = %q, want it to name the tier", group["name"].StringValue())
	}
}

func TestTheFunctionNetworkProgramRefusesAVPCWithNoDefaultSubnet(t *testing.T) {
	t.Parallel()

	cloud := &functionNetworkCloud{inputRecorder: &inputRecorder{}, vpcCIDR: "172.31.0.0/16", subnets: []networkSubnet{{id: "subnet-x", zone: "us-east-1a", cidr: "172.31.0.0/20"}}}
	network := &work{namespace: "ocel", tier: environment.TierProduction, region: "us-east-1", tags: stackTags("ocel", environment.TierProduction), subnets: cloud}
	err := pulumi.RunErr(func(pctx *pulumi.Context) error { return network.run(pctx) }, pulumi.WithMocks("ocel-function-network", "production--infra", cloud))
	var refused refusal.Refusal
	if !errors.As(err, &refused) {
		t.Fatalf("run = %v, want a refusal: a NAT gateway lives in a public subnet and the VPC has no default one", err)
	}
}

func functionNetworkOutputs() auto.OutputMap {
	subnets, _ := json.Marshal([]string{"subnet-private-a", "subnet-private-b"})
	return auto.OutputMap{
		outputKeyPrivateSubnets:   auto.OutputValue{Value: string(subnets)},
		outputKeyFunctionSecurity: auto.OutputValue{Value: "sg-functions"},
	}
}

func TestDecodeFunctionNetworkReadsEveryOutputAndRefusesAnEmptyOne(t *testing.T) {
	t.Parallel()

	decoded, err := decode(functionNetworkOutputs())
	if err != nil {
		t.Fatalf("decode() = %v", err)
	}
	if !slices.Equal(decoded.Subnets, []string{"subnet-private-a", "subnet-private-b"}) || decoded.SecurityGroup != "sg-functions" {
		t.Errorf("decode() = %+v", decoded)
	}
	if _, err := decode(auto.OutputMap{}); err == nil {
		t.Error("decode() accepted a stack with no outputs")
	}
}
