package functionnetwork

import (
	"context"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/pulumi/pulumi-aws/sdk/v7/go/aws/lambda"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/pricing"
	"github.com/ocelhq/ocel/pkg/progress"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/records"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/pkg/stackrecords"
	"github.com/ocelhq/ocel/platform/aws/provider/component"
	awsports "github.com/ocelhq/ocel/platform/aws/provider/ports"
	"github.com/ocelhq/ocel/platform/aws/provider/tier/sharedstack"
)

const (
	Slug = "ocel-function-network"

	Part    = "function-network"
	Summary = "two private subnets of the default VPC, a NAT gateway and elastic IP for each (one shared in preview), and S3 and DynamoDB gateway endpoints, " +
		"which every serverless app in the tier that reaches a postgres or kv Ocel provisions runs its functions in"

	outputKeyPrivateSubnets   = "privateSubnets"
	outputKeyFunctionSecurity = "functionSecurityGroup"

	zones       = 2
	deleteWait  = "40m"
	anyIPv4     = "0.0.0.0/0"
	stackTagKey = "ocel:stack"

	functionsWord = "functions"
	privateWord   = "private"
	eipPart       = "eip"
	natPart       = "nat"
	endpointType  = "Gateway"
)

var endpointServices = []string{"s3", "dynamodb"}

var Shared = sharedstack.Stack{
	Slug:        Slug,
	Part:        Part,
	Subject:     "function network",
	Consumer:    "serverless app that reaches a VPC-only resource",
	Ref:         StackRef,
	RefuseInUse: RefuseInUse,
}

type Network struct {
	Subnets       []string
	SecurityGroup string
	Clients       string
}

var Pending = Network{
	Subnets:       []string{"subnet-pending-a", "subnet-pending-b"},
	SecurityGroup: "sg-pending",
}

func (n *Network) VPCConfig() lambda.FunctionVpcConfigPtrInput {
	if n == nil {
		return nil
	}
	return &lambda.FunctionVpcConfigArgs{
		SubnetIds:        pulumi.ToStringArray(n.Subnets),
		SecurityGroupIds: n.securityGroups(),
	}
}

func (n *Network) securityGroups() pulumi.StringArray {
	groups := pulumi.StringArray{pulumi.String(n.SecurityGroup)}
	if n.Clients != "" {
		groups = append(groups, pulumi.String(n.Clients))
	}
	return groups
}

func IsNeededFor(typ provider.BindingType, source string) bool {
	return source == "" && (typ == provider.BindingPostgres || typ == provider.BindingKV)
}

func IsNeededForAny(grants []provider.Binding) bool {
	return slices.ContainsFunc(grants, func(grant provider.Binding) bool {
		return IsNeededFor(grant.Type, grant.Source)
	})
}

func StackRef(tier environment.Tier) provider.StackRef {
	return provider.StackRef{Project: Slug, Tier: tier, Name: naming.InfraStack(string(tier))}
}

func nameFor(namespace string, tier environment.Tier, parts ...string) string {
	return naming.OcelName(namespace, tier, append([]string{functionsWord}, parts...)...)
}

func stackTags(namespace string, tier environment.Tier) map[string]string {
	tags := naming.OcelTags(namespace, tier)
	tags["ocel:project"] = Slug
	tags[stackTagKey] = StackRef(tier).Name.String()
	return tags
}

type Stacks struct {
	Open func(context.Context, provider.StackRef) (sharedstack.Owner, error)
	lock sync.Mutex
}

func (s *Stacks) Read(ctx context.Context, tier environment.Tier) (Network, bool, error) {
	owner, err := s.Open(ctx, StackRef(tier))
	if err != nil {
		return Network{}, false, err
	}
	_, present, err := stackrecords.Read(ctx, owner.KeyValues, tier, Slug, StackRef(tier).Name)
	if err != nil || !present {
		return Network{}, false, err
	}
	state, err := Shared.ReadLease(ctx, owner.KeyValues, tier)
	if err != nil || state.IsPartlyDown(time.Now()) {
		return Network{}, false, err
	}
	outputs, err := owner.Automation.Outputs(ctx, StackRef(tier), nil)
	if err != nil {
		return Network{}, false, err
	}
	if len(outputs) == 0 {
		return Network{}, false, nil
	}
	decoded, err := decode(outputs)
	return decoded, err == nil, err
}

func (s *Stacks) Claim(ctx context.Context, ref provider.StackRef, progress progress.Log) (Network, error) {
	s.lock.Lock()
	defer s.lock.Unlock()
	tier := ref.Tier
	network, present, err := s.Read(ctx, tier)
	if err != nil {
		return Network{}, err
	}
	if present {
		return network, s.claim(ctx, ref)
	}
	owner, err := s.Open(ctx, StackRef(tier))
	if err != nil {
		return Network{}, err
	}
	if _, err := component.ReadDefaultVPC(ctx, owner.Subnets, owner.Region, "the function network serverless apps reach their databases and caches through"); err != nil {
		return Network{}, err
	}
	if progress != nil {
		progress.Say("Provisioning the shared function network for the " + string(tier) + " tier: private subnets, a NAT gateway and gateway endpoints every serverless app that reaches a VPC-only resource attaches to; the first attach can take several minutes")
	}
	var decoded Network
	err = Shared.Raise(ctx, owner, tier, ref.Project, func() ([]provider.PartResource, error) {
		var err error
		if decoded, err = provision(ctx, owner, StackRef(tier), NewProgram(owner.Namespace, tier, owner.Region, owner.Subnets), progress); err != nil {
			return nil, err
		}
		return listResources(owner.Namespace, tier, decoded), nil
	})
	if err != nil {
		return Network{}, err
	}
	return decoded, s.claim(ctx, ref)
}

func (s *Stacks) RecordConsumer(ctx context.Context, ref provider.StackRef) error {
	s.lock.Lock()
	defer s.lock.Unlock()
	return s.claim(ctx, ref)
}

func (s *Stacks) claim(ctx context.Context, ref provider.StackRef) error {
	owner, err := s.Open(ctx, StackRef(ref.Tier))
	if err != nil {
		return err
	}
	return Shared.Claim(ctx, owner, ref)
}

func (s *Stacks) Release(ctx context.Context, store records.Store, ref provider.StackRef, progress progress.Log) error {
	if store == nil {
		return nil
	}
	s.lock.Lock()
	defer s.lock.Unlock()
	return Shared.Release(ctx, s.Open, store, ref, progress)
}

func (s *Stacks) ForgetConsumer(ctx context.Context, store records.Store, ref provider.StackRef) error {
	if store == nil {
		return nil
	}
	s.lock.Lock()
	defer s.lock.Unlock()
	return Shared.ForgetConsumer(ctx, store, ref)
}

func (s *Stacks) Remove(ctx context.Context, tier environment.Tier, progress progress.Log) error {
	s.lock.Lock()
	defer s.lock.Unlock()
	owner, err := s.Open(ctx, StackRef(tier))
	if err != nil {
		return err
	}
	return Shared.Remove(ctx, owner, tier, progress)
}

func provision(ctx context.Context, owner sharedstack.Owner, ref provider.StackRef, program component.Program, progress progress.Log) (Network, error) {
	tier := ref.Tier
	if err := stackrecords.Write(ctx, owner.KeyValues, tier, Slug, ref.Name, stackrecords.Stack{
		Kind:      provider.StackInfra,
		WrittenBy: owner.WrittenBy,
	}); err != nil {
		return Network{}, err
	}
	outputs, err := owner.Automation.Run(ctx, ref, program, provider.Lease{}, progress)
	if err != nil {
		return Network{}, fmt.Errorf("provision the shared function network for the %s tier: %w", tier, err)
	}
	return decode(outputs)
}

func natGateways(tier environment.Tier) int {
	if tier == environment.TierProduction {
		return zones
	}
	return 1
}

func natName(namespace string, tier environment.Tier, n int) string {
	return nameFor(namespace, tier, "nat", strconv.Itoa(n+1))
}

func listResources(namespace string, tier environment.Tier, network Network) []provider.PartResource {
	var resources []provider.PartResource
	for n := range natGateways(tier) {
		resources = append(resources,
			provider.PartResource{Kind: "AWS::EC2::NatGateway", ID: natName(namespace, tier, n)},
			provider.PartResource{Kind: "AWS::EC2::EIP", ID: natName(namespace, tier, n)})
	}
	for i, subnet := range network.Subnets {
		resources = append(resources,
			provider.PartResource{Kind: "AWS::EC2::RouteTable", ID: nameFor(namespace, tier, privateWord, strconv.Itoa(i+1))},
			provider.PartResource{Kind: "AWS::EC2::Subnet", ID: subnet})
	}
	for _, service := range endpointServices {
		resources = append(resources, provider.PartResource{Kind: "AWS::EC2::VPCEndpoint", ID: nameFor(namespace, tier, service)})
	}
	return append(resources, provider.PartResource{Kind: "AWS::EC2::SecurityGroup", ID: network.SecurityGroup})
}

func ReadPart(ctx context.Context, store records.Store, region string, tier environment.Tier) (provider.BootstrapPart, bool, error) {
	recorded, present, err := stackrecords.ReadBootstrapPart(ctx, store, tier, Part)
	if err != nil || !present {
		return provider.BootstrapPart{}, false, err
	}
	users, err := Shared.ListUsers(ctx, store, tier)
	if err != nil {
		return provider.BootstrapPart{}, false, err
	}
	return provider.BootstrapPart{
		Name:      Part,
		Summary:   Summary,
		Current:   recorded.State == stackrecords.PartReady,
		Resources: recorded.Resources,
		UsedBy:    users,
		Shape:     &provider.PartShape{Vendor: awsports.Vendor, Region: region, Resources: Shape(tier)},

		RetiresWithLastUser: true,
	}, true, nil
}

func RefuseInUse(tier environment.Tier, users []string) error {
	return refusal.Refuse(refusal.CodeInvalid,
		"%s still run serverless apps in the function network of the %s tier, and their functions hold network interfaces in its subnets.\n"+
			"Remove the postgres and kv those apps use and deploy them, or destroy their environments: the last one to leave takes it down",
		strings.Join(users, ", "), tier)
}

func Shape(tier environment.Tier) []pricing.Shaped {
	var shaped []pricing.Shaped
	for n := range natGateways(tier) {
		name := Slug + "-" + strconv.Itoa(n+1)
		shaped = append(shaped,
			pricing.Shaped{Name: name, Type: component.TerraformNATGateway, Properties: map[string]any{}},
			pricing.Shaped{Name: name, Type: component.TerraformElasticIP, Properties: map[string]any{}})
	}
	return shaped
}

func NewChangeGroup(tier environment.Tier) provider.ChangeGroup {
	return provider.ChangeGroup{
		Kind:   provider.StackGroupKind,
		Name:   StackRef(tier).Name.String(),
		Action: provider.ActionCreate,
		Part:   Part,
		Reason: "the first serverless app in the " + string(tier) + " tier that reaches a VPC-only resource adds the " + Part + " part: " + Summary,
		Slow:   true,
	}
}
