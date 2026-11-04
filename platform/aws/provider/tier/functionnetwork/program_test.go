package functionnetwork

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/platform/aws/provider/tier/sharedparts"

	"github.com/pulumi/pulumi/sdk/v3/go/auto"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/platform/aws/provider/component"
	"github.com/ocelhq/ocel/platform/aws/provider/tier/network"
)

const testNamespace = "ocel"

var tierNetwork = network.Network{
	VPC:           "vpc-ocel",
	CIDR:          "10.20.0.0/16",
	Zones:         []string{"use1-az1", "use1-az2"},
	Public:        []string{"subnet-public-a", "subnet-public-b"},
	Private:       []string{"subnet-private-a", "subnet-private-b"},
	Isolated:      []string{"subnet-isolated-a", "subnet-isolated-b"},
	PrivateRoutes: []string{"rtb-private-a", "rtb-private-b"},
	Clients:       "sg-clients",
}

func runFunctionNetwork(t *testing.T, namespace string, tier environment.Tier) *inputRecorder {
	t.Helper()
	cloud := &inputRecorder{}
	program := NewProgram(namespace, tier, tierNetwork, component.Patches{})
	if err := pulumi.RunErr(program.Run, pulumi.WithMocks(sharedparts.FunctionNetworkSlug, string(tier)+"--infra", cloud)); err != nil {
		t.Fatalf("run the function network program: %v", err)
	}
	return cloud
}

func TestTheFunctionNetworkCarvesNoSubnetAndAddsNoEndpointOfItsOwn(t *testing.T) {
	t.Parallel()

	cloud := runFunctionNetwork(t, testNamespace, environment.TierProduction)

	for _, token := range []string{"aws:ec2/subnet:Subnet", "aws:ec2/routeTable:RouteTable", "aws:ec2/vpcEndpoint:VpcEndpoint", "aws:ec2/routeTableAssociation:RouteTableAssociation"} {
		if got := cloud.registered(token); len(got) != 0 {
			t.Errorf("declared %s %v, want none: the tier's network holds the subnets, route tables and endpoints", token, got)
		}
	}
}

func TestTheProductionFunctionNetworkRoutesEachZoneThroughANATGatewayOfItsOwn(t *testing.T) {
	t.Parallel()

	cloud := runFunctionNetwork(t, testNamespace, environment.TierProduction)

	gateways := cloud.registered("aws:ec2/natGateway:NatGateway")
	if len(gateways) != 2 {
		t.Fatalf("declared %d NAT gateways, want one per zone: a single gateway takes both private subnets offline with its zone", len(gateways))
	}
	if got := len(cloud.registered("aws:ec2/eip:Eip")); got != 2 {
		t.Errorf("declared %d elastic IPs, want one for each NAT gateway", got)
	}
	var placed []string
	for _, gateway := range gateways {
		placed = append(placed, cloud.inputs("aws:ec2/natGateway:NatGateway", gateway)["subnetId"].StringValue())
	}
	slices.Sort(placed)
	if !slices.Equal(placed, tierNetwork.Public) {
		t.Errorf("NAT gateways sit in %v, want the network's public subnets %v", placed, tierNetwork.Public)
	}
	requireDefaultRoutes(t, cloud)
}

func TestAPreviewFunctionNetworkRoutesBothZonesThroughOneNATGateway(t *testing.T) {
	t.Parallel()

	cloud := runFunctionNetwork(t, testNamespace, environment.TierPreview)

	gateways := cloud.registered("aws:ec2/natGateway:NatGateway")
	if len(gateways) != 1 {
		t.Fatalf("declared %d NAT gateways, want one: a preview trades its zone redundancy for half the gateway bill", len(gateways))
	}
	if nat := cloud.inputs("aws:ec2/natGateway:NatGateway", gateways[0]); nat["subnetId"].StringValue() != tierNetwork.Public[0] {
		t.Errorf("NAT gateway sits in %q, want the first public subnet: only a public subnet reaches the internet gateway", nat["subnetId"].StringValue())
	}
	requireDefaultRoutes(t, cloud)
}

func requireDefaultRoutes(t *testing.T, cloud *inputRecorder) {
	t.Helper()
	routes := cloud.registered("aws:ec2/route:Route")
	if len(routes) != len(tierNetwork.PrivateRoutes) {
		t.Fatalf("declared %d routes, want a default route in each private route table of the network", len(routes))
	}
	var tables []string
	for _, name := range routes {
		route := cloud.inputs("aws:ec2/route:Route", name)
		if route["destinationCidrBlock"].StringValue() != anyIPv4 || route["natGatewayId"].StringValue() == "" {
			t.Errorf("route = %v, want 0.0.0.0/0 through a NAT gateway", route)
		}
		tables = append(tables, route["routeTableId"].StringValue())
	}
	slices.Sort(tables)
	if !slices.Equal(tables, tierNetwork.PrivateRoutes) {
		t.Errorf("routes sit in %v, want the network's private route tables %v", tables, tierNetwork.PrivateRoutes)
	}
}

func TestTheFunctionNetworkSecurityGroupSitsInTheTiersNetworkAdmitsNothingAndAllowsEgress(t *testing.T) {
	t.Parallel()

	cloud := runFunctionNetwork(t, testNamespace, environment.TierProduction)

	groups := cloud.registered("aws:ec2/securityGroup:SecurityGroup")
	if len(groups) != 1 {
		t.Fatalf("declared %d security groups, want one for the functions", len(groups))
	}
	group := cloud.inputs("aws:ec2/securityGroup:SecurityGroup", groups[0])
	if group["vpcId"].StringValue() != tierNetwork.VPC {
		t.Errorf("the security group sits in %q, want the tier's network %s", group["vpcId"].StringValue(), tierNetwork.VPC)
	}
	if ingress := group["ingress"]; ingress.IsArray() && len(ingress.ArrayValue()) > 0 {
		t.Errorf("ingress = %v, want none: nothing connects to a function through its network interface", ingress)
	}
	if len(group["egress"].ArrayValue()) != 1 {
		t.Errorf("egress = %v, want the one rule that lets a function call out", group["egress"])
	}
}

func TestTwoNamespacesInOneAccountNameNoFunctionNetworkResourceOfATierAlike(t *testing.T) {
	t.Parallel()

	namesIn := func(namespace string) map[string]bool {
		cloud := runFunctionNetwork(t, namespace, environment.TierProduction)
		named := map[string]bool{}
		for _, name := range cloud.registered("aws:ec2/securityGroup:SecurityGroup") {
			named[cloud.inputs("aws:ec2/securityGroup:SecurityGroup", name)["name"].StringValue()] = true
		}
		return named
	}
	ours, theirs := namesIn("ocel"), namesIn("ocel2")
	for name := range ours {
		if theirs[name] {
			t.Errorf("security group %q is named alike in namespaces ocel and ocel2", name)
		}
	}
}

func TestEveryFunctionNetworkResourceIsTaggedAsOcelsAndNamedForItsTier(t *testing.T) {
	t.Parallel()

	cloud := runFunctionNetwork(t, testNamespace, environment.TierPreview)

	for _, token := range []string{"aws:ec2/eip:Eip", "aws:ec2/natGateway:NatGateway", "aws:ec2/securityGroup:SecurityGroup"} {
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

func TestTheFunctionNetworkProgramRefusesANetworkWithTooFewPublicSubnetsForItsNATGateways(t *testing.T) {
	t.Parallel()

	short := tierNetwork
	short.Public = short.Public[:1]
	program := NewProgram(testNamespace, environment.TierProduction, short, component.Patches{})
	if err := pulumi.RunErr(program.Run, pulumi.WithMocks(sharedparts.FunctionNetworkSlug, "production--infra", &inputRecorder{})); err == nil {
		t.Fatal("run = nil, want a production function network with one public subnet for two NAT gateways refused")
	}
}

func functionNetworkOutputs() auto.OutputMap {
	subnets, _ := json.Marshal(tierNetwork.Private)
	return auto.OutputMap{
		outputKeyPrivateSubnets:   auto.OutputValue{Value: string(subnets)},
		outputKeyFunctionSecurity: auto.OutputValue{Value: "sg-functions"},
		outputKeyClients:          auto.OutputValue{Value: "sg-clients"},
	}
}

func TestDecodeFunctionNetworkReadsEveryOutputAndRefusesAnEmptyOne(t *testing.T) {
	t.Parallel()

	decoded, err := decode(functionNetworkOutputs())
	if err != nil {
		t.Fatalf("decode() = %v", err)
	}
	if !slices.Equal(decoded.Subnets, tierNetwork.Private) || decoded.SecurityGroup != "sg-functions" || decoded.Clients != "sg-clients" {
		t.Errorf("decode() = %+v", decoded)
	}
	if _, err := decode(auto.OutputMap{}); err == nil {
		t.Error("decode() accepted a stack with no outputs")
	}
}
