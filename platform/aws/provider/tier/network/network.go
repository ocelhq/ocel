package network

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

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
	Slug = "ocel-network"

	Part    = "network"
	Summary = "the VPC every container app, VPC-attached function, database, cache and bastion of the tier runs in: a public, a private and an isolated subnet in each of two zones, " +
		"an internet gateway, S3 and DynamoDB gateway endpoints, and the security group whatever reaches a database or cache joins"

	kindVPC             = "AWS::EC2::VPC"
	kindInternetGateway = "AWS::EC2::InternetGateway"
	kindSubnet          = "AWS::EC2::Subnet"
	kindRouteTable      = "AWS::EC2::RouteTable"
	kindVPCEndpoint     = "AWS::EC2::VPCEndpoint"
	kindSecurityGroup   = "AWS::EC2::SecurityGroup"

	networkWord   = "network"
	stackTagKey   = "ocel:stack"
	projectTagKey = "ocel:project"
)

var Shared = sharedstack.Stack{
	Slug:        Slug,
	Part:        Part,
	Subject:     "network",
	Consumer:    "part or database that runs in it",
	Ref:         StackRef,
	RefuseInUse: RefuseInUse,
}

type Network struct {
	VPC           string   `json:"vpc"`
	CIDR          string   `json:"cidr"`
	Zones         []string `json:"zones"`
	Public        []string `json:"public"`
	Private       []string `json:"private"`
	Isolated      []string `json:"isolated"`
	PrivateRoutes []string `json:"privateRoutes"`
	Clients       string   `json:"clients"`
}

var Pending = Network{
	VPC:           "vpc-pending",
	CIDR:          exampleCIDR,
	Zones:         []string{"zone-pending-a", "zone-pending-b"},
	Public:        []string{"subnet-pending-public-a", "subnet-pending-public-b"},
	Private:       []string{"subnet-pending-private-a", "subnet-pending-private-b"},
	Isolated:      []string{"subnet-pending-isolated-a", "subnet-pending-isolated-b"},
	PrivateRoutes: []string{"rtb-pending-private-a", "rtb-pending-private-b"},
	Clients:       "sg-pending-clients",
}

func StackRef(tier environment.Tier) provider.StackRef {
	return provider.StackRef{Project: Slug, Tier: tier, Name: naming.InfraStack(string(tier))}
}

func nameFor(namespace string, tier environment.Tier, words ...string) string {
	return naming.OcelName(namespace, tier, append([]string{networkWord}, words...)...)
}

func stackTags(namespace string, tier environment.Tier) map[string]string {
	tags := naming.OcelTags(namespace, tier)
	tags[projectTagKey] = Slug
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
	return read(ctx, owner, tier)
}

func read(ctx context.Context, owner sharedstack.Owner, tier environment.Tier) (Network, bool, error) {
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

func (s *Stacks) Claim(ctx context.Context, consumer provider.StackRef, progress progress.Log) (Network, error) {
	s.lock.Lock()
	defer s.lock.Unlock()
	tier := consumer.Tier
	owner, err := s.Open(ctx, StackRef(tier))
	if err != nil {
		return Network{}, err
	}
	if owner.NetworkCIDR != "" {
		if _, err := NewLayout(owner.NetworkCIDR); err != nil {
			return Network{}, err
		}
	}
	current, present, err := read(ctx, owner, tier)
	if err != nil {
		return Network{}, err
	}
	if present {
		if err := refuseNewBlock(tier, current, owner.NetworkCIDR); err != nil {
			return Network{}, err
		}
		return current, Shared.Claim(ctx, owner, consumer)
	}
	zones, err := PickZones(ctx, owner.EC2)
	if err != nil {
		return Network{}, err
	}
	layout, err := Place(ctx, owner.EC2, owner.Quotas, Placement{
		Region:     owner.Region,
		AccountID:  owner.AccountID,
		Namespace:  owner.Namespace,
		Tier:       tier,
		Configured: owner.NetworkCIDR,
	})
	if err != nil {
		return Network{}, err
	}
	if progress != nil {
		progress.Say(fmt.Sprintf("Provisioning the network of the %s tier: a VPC of %s across %s, which its container apps, VPC-attached functions, databases and caches run in",
			tier, layout.CIDR, strings.Join(zones, " and ")))
	}
	var raised Network
	err = Shared.Raise(ctx, owner, tier, consumer.Project, func() ([]provider.PhysicalResource, error) {
		var err error
		if raised, err = provision(ctx, owner, StackRef(tier), NewProgram(owner.Namespace, tier, owner.Region, layout, zones), progress); err != nil {
			return nil, err
		}
		return listResources(owner.Namespace, tier, raised), nil
	})
	if err != nil {
		return Network{}, err
	}
	return raised, Shared.Claim(ctx, owner, consumer)
}

func refuseNewBlock(tier environment.Tier, current Network, configured string) error {
	if configured == "" || configured == current.CIDR {
		return nil
	}
	return refusal.Refuse(refusal.CodeInvalid,
		"the %s tier's network was created with %s, and `network.%s.cidr` now asks for %s; a VPC's block cannot change without replacing the VPC and everything in it. "+
			"Set it back to %s, or remove everything in the tier's network and `%s --remove %s` first",
		tier, current.CIDR, tier, configured, current.CIDR, provider.BootstrapCommand(tier), Part)
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
		return Network{}, fmt.Errorf("provision the network of the %s tier: %w", tier, err)
	}
	return decode(outputs)
}

func (s *Stacks) Release(ctx context.Context, store records.Store, consumer provider.StackRef, progress progress.Log) error {
	if store == nil {
		return nil
	}
	s.lock.Lock()
	defer s.lock.Unlock()
	return Shared.Release(ctx, s.Open, store, consumer, progress)
}

func (s *Stacks) ForgetConsumer(ctx context.Context, store records.Store, consumer provider.StackRef) error {
	if store == nil {
		return nil
	}
	s.lock.Lock()
	defer s.lock.Unlock()
	return Shared.ForgetConsumer(ctx, store, consumer)
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

func listResources(namespace string, tier environment.Tier, network Network) []provider.PhysicalResource {
	resources := []provider.PhysicalResource{
		{Kind: kindVPC, ID: network.VPC},
		{Kind: kindInternetGateway, ID: nameFor(namespace, tier)},
		{Kind: kindRouteTable, ID: nameFor(namespace, tier, publicWord)},
		{Kind: kindRouteTable, ID: nameFor(namespace, tier, isolatedWord)},
	}
	for _, subnets := range [][]string{network.Public, network.Private, network.Isolated} {
		for _, subnet := range subnets {
			resources = append(resources, provider.PhysicalResource{Kind: kindSubnet, ID: subnet})
		}
	}
	for _, routes := range network.PrivateRoutes {
		resources = append(resources, provider.PhysicalResource{Kind: kindRouteTable, ID: routes})
	}
	for _, service := range endpointServices {
		resources = append(resources, provider.PhysicalResource{Kind: kindVPCEndpoint, ID: nameFor(namespace, tier, service)})
	}
	return append(resources, provider.PhysicalResource{Kind: kindSecurityGroup, ID: network.Clients})
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
		"%s still run in the network of the %s tier, and a VPC is not deleted while anything is in it.\n"+
			"Remove those parts with `%s --remove`, and destroy the environments whose databases and caches sit in it: the last one to leave takes it down",
		strings.Join(users, ", "), tier, provider.BootstrapCommand(tier))
}

func Shape(tier environment.Tier) []pricing.Shaped {
	name := Slug + "-" + string(tier)
	shaped := []pricing.Shaped{
		{Name: name, Type: component.TerraformVPC, Properties: map[string]any{}},
		{Name: name, Type: component.TerraformInternetGateway, Properties: map[string]any{}},
	}
	for _, role := range []string{publicWord, privateWord, isolatedWord} {
		for n := range Zones {
			shaped = append(shaped, pricing.Shaped{Name: naming.Join(naming.WordSeparator, name, role, fmt.Sprint(n+1)), Type: component.TerraformSubnet, Properties: map[string]any{}})
		}
	}
	for _, service := range endpointServices {
		shaped = append(shaped, pricing.Shaped{Name: naming.Join(naming.WordSeparator, name, service), Type: component.TerraformVPCEndpoint, Properties: map[string]any{"vpc_endpoint_type": endpointType}})
	}
	return shaped
}

func NewChangeGroup(namespace string, tier environment.Tier) provider.ChangeGroup {
	return provider.ChangeGroup{
		Kind:    provider.StackGroupKind,
		Name:    StackRef(tier).Name.String(),
		Action:  provider.ActionCreate,
		Part:    Part,
		Reason:  "the first container app, VPC-attached function, database or cache in the " + string(tier) + " tier adds the " + Part + " part: " + Summary,
		Changes: createdBy(namespace, tier),
	}
}
