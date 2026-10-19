package containers

import (
	"context"
	"fmt"
	"slices"
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
	"github.com/ocelhq/ocel/platform/aws/provider/logevents"
	awsports "github.com/ocelhq/ocel/platform/aws/provider/ports"
	"github.com/ocelhq/ocel/platform/aws/provider/tier/sharedstack"
)

const (
	Slug = awsports.ContainersSlug

	Part    = "containers"
	Summary = "the load balancer and cluster every container app in the tier runs behind"

	TaskPort              = 8080
	TargetGroupNamePrefix = "ocel-"

	logRetentionDays = 14
)

var Shared = sharedstack.Stack{
	Slug:        Slug,
	Part:        Part,
	Subject:     "container infrastructure",
	Consumer:    "container app",
	Ref:         StackRef,
	Forget:      forgetFront,
	RefuseInUse: RefuseInUse,
}

func forgetFront(ctx context.Context, store records.Store, tier environment.Tier) error {
	return records.Forget(ctx, store, awsports.ContainerFrontKey(tier))
}

type Infrastructure struct {
	VPC           string
	Subnets       []string
	Listener      string
	OriginHost    string
	VPCOrigin     string
	TaskSecurity  string
	Cluster       string
	ExecutionRole string
	LogGroup      string

	PublicListener string
	PublicHost     string
	PublicRanges   string
}

func (s Infrastructure) Front() awsports.ContainerFront {
	return awsports.ContainerFront{VPCOrigin: s.VPCOrigin, Host: s.OriginHost}
}

type Fronts struct {
	Private bool
	Public  []string
}

func (f Fronts) addInstalled(installed Fronts) Fronts {
	kept := Fronts{Private: f.Private || installed.Private, Public: f.Public}
	if len(kept.Public) == 0 {
		kept.Public = installed.Public
	}
	return kept
}

func (s Infrastructure) installed() Fronts {
	installed := Fronts{Private: s.Listener != ""}
	if s.PublicListener != "" && s.PublicRanges != "" {
		installed.Public = strings.Split(s.PublicRanges, ",")
	}
	return installed
}

func (s Infrastructure) hasFronts(f Fronts) bool {
	if f.Private && s.Listener == "" {
		return false
	}
	return len(f.Public) == 0 || (s.PublicListener != "" && s.PublicRanges == joinRanges(f.Public))
}

func StackRef(tier environment.Tier) provider.StackRef {
	return provider.StackRef{Project: Slug, Tier: tier, Name: naming.InfraStack(string(tier))}
}

func nameFor(tier environment.Tier, parts ...string) string {
	return naming.Join(naming.WordSeparator, append([]string{Slug, string(tier)}, parts...)...)
}

func stackTags(tier environment.Tier) map[string]string {
	return map[string]string{
		"ocel:managed-by":    "ocel",
		"ocel:project":       Slug,
		naming.EnvTierTagKey: string(tier),
		"ocel:stack":         StackRef(tier).Name.String(),
	}
}

func joinRanges(ranges []string) string {
	return strings.Join(slices.Sorted(slices.Values(ranges)), ",")
}

type Stacks struct {
	Open func(context.Context, provider.StackRef) (sharedstack.Owner, error)
	lock sync.Mutex
}

func (s *Stacks) Read(ctx context.Context, tier environment.Tier) (Infrastructure, bool, error) {
	owner, err := s.Open(ctx, StackRef(tier))
	if err != nil {
		return Infrastructure{}, false, err
	}
	_, present, err := stackrecords.Read(ctx, owner.KeyValues, tier, Slug, StackRef(tier).Name)
	if err != nil || !present {
		return Infrastructure{}, false, err
	}
	state, err := Shared.ReadLease(ctx, owner.KeyValues, tier)
	if err != nil || state.IsPartlyDown(time.Now()) {
		return Infrastructure{}, false, err
	}
	outputs, err := owner.Automation.Outputs(ctx, StackRef(tier), nil)
	if err != nil {
		return Infrastructure{}, false, err
	}
	if len(outputs) == 0 {
		return Infrastructure{}, false, nil
	}
	decoded, err := decode(outputs, false)
	return decoded, err == nil, err
}

func (s *Stacks) Ensure(ctx context.Context, ref provider.StackRef, fronts Fronts, progress progress.Log) (Infrastructure, error) {
	s.lock.Lock()
	defer s.lock.Unlock()
	tier := ref.Tier
	infra, present, err := s.Read(ctx, tier)
	if err != nil {
		return Infrastructure{}, err
	}
	owner, err := s.Open(ctx, StackRef(tier))
	if err != nil {
		return Infrastructure{}, err
	}
	if present && infra.hasFronts(fronts) {
		if err := recordFront(ctx, owner.KeyValues, tier, infra); err != nil {
			return Infrastructure{}, err
		}
		return infra, s.claim(ctx, ref)
	}
	raised := fronts
	if present {
		raised = fronts.addInstalled(infra.installed())
	} else if _, err := component.ReadDefaultVPC(ctx, owner.Subnets, owner.Region, "the cluster and load balancer container apps run behind"); err != nil {
		return Infrastructure{}, err
	}
	switch {
	case progress == nil:
	case !present:
		progress.Say("Provisioning the shared container infrastructure for the " + string(tier) + " tier: one load balancer and one cluster every container app in it runs behind")
	case fronts.Private && infra.Listener == "":
		progress.Say("Raising the internal load balancer of the " + string(tier) + " tier: CloudFront reaches a container app routed through it there, by its VPC origin")
	default:
		progress.Say("Raising the public load balancer of the " + string(tier) + " tier: the edge in front of a container app forwards its hostnames there")
	}
	var decoded Infrastructure
	err = Shared.Raise(ctx, owner, tier, ref.Project, func() ([]provider.PartResource, error) {
		var err error
		if decoded, err = provision(ctx, owner, StackRef(tier), raised, progress); err != nil {
			return nil, err
		}
		return listResources(tier, decoded.Front()), nil
	})
	if err != nil {
		return Infrastructure{}, err
	}
	return decoded, s.claim(ctx, ref)
}

func provision(ctx context.Context, owner sharedstack.Owner, ref provider.StackRef, fronts Fronts, progress progress.Log) (Infrastructure, error) {
	tier := ref.Tier
	if err := stackrecords.Write(ctx, owner.KeyValues, tier, Slug, ref.Name, stackrecords.Stack{
		Kind:      provider.StackInfra,
		WrittenBy: owner.WrittenBy,
	}); err != nil {
		return Infrastructure{}, err
	}
	if owner.VPCOrigins != nil {
		if err := reclaimFailedVPCOrigins(ctx, owner.VPCOrigins, tier, progress); err != nil {
			return Infrastructure{}, err
		}
	}
	program := NewProgram(tier, owner.AppBoundaryARN, owner.AppRolePath, fronts)
	outputs, err := owner.Automation.Run(ctx, ref, program, provider.Lease{}, progress)
	if err != nil {
		return Infrastructure{}, fmt.Errorf("provision the shared container infrastructure for the %s tier: %w", tier, err)
	}
	decoded, err := decode(outputs, fronts.Private)
	if err != nil {
		return Infrastructure{}, err
	}
	if err := recordFront(ctx, owner.KeyValues, tier, decoded); err != nil {
		return Infrastructure{}, err
	}
	return decoded, nil
}

func recordFront(ctx context.Context, store records.Store, tier environment.Tier, infra Infrastructure) error {
	if infra.VPCOrigin == "" {
		return forgetFront(ctx, store, tier)
	}
	return awsports.WriteContainerFront(ctx, store, tier, infra.Front())
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

func (s *Stacks) Remove(ctx context.Context, tier environment.Tier, progress progress.Log) error {
	s.lock.Lock()
	defer s.lock.Unlock()
	owner, err := s.Open(ctx, StackRef(tier))
	if err != nil {
		return err
	}
	return Shared.Remove(ctx, owner, tier, progress)
}

func RefuseInUse(tier environment.Tier, users []string) error {
	return refusal.Refuse(refusal.CodeInvalid,
		"%s still run container apps on the shared container infrastructure of the %s tier, and its cluster is not deleted while their services run in it.\n"+
			"Remove the container apps from those projects and deploy them, or destroy their environments: the last one to leave takes it down",
		strings.Join(users, ", "), tier)
}

func ReadPart(ctx context.Context, store records.Store, region string, tier environment.Tier) (provider.BootstrapPart, bool, error) {
	_, present, err := stackrecords.Read(ctx, store, tier, Slug, StackRef(tier).Name)
	if err != nil || !present {
		return provider.BootstrapPart{}, false, err
	}
	users, err := Shared.ListUsers(ctx, store, tier)
	if err != nil {
		return provider.BootstrapPart{}, false, err
	}
	front, _, err := awsports.ReadContainerFront(ctx, store, tier)
	if err != nil {
		return provider.BootstrapPart{}, false, err
	}
	return provider.BootstrapPart{
		Name:      Part,
		Summary:   Summary,
		Current:   true,
		Resources: listResources(tier, front),
		UsedBy:    users,
		Shape:     &provider.PartShape{Vendor: awsports.Vendor, Region: region, Resources: Shape(tier)},

		RetiresWithLastUser: true,
	}, true, nil
}

func listResources(tier environment.Tier, front awsports.ContainerFront) []provider.PartResource {
	resources := []provider.PartResource{
		{Kind: "AWS::ECS::Cluster", ID: nameFor(tier)},
		{Kind: "AWS::ElasticLoadBalancingV2::LoadBalancer", ID: nameFor(tier)},
		{Kind: "AWS::EC2::SecurityGroup", ID: nameFor(tier, "front")},
		{Kind: "AWS::EC2::SecurityGroup", ID: nameFor(tier, "tasks")},
		{Kind: "AWS::Logs::LogGroup", ID: logevents.NameContainerGroup(tier)},
	}
	if front.VPCOrigin != "" {
		resources = append(resources, provider.PartResource{Kind: "AWS::CloudFront::VpcOrigin", ID: front.VPCOrigin})
	}
	return resources
}

func Shape(tier environment.Tier) []pricing.Shaped {
	return []pricing.Shaped{
		{Name: Slug, Type: component.TerraformECSCluster, Properties: map[string]any{}},
		{Name: Slug, Type: component.TerraformLoadBalancer, Properties: map[string]any{"load_balancer_type": "application", "internal": true}},
		{Name: Slug, Type: component.TerraformLogGroup, Properties: map[string]any{"retention_in_days": logRetentionDays, "name": logevents.NameContainerGroup(tier)}},
	}
}
