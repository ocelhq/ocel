package containers

import (
	"maps"
	"slices"
	"strings"
	"testing"

	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"
)

func TestEveryContainerInfraResourceIsNamedForWhatItIsInTheAccount(t *testing.T) {
	t.Parallel()

	rec := &inputRecorder{}
	if err := pulumi.RunErr(func(pctx *pulumi.Context) error { return publicInfraWork().run(pctx) }, pulumi.WithMocks("ocel-containers", "production--infra", rec)); err != nil {
		t.Fatalf("run the container infrastructure program: %v", err)
	}

	want := map[string][]string{
		"aws:ecs/cluster:Cluster":                            {"ocel-prod-containers"},
		"aws:ec2/securityGroup:SecurityGroup":                {"ocel-prod-containers-sg", "ocel-prod-containers-tasks-sg", "ocel-prod-public-sg"},
		"aws:lb/loadBalancer:LoadBalancer":                   {"ocel-prod-containers", "ocel-prod-public"},
		"aws:lb/listener:Listener":                           {"ocel-prod-containers/http", "ocel-prod-public/http", "ocel-prod-public/https"},
		"aws:cloudfront/vpcOrigin:VpcOrigin":                 {"ocel-prod-containers"},
		"aws:iam/role:Role":                                  {"ocel-prod-containers-exec", "ocel-prod-public-liveness"},
		"aws:iam/rolePolicyAttachment:RolePolicyAttachment":  {"ocel-prod-containers-exec/AmazonECSTaskExecutionRolePolicy"},
		"aws:cloudwatch/logGroup:LogGroup":                   {"/ecs/ocel-prod-containers"},
		"aws:acm/certificate:Certificate":                    {"ocel-prod-public/certificate"},
		"aws:lambda/function:Function":                       {"ocel-prod-public-liveness"},
		"aws:lb/targetGroup:TargetGroup":                     {"ocel-prod-public-tg"},
		"aws:lambda/permission:Permission":                   {"ocel-prod-public-liveness/invoke"},
		"aws:lb/targetGroupAttachment:TargetGroupAttachment": {"ocel-prod-public-tg/ocel-prod-public-liveness"},
		"aws:lb/listenerRule:ListenerRule":                   {"ocel-prod-public/https/liveness"},
	}
	declared := map[string][]string{}
	for key := range rec.recorded {
		token, name, _ := strings.Cut(key, "::")
		declared[token] = append(declared[token], name)
	}
	for token, names := range declared {
		slices.Sort(names)
		if !slices.Equal(names, want[token]) {
			t.Errorf("the program declares %s %v, want %v: a Pulumi name is the physical name, or <parent>/<part> where AWS gives none", token, names, want[token])
		}
	}
	for _, token := range slices.Sorted(maps.Keys(want)) {
		if _, found := declared[token]; !found {
			t.Errorf("the program declares no %s, want %v", token, want[token])
		}
	}
}

func TestEveryContainerSecurityGroupCarriesItsNameAsTheNameTheConsoleLists(t *testing.T) {
	t.Parallel()

	rec := &inputRecorder{}
	if err := pulumi.RunErr(func(pctx *pulumi.Context) error { return publicInfraWork().run(pctx) }, pulumi.WithMocks("ocel-containers", "production--infra", rec)); err != nil {
		t.Fatalf("run the container infrastructure program: %v", err)
	}
	for _, name := range rec.registered("aws:ec2/securityGroup:SecurityGroup") {
		inputs := rec.recorded["aws:ec2/securityGroup:SecurityGroup::"+name]
		if tag := inputs["tags"].ObjectValue()["Name"]; !tag.IsString() || tag.StringValue() != inputs["name"].StringValue() {
			t.Errorf("security group %s carries Name tag %v, want its name %q", name, tag, inputs["name"].StringValue())
		}
	}
}
