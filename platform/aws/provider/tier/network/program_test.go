package network

import (
	"encoding/json"
	"regexp"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/pulumi/pulumi/sdk/v3/go/auto"
	"github.com/pulumi/pulumi/sdk/v3/go/common/resource"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/platform/aws/provider/component"
	"github.com/ocelhq/ocel/platform/aws/provider/component/componenttest"
)

const runCIDR = "10.20.0.0/16"

type recorded struct {
	mu        sync.Mutex
	resources map[string]resource.PropertyMap
}

func (r *recorded) NewResource(args pulumi.MockResourceArgs) (string, resource.PropertyMap, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.resources == nil {
		r.resources = map[string]resource.PropertyMap{}
	}
	r.resources[args.TypeToken+"::"+args.Name] = args.Inputs
	return args.Name + "-id", componenttest.Autonamed(args), nil
}

func (r *recorded) Call(pulumi.MockCallArgs) (resource.PropertyMap, error) {
	return resource.PropertyMap{}, nil
}

func (r *recorded) of(token string) map[string]resource.PropertyMap {
	r.mu.Lock()
	defer r.mu.Unlock()
	found := map[string]resource.PropertyMap{}
	for key, inputs := range r.resources {
		if name, ok := strings.CutPrefix(key, token+"::"); ok {
			found[name] = inputs
		}
	}
	return found
}

func runNetwork(t *testing.T, namespace string, tier environment.Tier) *recorded {
	t.Helper()
	layout, err := NewLayout(runCIDR)
	if err != nil {
		t.Fatal(err)
	}
	program := NewProgram(namespace, tier, "us-east-1", layout, []string{"use1-az1", "use1-az2"}, component.Patches{})
	cloud := &recorded{}
	if err := pulumi.RunErr(program.Run, pulumi.WithMocks(Slug, string(tier)+"--infra", cloud)); err != nil {
		t.Fatalf("run the network program: %v", err)
	}
	return cloud
}

func tagOf(inputs resource.PropertyMap, key string) string {
	tags, ok := inputs["tags"]
	if !ok || !tags.IsObject() {
		return ""
	}
	value, ok := tags.ObjectValue()[resource.PropertyKey(key)]
	if !ok || !value.IsString() {
		return ""
	}
	return value.StringValue()
}

func TestTheNetworkIsOneVPCOfTheTiersBlockWithAPublicAPrivateAndAnIsolatedSubnetInEachOfTwoZones(t *testing.T) {
	t.Parallel()

	cloud := runNetwork(t, "ocel", environment.TierProduction)

	vpcs := cloud.of("aws:ec2/vpc:Vpc")
	if len(vpcs) != 1 {
		t.Fatalf("declared %d VPCs, want one per tier", len(vpcs))
	}
	for _, vpc := range vpcs {
		if vpc["cidrBlock"].StringValue() != runCIDR || !vpc["enableDnsHostnames"].BoolValue() || !vpc["enableDnsSupport"].BoolValue() {
			t.Errorf("VPC = %v, want %s with DNS names on: RDS and ElastiCache endpoints resolve through them", vpc, runCIDR)
		}
		if tagOf(vpc, "Name") != "ocel-prod-network" || tagOf(vpc, naming.EnvTierTagKey) != "production" || tagOf(vpc, naming.ManagedByTagKey) != "ocel" {
			t.Errorf("VPC tags = %v, want it named and tagged for its namespace and tier", vpc["tags"])
		}
	}

	subnets := cloud.of("aws:ec2/subnet:Subnet")
	byRole := map[string][]string{}
	zones := map[string][]string{}
	for name, subnet := range subnets {
		role := strings.Split(strings.TrimPrefix(name, "ocel-prod-network-"), "-")[0]
		byRole[role] = append(byRole[role], subnet["cidrBlock"].StringValue())
		zones[role] = append(zones[role], subnet["availabilityZoneId"].StringValue())
		if subnet["mapPublicIpOnLaunch"].BoolValue() {
			t.Errorf("subnet %s hands out a public address to whatever launches in it; a task asks for one itself", name)
		}
	}
	want := map[string][]string{
		"public":   {"10.20.0.0/19", "10.20.32.0/19"},
		"private":  {"10.20.64.0/19", "10.20.96.0/19"},
		"isolated": {"10.20.128.0/19", "10.20.160.0/19"},
	}
	for role, blocks := range want {
		got := slices.Sorted(slices.Values(byRole[role]))
		if !slices.Equal(got, blocks) {
			t.Errorf("%s subnets = %v, want %v", role, got, blocks)
		}
		if placed := slices.Sorted(slices.Values(zones[role])); !slices.Equal(placed, []string{"use1-az1", "use1-az2"}) {
			t.Errorf("%s subnets sit in %v, want one in each zone by ID", role, placed)
		}
	}
}

func TestOnlyThePublicSubnetsRouteToTheInternetAndTheGatewayEndpointsServeThePublicAndPrivateOnes(t *testing.T) {
	t.Parallel()

	cloud := runNetwork(t, "ocel", environment.TierPreview)

	routes := cloud.of("aws:ec2/route:Route")
	if len(routes) != 1 {
		t.Fatalf("declared routes %v, want only the public table's default route: the private tables get theirs from the function network's NAT", routes)
	}
	for _, route := range routes {
		if route["destinationCidrBlock"].StringValue() != anyIPv4 || route["routeTableId"].StringValue() != "ocel-preview-network-public-id" || route["gatewayId"].StringValue() != "ocel-preview-network/internet-gateway-id" {
			t.Errorf("route = %v, want the public table's default route through the internet gateway", route)
		}
	}
	tables := cloud.of("aws:ec2/routeTable:RouteTable")
	for name, table := range tables {
		if _, inline := table["routes"]; inline {
			t.Errorf("route table %s declares routes inline, which would strip the NAT route the function network adds", name)
		}
	}
	associations := cloud.of("aws:ec2/routeTableAssociation:RouteTableAssociation")
	for name, association := range associations {
		table := association["routeTableId"].StringValue()
		switch {
		case strings.HasPrefix(name, "ocel-preview-network-isolated-") && table != "ocel-preview-network-isolated-id":
			t.Errorf("isolated subnet %s routes through %s, want the isolated table, which reaches nothing outside the VPC", name, table)
		case strings.HasPrefix(name, "ocel-preview-network-public-") && table != "ocel-preview-network-public-id":
			t.Errorf("public subnet %s routes through %s, want the public table", name, table)
		}
	}

	endpoints := cloud.of("aws:ec2/vpcEndpoint:VpcEndpoint")
	if len(endpoints) != 2 {
		t.Fatalf("declared endpoints %v, want S3 and DynamoDB", endpoints)
	}
	for name, endpoint := range endpoints {
		var served []string
		for _, table := range endpoint["routeTableIds"].ArrayValue() {
			served = append(served, table.StringValue())
		}
		slices.Sort(served)
		if want := []string{"ocel-preview-network-private-1-id", "ocel-preview-network-private-2-id", "ocel-preview-network-public-id"}; !slices.Equal(served, want) {
			t.Errorf("endpoint %s serves %v, want %v", name, served, want)
		}
		if endpoint["vpcEndpointType"].StringValue() != endpointType {
			t.Errorf("endpoint %s is a %s endpoint, want a free gateway endpoint", name, endpoint["vpcEndpointType"].StringValue())
		}
	}
}

func TestTwoNamespacesNameTheirNetworksApart(t *testing.T) {
	t.Parallel()

	names := func(namespace string) []string {
		cloud := runNetwork(t, namespace, environment.TierProduction)
		var found []string
		for _, token := range []string{"aws:ec2/vpc:Vpc", "aws:ec2/securityGroup:SecurityGroup"} {
			for _, inputs := range cloud.of(token) {
				found = append(found, tagOf(inputs, "Name"))
				if name, ok := inputs["name"]; ok {
					found = append(found, name.StringValue())
				}
			}
		}
		return found
	}
	ours, theirs := names("ocel"), names("ocel2")
	for _, name := range ours {
		if slices.Contains(theirs, name) {
			t.Errorf("namespaces ocel and ocel2 both name %q", name)
		}
	}
}

func TestANetworkIsReadBackFromItsOutputAndRefusedWhenItLacksASubnet(t *testing.T) {
	t.Parallel()

	whole := Network{VPC: "vpc-1", CIDR: runCIDR, Zones: []string{"a", "b"}, Public: []string{"p1", "p2"}, Private: []string{"q1", "q2"}, Isolated: []string{"i1", "i2"}, PrivateRoutes: []string{"r1", "r2"}, Clients: "sg-1"}
	encoded, _ := json.Marshal(whole)
	decoded, err := decode(auto.OutputMap{outputKeyNetwork: {Value: string(encoded)}})
	if err != nil || decoded.VPC != "vpc-1" || decoded.Clients != "sg-1" || !slices.Equal(decoded.Isolated, whole.Isolated) {
		t.Errorf("decode() = %+v, %v, want the network back", decoded, err)
	}
	whole.Isolated = whole.Isolated[:1]
	encoded, _ = json.Marshal(whole)
	if _, err := decode(auto.OutputMap{outputKeyNetwork: {Value: string(encoded)}}); err == nil {
		t.Error("decode() = nil, want a network with one isolated subnet refused")
	}
}

func TestTheNetworksSecurityGroupDescribesItselfInTheCharactersAWSAccepts(t *testing.T) {
	t.Parallel()

	accepted := regexp.MustCompile(`^[0-9A-Za-z_ .:/()#,@\[\]+=&;{}!$*-]*$`)
	for _, tier := range []environment.Tier{environment.TierProduction, environment.TierPreview} {
		for name, group := range runNetwork(t, "ocel", tier).of("aws:ec2/securityGroup:SecurityGroup") {
			descriptions := []string{group["description"].StringValue()}
			for _, direction := range []resource.PropertyKey{"ingress", "egress"} {
				if !group[direction].IsArray() {
					continue
				}
				for _, rule := range group[direction].ArrayValue() {
					if description := rule.ObjectValue()["description"]; description.IsString() {
						descriptions = append(descriptions, description.StringValue())
					}
				}
			}
			for _, description := range descriptions {
				if !accepted.MatchString(description) {
					t.Errorf("%s is described %q, which AWS refuses: a security group and its rules take only %s", name, description, accepted)
				}
			}
		}
	}
}

func TestTheNetworksChangeGroupListsEveryResourceItsRaiseCreates(t *testing.T) {
	t.Parallel()

	for _, tier := range []environment.Tier{environment.TierProduction, environment.TierPreview} {
		cloud := runNetwork(t, "acme", tier)
		var created []string
		cloud.mu.Lock()
		for key := range cloud.resources {
			created = append(created, key)
		}
		cloud.mu.Unlock()
		var planned []string
		for _, change := range newChangeGroup("acme", tier).Changes {
			if change.Action != provider.ActionCreate {
				t.Errorf("the %s network's group plans %s %s as %s, want it created", tier, change.Kind, change.Name, change.Action)
			}
			planned = append(planned, change.Kind+"::"+change.Name)
		}
		slices.Sort(created)
		slices.Sort(planned)
		if !slices.Equal(planned, created) {
			t.Errorf("the %s network's group plans\n %v\nand its raise creates\n %v\nwant the plan to be the diff the raise runs", tier, planned, created)
		}
	}
}
