package functionnetwork

import (
	"context"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	awsec2 "github.com/aws/aws-sdk-go-v2/service/ec2"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"
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
	"github.com/ocelhq/ocel/platform/aws/provider/tier/network"
	"github.com/ocelhq/ocel/platform/aws/provider/tier/sharedstack"
)

const (
	Slug = "ocel-function-network"

	Part    = "function-network"
	Summary = "a NAT gateway and elastic IP for each zone of the tier's network (one shared in preview) and the routes its private subnets take through them, " +
		"which every serverless app in the tier that reaches a postgres or kv Ocel provisions runs its functions behind"

	outputKeyPrivateSubnets   = "privateSubnets"
	outputKeyFunctionSecurity = "functionSecurityGroup"
	outputKeyClients          = "clientsSecurityGroup"
	outputKeyPatchDigest      = "patchDigest"

	deleteWait  = "40m"
	anyIPv4     = "0.0.0.0/0"
	stackTagKey = "ocel:stack"

	functionsWord = "functions"
	privateWord   = "private"
	eipPart       = "eip"
	natPart       = "nat"
)

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

	patchDigest string
}

var Pending = Network{
	Subnets:       network.Pending.Private,
	SecurityGroup: "sg-pending",
	Clients:       network.Pending.Clients,
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
	return pulumi.StringArray{pulumi.String(n.SecurityGroup), pulumi.String(n.Clients)}
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

func projectTags(namespace string, tier environment.Tier) map[string]string {
	tags := naming.OcelTags(namespace, tier)
	tags["ocel:project"] = Slug
	return tags
}

func stackTags(namespace string, tier environment.Tier) map[string]string {
	tags := projectTags(namespace, tier)
	tags[stackTagKey] = StackRef(tier).Name.String()
	return tags
}

type Stacks struct {
	Open    func(context.Context, provider.StackRef) (sharedstack.Owner, error)
	Network *network.Stacks
	Patches func(context.Context, environment.Tier) (component.Patches, error)
	lock    sync.Mutex
}

func (s *Stacks) patchesFor(ctx context.Context, tier environment.Tier) (component.Patches, error) {
	if s.Patches == nil {
		return component.Patches{}, nil
	}
	return s.Patches(ctx, tier)
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
	patches, err := s.patchesFor(ctx, tier)
	if err != nil {
		return Network{}, err
	}
	owner, err := s.Open(ctx, StackRef(tier))
	if err != nil {
		return Network{}, err
	}
	if present && (patches.IsEmpty() || patches.Digest == network.patchDigest) {
		if _, err := s.Network.Claim(ctx, StackRef(tier), progress); err != nil {
			return Network{}, err
		}
		return network, s.claim(ctx, ref)
	}
	if present {
		if progress != nil {
			progress.Say("Applying the transforms that patch the function network of the " + string(tier) + " tier: they differ from the ones it was provisioned with")
		}
		repatched, err := s.provision(ctx, owner, StackRef(tier), patches, progress)
		if err != nil {
			return Network{}, err
		}
		return repatched, s.claim(ctx, ref)
	}
	if err := refuseFullAddresses(ctx, owner, tier); err != nil {
		return Network{}, err
	}
	if progress != nil {
		progress.Say("Provisioning the shared function network for the " + string(tier) + " tier: NAT gateways the private subnets every serverless app that reaches a VPC-only resource attaches to route through; the first attach can take several minutes")
	}
	var decoded Network
	err = Shared.Raise(ctx, owner, tier, ref.Project, func() ([]provider.PhysicalResource, error) {
		var err error
		if decoded, err = s.provision(ctx, owner, StackRef(tier), patches, progress); err != nil {
			return nil, err
		}
		return listResources(owner.Namespace, tier, decoded), nil
	})
	if err != nil {
		return Network{}, err
	}
	return decoded, s.claim(ctx, ref)
}

func refuseFullAddresses(ctx context.Context, owner sharedstack.Owner, tier environment.Tier) error {
	held, err := owner.EC2.DescribeAddresses(ctx, &awsec2.DescribeAddressesInput{})
	if err != nil {
		return fmt.Errorf("count the elastic IPs this account holds in %s: %w", owner.Region, err)
	}
	others := 0
	for _, address := range held.Addresses {
		if !isAddressOf(address, owner.Namespace, tier) {
			others++
		}
	}
	return component.RefuseFullQuota(ctx, owner.Quotas, component.ElasticIPQuota, owner.Region, others, natGateways(tier),
		"the "+string(tier)+" tier's function network, one elastic IP for each NAT gateway,")
}

func isAddressOf(address ec2types.Address, namespace string, tier environment.Tier) bool {
	return component.HasTags(address.Tags, projectTags(namespace, tier))
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
	retired, err := Shared.Release(ctx, s.Open, store, ref, progress)
	if err != nil || !retired {
		return err
	}
	return s.Network.Release(ctx, store, StackRef(ref.Tier), progress)
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
	if err := Shared.Remove(ctx, owner, tier, progress); err != nil {
		return err
	}
	return s.Network.Release(ctx, owner.KeyValues, StackRef(tier), progress)
}

func (s *Stacks) provision(ctx context.Context, owner sharedstack.Owner, ref provider.StackRef, patches component.Patches, progress progress.Log) (Network, error) {
	tier := ref.Tier
	if err := stackrecords.Write(ctx, owner.KeyValues, tier, Slug, ref.Name, stackrecords.Stack{
		Kind:      provider.StackInfra,
		WrittenBy: owner.WrittenBy,
	}); err != nil {
		return Network{}, err
	}
	placed, err := s.Network.Claim(ctx, ref, progress)
	if err != nil {
		return Network{}, err
	}
	outputs, err := owner.Automation.Run(ctx, ref, NewProgram(owner.Namespace, tier, placed, patches), provider.Lease{}, progress)
	if err != nil {
		return Network{}, fmt.Errorf("provision the shared function network for the %s tier: %w", tier, err)
	}
	return decode(outputs)
}

func natGateways(tier environment.Tier) int {
	if tier == environment.TierProduction {
		return network.Zones
	}
	return 1
}

func natName(namespace string, tier environment.Tier, n int) string {
	return nameFor(namespace, tier, "nat", strconv.Itoa(n+1))
}

func listResources(namespace string, tier environment.Tier, network Network) []provider.PhysicalResource {
	var resources []provider.PhysicalResource
	for n := range natGateways(tier) {
		resources = append(resources,
			provider.PhysicalResource{Kind: "AWS::EC2::NatGateway", ID: natName(namespace, tier, n)},
			provider.PhysicalResource{Kind: "AWS::EC2::EIP", ID: natName(namespace, tier, n)})
	}
	return append(resources, provider.PhysicalResource{Kind: "AWS::EC2::SecurityGroup", ID: network.SecurityGroup})
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
