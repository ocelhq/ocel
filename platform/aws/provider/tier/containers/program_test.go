package containers

import (
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/pulumi/pulumi/sdk/v3/go/common/resource"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/platform/aws/provider/component/componenttest"
	"github.com/ocelhq/ocel/platform/aws/provider/iam"
)

func TestTheContainerInfraProgramProvisionsOneFrontOneClusterAndOneExecutionRole(t *testing.T) {
	t.Parallel()

	infra := &work{tier: environment.TierProduction, boundary: "arn:aws:iam::123456789012:policy/ocel-app-boundary", tags: stackTags(environment.TierProduction)}
	rec := &inputRecorder{}
	if err := pulumi.RunErr(func(pctx *pulumi.Context) error { return infra.run(pctx) }, pulumi.WithMocks("ocel-containers", "production--infra", rec)); err != nil {
		t.Fatalf("run the container infrastructure program: %v", err)
	}

	listener := recordedOf(t, rec, "aws:lb/listener:Listener")
	action := listener["defaultActions"].ArrayValue()[0].ObjectValue()
	if action["type"].StringValue() != "fixed-response" || action["fixedResponse"].ObjectValue()["statusCode"].StringValue() != listenerDeniedStatus {
		t.Errorf("default action = %v, want a fixed %s: a request that names no release reaches nothing", action, listenerDeniedStatus)
	}
	balancer := recordedOf(t, rec, "aws:lb/loadBalancer:LoadBalancer")
	if balancer["name"].StringValue() != "ocel-containers-production" || !balancer["internal"].BoolValue() {
		t.Errorf("load balancer = %v, want an internal front named for the tier: nothing on the internet has a route to it", balancer)
	}
	vpcOrigin := recordedOf(t, rec, "aws:cloudfront/vpcOrigin:VpcOrigin")
	endpoint := vpcOrigin["vpcOriginEndpointConfig"].ObjectValue()
	if endpoint["arn"].StringValue() == "" || endpoint["name"].StringValue() != "ocel-containers-production" {
		t.Errorf("vpc origin = %v, want it to name the tier front by its ARN: the edge reaches the front through it and nothing else", endpoint)
	}
	if endpoint["originProtocolPolicy"].StringValue() != vpcOriginProtocolPolicy || endpoint["httpPort"].NumberValue() != originListenerPort {
		t.Errorf("vpc origin = %v, want CloudFront to reach the listener on %d over its private path", endpoint, originListenerPort)
	}
	role := recordedOf(t, rec, "aws:iam/role:Role")
	if !strings.Contains(role["assumeRolePolicy"].StringValue(), iam.ECSTasksPrincipal) {
		t.Errorf("the execution role trusts %q, want %s", role["assumeRolePolicy"].StringValue(), iam.ECSTasksPrincipal)
	}
	if len(rec.attached) != 1 || rec.attached[0] != ecsTaskExecutionPolicyARN {
		t.Errorf("attached %v, want only the ECS task execution policy", rec.attached)
	}
	groups := 0
	for key, inputs := range rec.recorded {
		if !strings.HasPrefix(key, "aws:ec2/securityGroup:SecurityGroup::") {
			continue
		}
		groups++
		ingress := inputs["ingress"].ArrayValue()[0].ObjectValue()
		if strings.HasSuffix(key, "tasks-security-group") && ingress["fromPort"].NumberValue() != TaskPort {
			t.Errorf("the task security group admits port %v, want %d and only from the front", ingress["fromPort"], TaskPort)
		}
		if strings.HasSuffix(key, "front-security-group") {
			if _, open := ingress["cidrBlocks"]; open || len(ingress["prefixListIds"].ArrayValue()) != 1 {
				t.Errorf("the front admits %v, want only CloudFront's origin-facing prefix list: the secret must never cross the open internet", ingress)
			}
		}
	}
	if groups != 2 {
		t.Errorf("declared %d security groups, want one for the front and one for the tasks", groups)
	}
}

func TestTheContainerSecurityGroupsKeepTheDescriptionsTheyWereCreatedWith(t *testing.T) {
	t.Parallel()

	infra := &work{tier: environment.TierPreview, boundary: "arn:aws:iam::123456789012:policy/ocel-app-boundary", tags: stackTags(environment.TierPreview)}
	rec := &inputRecorder{}
	if err := pulumi.RunErr(func(pctx *pulumi.Context) error { return infra.run(pctx) }, pulumi.WithMocks("ocel-containers", "preview--infra", rec)); err != nil {
		t.Fatalf("run the container infrastructure program: %v", err)
	}

	want := map[string]string{
		"front-security-group": "Ocel: the load balancer every container app in the preview class answers behind",
		"tasks-security-group": "Ocel: the tasks every container app in the preview class runs as",
	}
	for key, inputs := range rec.recorded {
		if !strings.HasPrefix(key, "aws:ec2/securityGroup:SecurityGroup::") {
			continue
		}
		for suffix, description := range want {
			if strings.HasSuffix(key, suffix) && inputs["description"].StringValue() != description {
				t.Errorf("%s is described %q, want %q: a new description replaces the group, and the replacement cannot take the name the group already holds", suffix, inputs["description"].StringValue(), description)
			}
		}
	}
}

func TestDecodeContainerInfraReadsEveryOutputAndRefusesAnEmptyOne(t *testing.T) {
	t.Parallel()

	decoded, err := decode(infrastructureOutputs())
	if err != nil {
		t.Fatalf("decode() = %v", err)
	}
	if decoded.OriginHost != fixtureOrigin || decoded.VPCOrigin != fixtureFront || len(decoded.Subnets) != 2 || decoded.Listener != fixtureListener {
		t.Errorf("decoded = %+v, want every output the program exported", decoded)
	}
	partial := infrastructureOutputs()
	delete(partial, outputKeyListener)
	if _, err := decode(partial); err == nil {
		t.Error("decode accepted outputs with no listener, so a container would have no rule to hang off")
	}
}

type subnetLookups struct {
	inputRecorder
	lookupMu sync.Mutex
	filters  [][]string
}

func (s *subnetLookups) Call(args pulumi.MockCallArgs) (resource.PropertyMap, error) {
	if args.Token != "aws:ec2/getSubnets:getSubnets" {
		return s.inputRecorder.Call(args)
	}
	var seen []string
	for _, filter := range args.Args["filters"].ArrayValue() {
		fields := filter.ObjectValue()
		seen = append(seen, fields["name"].StringValue())
	}
	s.lookupMu.Lock()
	s.filters = append(s.filters, seen)
	s.lookupMu.Unlock()
	return resource.PropertyMap{}, nil
}

func (s *subnetLookups) filtered() [][]string {
	s.lookupMu.Lock()
	defer s.lookupMu.Unlock()
	return slices.Clone(s.filters)
}

type zoneLookups struct {
	inputRecorder
	lookupMu sync.Mutex
	excluded []string
	zoneIDs  []string
}

func (z *zoneLookups) Call(args pulumi.MockCallArgs) (resource.PropertyMap, error) {
	z.lookupMu.Lock()
	defer z.lookupMu.Unlock()
	switch args.Token {
	case "aws:index/getAvailabilityZones:getAvailabilityZones":
		for _, zone := range args.Args["excludeZoneIds"].ArrayValue() {
			z.excluded = append(z.excluded, zone.StringValue())
		}
		return resource.PropertyMap{"zoneIds": resource.NewArrayProperty([]resource.PropertyValue{
			resource.NewStringProperty("use1-az1"), resource.NewStringProperty("use1-az2"),
		})}, nil
	case "aws:ec2/getSubnets:getSubnets":
		for _, filter := range args.Args["filters"].ArrayValue() {
			fields := filter.ObjectValue()
			if fields["name"].StringValue() != "availability-zone-id" {
				continue
			}
			for _, value := range fields["values"].ArrayValue() {
				z.zoneIDs = append(z.zoneIDs, value.StringValue())
			}
		}
	}
	return z.inputRecorder.Call(args)
}

func TestTheContainerInfraLooksOnlyInZonesACloudFrontVPCOriginReaches(t *testing.T) {
	t.Parallel()

	lookups := &zoneLookups{}
	if err := pulumi.RunErr(func(pctx *pulumi.Context) error { return publicInfraWork().run(pctx) }, pulumi.WithMocks("ocel-containers", "production--infra", lookups)); err != nil {
		t.Fatalf("run the container infrastructure program: %v", err)
	}

	lookups.lookupMu.Lock()
	defer lookups.lookupMu.Unlock()
	if !slices.Contains(lookups.excluded, "use1-az3") {
		t.Errorf("the zone lookup excluded %v, want use1-az3 among them: CloudFront refuses a VPC origin whose load balancer has a subnet there", lookups.excluded)
	}
	if !slices.Equal(lookups.zoneIDs, []string{"use1-az1", "use1-az2"}) {
		t.Errorf("the subnet lookup kept zones %v, want only those the zone lookup returned", lookups.zoneIDs)
	}
}

func TestTheContainerInfraLooksOnlyInTheDefaultSubnetsOfTheDefaultVPC(t *testing.T) {
	t.Parallel()

	infra := &work{tier: environment.TierProduction, boundary: componenttest.BoundaryARN, tags: stackTags(environment.TierProduction)}
	lookups := &subnetLookups{}
	if err := pulumi.RunErr(func(pctx *pulumi.Context) error { return infra.run(pctx) }, pulumi.WithMocks("ocel-containers", "production--infra", lookups)); err != nil {
		t.Fatalf("run the container infrastructure program: %v", err)
	}

	assertDefaultSubnetsOnly(t, lookups.filtered())
}
