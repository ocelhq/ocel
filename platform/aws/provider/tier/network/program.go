package network

import (
	"encoding/json"
	"fmt"
	"strconv"

	"github.com/ocelhq/ocel/platform/aws/provider/tier/sharedparts"

	"github.com/pulumi/pulumi-aws/sdk/v7/go/aws/ec2"
	"github.com/pulumi/pulumi/sdk/v3/go/auto"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/provider"
	ocelpulumi "github.com/ocelhq/ocel/pkg/provider/pulumi"
	"github.com/ocelhq/ocel/platform/aws/provider/component"
)

const (
	outputKeyNetwork = "network"

	anyIPv4    = "0.0.0.0/0"
	deleteWait = "40m"

	publicWord   = "public"
	privateWord  = "private"
	isolatedWord = "isolated"
	clientsWord  = "clients"

	gatewayPart     = "internet-gateway"
	internetPart    = "internet"
	associationPart = "routes"
)

type work struct {
	namespace string
	tier      environment.Tier
	region    string
	layout    Layout
	zones     []string
	tags      map[string]string
	patches   component.Patches
}

func NewProgram(namespace string, tier environment.Tier, region string, layout Layout, zones []string, patches component.Patches) component.Program {
	w := &work{namespace: namespace, tier: tier, region: region, layout: layout, zones: zones, tags: stackTags(namespace, tier), patches: patches}
	return component.Program{Run: w.run, Tags: w.tags}
}

func (w *work) tagsFor(name string) pulumi.StringMap {
	tags := pulumi.StringMap{}
	for key, value := range w.patches.TagsOver(w.tags) {
		tags[key] = pulumi.String(value)
	}
	tags["Name"] = pulumi.String(name)
	return tags
}

func (w *work) name(words ...string) string {
	return nameFor(w.namespace, w.tier, words...)
}

func (w *work) run(ctx *pulumi.Context) error {
	if len(w.zones) != sharedparts.NetworkZones {
		return fmt.Errorf("the network spreads across %d zones, and this run names %d", sharedparts.NetworkZones, len(w.zones))
	}
	if err := w.patches.Apply(ctx); err != nil {
		return err
	}
	slowDelete := pulumi.Timeouts(&pulumi.CustomTimeouts{Delete: deleteWait})
	vpc, err := ec2.NewVpc(ctx, w.name(), &ec2.VpcArgs{
		CidrBlock:          pulumi.String(w.layout.CIDR),
		EnableDnsHostnames: pulumi.Bool(true),
		EnableDnsSupport:   pulumi.Bool(true),
		Tags:               w.tagsFor(w.name()),
	})
	if err != nil {
		return err
	}
	gateway, err := ec2.NewInternetGateway(ctx, component.NameOf(w.name()).Part(gatewayPart), &ec2.InternetGatewayArgs{
		VpcId: vpc.ID(),
		Tags:  w.tagsFor(w.name()),
	})
	if err != nil {
		return err
	}
	publicName := w.name(publicWord)
	public, err := ec2.NewRouteTable(ctx, publicName, &ec2.RouteTableArgs{
		VpcId: vpc.ID(),
		Tags:  w.tagsFor(publicName),
	})
	if err != nil {
		return err
	}
	if _, err := ec2.NewRoute(ctx, component.NameOf(publicName).Part(internetPart), &ec2.RouteArgs{
		RouteTableId:         public.ID(),
		DestinationCidrBlock: pulumi.String(anyIPv4),
		GatewayId:            gateway.ID(),
	}); err != nil {
		return err
	}
	isolatedName := w.name(isolatedWord)
	isolated, err := ec2.NewRouteTable(ctx, isolatedName, &ec2.RouteTableArgs{
		VpcId: vpc.ID(),
		Tags:  w.tagsFor(isolatedName),
	})
	if err != nil {
		return err
	}

	var publicIDs, privateIDs, isolatedIDs, privateRoutes pulumi.StringArray
	for i, zone := range w.zones {
		n := strconv.Itoa(i + 1)
		subnet, err := w.subnet(ctx, vpc, publicWord, n, zone, w.layout.Public[i], public, slowDelete)
		if err != nil {
			return err
		}
		publicIDs = append(publicIDs, subnet.ID())

		routesName := w.name(privateWord, n)
		routes, err := ec2.NewRouteTable(ctx, routesName, &ec2.RouteTableArgs{
			VpcId: vpc.ID(),
			Tags:  w.tagsFor(routesName),
		})
		if err != nil {
			return err
		}
		privateRoutes = append(privateRoutes, routes.ID())
		if subnet, err = w.subnet(ctx, vpc, privateWord, n, zone, w.layout.Private[i], routes, slowDelete); err != nil {
			return err
		}
		privateIDs = append(privateIDs, subnet.ID())

		if subnet, err = w.subnet(ctx, vpc, isolatedWord, n, zone, w.layout.Isolated[i], isolated, slowDelete); err != nil {
			return err
		}
		isolatedIDs = append(isolatedIDs, subnet.ID())
	}

	reached := append(pulumi.StringArray{public.ID()}, privateRoutes...)
	for _, service := range sharedparts.NetworkEndpointServices {
		endpoint := w.name(service)
		if _, err := ec2.NewVpcEndpoint(ctx, endpoint, &ec2.VpcEndpointArgs{
			VpcId:           vpc.ID(),
			ServiceName:     pulumi.String("com.amazonaws." + w.region + "." + service),
			VpcEndpointType: pulumi.String(sharedparts.NetworkEndpointType),
			RouteTableIds:   reached,
			Tags:            w.tagsFor(endpoint),
		}); err != nil {
			return err
		}
	}

	group := component.SecurityGroupName(w.name(clientsWord))
	clients, err := ec2.NewSecurityGroup(ctx, group, &ec2.SecurityGroupArgs{
		Name:        pulumi.String(group),
		Description: pulumi.String("Ocel " + string(w.tier) + ": what joins it may reach the databases and caches of the tier; it admits nothing itself"),
		VpcId:       vpc.ID(),
		Ingress:     ec2.SecurityGroupIngressArray{},
		Egress: ec2.SecurityGroupEgressArray{&ec2.SecurityGroupEgressArgs{
			Protocol: pulumi.String("-1"), FromPort: pulumi.Int(0), ToPort: pulumi.Int(0),
			CidrBlocks: pulumi.StringArray{pulumi.String(anyIPv4)},
		}},
		Tags: w.tagsFor(group),
	}, slowDelete)
	if err != nil {
		return err
	}

	zones := w.zones
	encoded := pulumi.All(vpc.ID(), publicIDs.ToStringArrayOutput(), privateIDs.ToStringArrayOutput(), isolatedIDs.ToStringArrayOutput(), privateRoutes.ToStringArrayOutput(), clients.ID()).
		ApplyT(func(resolved []any) (string, error) {
			network := Network{CIDR: w.layout.CIDR, Zones: zones, PatchDigest: w.patches.Digest}
			network.VPC = string(resolved[0].(pulumi.ID))
			network.Public, _ = resolved[1].([]string)
			network.Private, _ = resolved[2].([]string)
			network.Isolated, _ = resolved[3].([]string)
			network.PrivateRoutes, _ = resolved[4].([]string)
			network.Clients = string(resolved[5].(pulumi.ID))
			blob, err := json.Marshal(network)
			return string(blob), err
		}).(pulumi.StringOutput)
	ctx.Export(outputKeyNetwork, encoded)
	return nil
}

func ResourceNames(namespace string, tier environment.Tier) map[string][]ocelpulumi.ResourceRef {
	name := func(words ...string) string { return nameFor(namespace, tier, words...) }
	names := map[string][]ocelpulumi.ResourceRef{
		"vpc":                {{Token: tokenVPC, Name: name()}},
		"internetGateway":    {{Token: tokenInternetGateway, Name: component.NameOf(name()).Part(gatewayPart)}},
		"publicRouteTable":   {{Token: tokenRouteTable, Name: name(publicWord)}},
		"isolatedRouteTable": {{Token: tokenRouteTable, Name: name(isolatedWord)}},
	}
	for _, service := range sharedparts.NetworkEndpointServices {
		names[service+"Endpoint"] = []ocelpulumi.ResourceRef{{Token: tokenVPCEndpoint, Name: name(service)}}
	}
	for i := range sharedparts.NetworkZones {
		n := strconv.Itoa(i + 1)
		for _, role := range []string{publicWord, privateWord, isolatedWord} {
			names[role+"Subnet"] = append(names[role+"Subnet"], ocelpulumi.ResourceRef{Token: tokenSubnet, Name: name(role, n)})
		}
		names["privateRouteTable"] = append(names["privateRouteTable"], ocelpulumi.ResourceRef{Token: tokenRouteTable, Name: name(privateWord, n)})
	}
	return names
}

func (w *work) subnet(ctx *pulumi.Context, vpc *ec2.Vpc, role, n, zone, cidr string, routes *ec2.RouteTable, opts ...pulumi.ResourceOption) (*ec2.Subnet, error) {
	name := w.name(role, n)
	subnet, err := ec2.NewSubnet(ctx, name, &ec2.SubnetArgs{
		VpcId:               vpc.ID(),
		CidrBlock:           pulumi.String(cidr),
		AvailabilityZoneId:  pulumi.String(zone),
		MapPublicIpOnLaunch: pulumi.Bool(false),
		Tags:                w.tagsFor(name),
	}, opts...)
	if err != nil {
		return nil, err
	}
	if _, err := ec2.NewRouteTableAssociation(ctx, component.NameOf(name).Part(associationPart), &ec2.RouteTableAssociationArgs{
		RouteTableId: routes.ID(),
		SubnetId:     subnet.ID(),
	}); err != nil {
		return nil, err
	}
	return subnet, nil
}

func decode(outputs auto.OutputMap) (Network, error) {
	fields := make(map[string]any, len(outputs))
	for key, value := range outputs {
		fields[key] = value.Value
	}
	encoded, err := component.RequireStringField(fields, sharedparts.NetworkSlug, outputKeyNetwork)
	if err != nil {
		return Network{}, err
	}
	var network Network
	if err := json.Unmarshal([]byte(encoded), &network); err != nil {
		return Network{}, fmt.Errorf("output %q of %s: %w", outputKeyNetwork, sharedparts.NetworkSlug, err)
	}
	if network.VPC == "" || len(network.Public) != sharedparts.NetworkZones || len(network.Private) != sharedparts.NetworkZones || len(network.Isolated) != sharedparts.NetworkZones || network.Clients == "" {
		return Network{}, fmt.Errorf("output %q of %s names no VPC, clients group or %d subnets of each kind", outputKeyNetwork, sharedparts.NetworkSlug, sharedparts.NetworkZones)
	}
	return network, nil
}

const (
	tokenVPC              = "aws:ec2/vpc:Vpc"
	tokenInternetGateway  = "aws:ec2/internetGateway:InternetGateway"
	tokenSubnet           = "aws:ec2/subnet:Subnet"
	tokenRouteTable       = "aws:ec2/routeTable:RouteTable"
	tokenRoute            = "aws:ec2/route:Route"
	tokenRouteAssociation = "aws:ec2/routeTableAssociation:RouteTableAssociation"
	tokenVPCEndpoint      = "aws:ec2/vpcEndpoint:VpcEndpoint"
)

func createdBy(namespace string, tier environment.Tier) []provider.Change {
	name := func(words ...string) string { return nameFor(namespace, tier, words...) }
	created := func(token, name string) provider.Change {
		return provider.Change{Kind: token, Name: name, Action: provider.ActionCreate}
	}
	changes := []provider.Change{
		created(tokenVPC, name()),
		created(tokenInternetGateway, component.NameOf(name()).Part(gatewayPart)),
		created(tokenRouteTable, name(publicWord)),
		created(tokenRoute, component.NameOf(name(publicWord)).Part(internetPart)),
		created(tokenRouteTable, name(isolatedWord)),
		created(component.TokenEC2SecurityGroup, component.SecurityGroupName(name(clientsWord))),
	}
	for i := range sharedparts.NetworkZones {
		n := strconv.Itoa(i + 1)
		for _, role := range []string{publicWord, privateWord, isolatedWord} {
			changes = append(changes,
				created(tokenSubnet, name(role, n)),
				created(tokenRouteAssociation, component.NameOf(name(role, n)).Part(associationPart)))
		}
		changes = append(changes, created(tokenRouteTable, name(privateWord, n)))
	}
	for _, service := range sharedparts.NetworkEndpointServices {
		changes = append(changes, created(tokenVPCEndpoint, name(service)))
	}
	return changes
}
