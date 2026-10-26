package functionnetwork

import (
	"encoding/json"
	"fmt"
	"strconv"

	"github.com/pulumi/pulumi-aws/sdk/v7/go/aws/ec2"
	"github.com/pulumi/pulumi/sdk/v3/go/auto"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"

	"github.com/ocelhq/ocel/pkg/environment"
	ocelpulumi "github.com/ocelhq/ocel/pkg/provider/pulumi"
	"github.com/ocelhq/ocel/platform/aws/provider/component"
	"github.com/ocelhq/ocel/platform/aws/provider/tier/network"
)

type work struct {
	namespace string
	tier      environment.Tier
	network   network.Network
	tags      map[string]string
	patches   component.Patches
}

func NewProgram(namespace string, tier environment.Tier, placed network.Network, patches component.Patches) component.Program {
	w := &work{namespace: namespace, tier: tier, network: placed, tags: stackTags(namespace, tier), patches: patches}
	return component.Program{Run: w.run, Tags: w.tags}
}

const (
	tokenElasticIP  = "aws:ec2/eip:Eip"
	tokenNATGateway = "aws:ec2/natGateway:NatGateway"
)

func ResourceNames(namespace string, tier environment.Tier) map[string][]ocelpulumi.ResourceRef {
	names := map[string][]ocelpulumi.ResourceRef{}
	for n := range natGateways(tier) {
		names["natAddress"] = append(names["natAddress"], ocelpulumi.ResourceRef{Token: tokenElasticIP, Name: component.NameOf(natName(namespace, tier, n)).Part(eipPart)})
		names["natGateway"] = append(names["natGateway"], ocelpulumi.ResourceRef{Token: tokenNATGateway, Name: natName(namespace, tier, n)})
	}
	return names
}

func (w *work) run(ctx *pulumi.Context) error {
	placed := w.network
	if len(placed.Public) < natGateways(w.tier) || len(placed.PrivateRoutes) == 0 {
		return fmt.Errorf("the %s tier's network has %d public subnets and %d private route tables, and its function network needs %d and at least one",
			w.tier, len(placed.Public), len(placed.PrivateRoutes), natGateways(w.tier))
	}
	if err := w.patches.Apply(ctx); err != nil {
		return err
	}
	tier := w.tier
	tagsFor := func(name string) pulumi.StringMap {
		tags := pulumi.StringMap{}
		for key, value := range w.patches.TagsOver(w.tags) {
			tags[key] = pulumi.String(value)
		}
		tags["Name"] = pulumi.String(name)
		return tags
	}

	gateways := make([]*ec2.NatGateway, 0, natGateways(tier))
	for n := range natGateways(tier) {
		nat := natName(w.namespace, tier, n)
		address, err := ec2.NewEip(ctx, component.NameOf(nat).Part(eipPart), &ec2.EipArgs{
			Domain: pulumi.String("vpc"),
			Tags:   tagsFor(nat),
		})
		if err != nil {
			return err
		}
		gateway, err := ec2.NewNatGateway(ctx, nat, &ec2.NatGatewayArgs{
			AllocationId: address.ID(),
			SubnetId:     pulumi.String(placed.Public[n]),
			Tags:         tagsFor(nat),
		})
		if err != nil {
			return err
		}
		gateways = append(gateways, gateway)
	}

	for i, routes := range placed.PrivateRoutes {
		if _, err := ec2.NewRoute(ctx, component.NameOf(nameFor(w.namespace, tier, privateWord, strconv.Itoa(i+1))).Part(natPart), &ec2.RouteArgs{
			RouteTableId:         pulumi.String(routes),
			DestinationCidrBlock: pulumi.String(anyIPv4),
			NatGatewayId:         gateways[min(i, len(gateways)-1)].ID(),
		}); err != nil {
			return err
		}
	}

	group := component.SecurityGroupName(nameFor(w.namespace, tier))
	functions, err := ec2.NewSecurityGroup(ctx, group, &ec2.SecurityGroupArgs{
		Name:        pulumi.String(group),
		Description: pulumi.String("Ocel: the functions in the " + string(tier) + " class that reach a VPC-only resource run as"),
		VpcId:       pulumi.String(placed.VPC),
		Ingress:     ec2.SecurityGroupIngressArray{},
		Egress: ec2.SecurityGroupEgressArray{&ec2.SecurityGroupEgressArgs{
			Protocol: pulumi.String("-1"), FromPort: pulumi.Int(0), ToPort: pulumi.Int(0),
			CidrBlocks:  pulumi.StringArray{pulumi.String(anyIPv4)},
			Description: pulumi.String("Ocel: a function calls out to its resources and the internet; nothing connects to it"),
		}},
		Tags: tagsFor(group),
	}, pulumi.Timeouts(&pulumi.CustomTimeouts{Delete: deleteWait}))
	if err != nil {
		return err
	}

	subnets, err := json.Marshal(placed.Private)
	if err != nil {
		return err
	}
	ctx.Export(outputKeyPrivateSubnets, pulumi.String(subnets))
	ctx.Export(outputKeyFunctionSecurity, functions.ID().ToStringOutput())
	ctx.Export(outputKeyClients, pulumi.String(placed.Clients))
	ctx.Export(outputKeyPatchDigest, pulumi.String(w.patches.Digest))
	return nil
}

func decode(outputs auto.OutputMap) (Network, error) {
	fields := make(map[string]any, len(outputs))
	for key, value := range outputs {
		fields[key] = value.Value
	}
	var decoded Network
	var err error
	if decoded.SecurityGroup, err = component.RequireStringField(fields, Slug, outputKeyFunctionSecurity); err != nil {
		return Network{}, err
	}
	if decoded.Clients, err = component.RequireStringField(fields, Slug, outputKeyClients); err != nil {
		return Network{}, err
	}
	decoded.patchDigest, _ = fields[outputKeyPatchDigest].(string)
	encoded, err := component.RequireStringField(fields, Slug, outputKeyPrivateSubnets)
	if err != nil {
		return Network{}, err
	}
	if err := json.Unmarshal([]byte(encoded), &decoded.Subnets); err != nil || len(decoded.Subnets) == 0 {
		return Network{}, fmt.Errorf("output %q for %s names no subnets to attach a function to", outputKeyPrivateSubnets, Slug)
	}
	return decoded, nil
}
