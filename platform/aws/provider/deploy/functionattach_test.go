package deploy

import (
	"slices"
	"testing"

	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"

	"github.com/ocelhq/ocel/pkg/provider"
)

func TestOnlyProvisionedPostgresAndKVNeedTheFunctionNetwork(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name   string
		grants []provider.Binding
		want   bool
	}{
		{"no grant", nil, false},
		{"a bucket", []provider.Binding{{Type: provider.BindingBucket, Name: "bucket--uploads"}}, false},
		{"a topic and a task", []provider.Binding{{Type: provider.BindingTopic, Name: "topic--a"}, {Type: provider.BindingTask, Name: "task--b"}}, false},
		{"provisioned postgres", []provider.Binding{{Type: provider.BindingPostgres, Name: "db--main"}}, true},
		{"provisioned kv", []provider.Binding{{Type: provider.BindingKV, Name: "kv--cache"}}, true},
		{"postgres the user's own IaC provisions", []provider.Binding{{Type: provider.BindingPostgres, Name: "db--main", Source: "terraform"}}, false},
		{"kv another project published", []provider.Binding{{Type: provider.BindingKV, Name: "kv--cache", Source: "ocel"}}, false},
		{"a bucket beside provisioned postgres", []provider.Binding{{Type: provider.BindingBucket, Name: "bucket--uploads"}, {Type: provider.BindingPostgres, Name: "db--main"}}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			if got := needsFunctionNetwork(tc.grants); got != tc.want {
				t.Errorf("needsFunctionNetwork() = %v, want %v", got, tc.want)
			}
		})
	}
}

func attachedNetwork() *functionNetwork {
	return &functionNetwork{Subnets: []string{"subnet-private-a", "subnet-private-b"}, SecurityGroup: "sg-functions"}
}

func runAppProgram(t *testing.T, work *appWork) *inputRecorder {
	t.Helper()
	rec := &inputRecorder{}
	program := func(pctx *pulumi.Context) error { return work.run(pctx, nil) }
	if err := pulumi.RunErr(program, pulumi.WithMocks("shop", "prod", rec)); err != nil {
		t.Fatalf("run the app program: %v", err)
	}
	return rec
}

func TestAnAppAttachedToTheFunctionNetworkPlacesEveryFunctionInItsPrivateSubnets(t *testing.T) {
	t.Parallel()

	cfg, spec := appStackSpec(t)
	spec.App.Grants = []provider.Binding{{Type: provider.BindingPostgres, Name: "db--main"}}
	work, err := releasing(t, cfg).appWork(spec, nil, attachedNetwork())
	if err != nil {
		t.Fatalf("appWork() = %v", err)
	}

	rec := runAppProgram(t, work)

	functions := rec.registered("aws:lambda/function:Function")
	if len(functions) != 2 {
		t.Fatalf("declared %d functions, want both of the app's", len(functions))
	}
	for _, name := range functions {
		placed := rec.object(t, "aws:lambda/function:Function", name, "vpcConfig")
		if got := stringsAt(placed, "subnetIds"); !slices.Equal(got, attachedNetwork().Subnets) {
			t.Errorf("%s runs in subnets %v, want the function network's private subnets", name, got)
		}
		if got := stringsAt(placed, "securityGroupIds"); !slices.Equal(got, []string{"sg-functions"}) {
			t.Errorf("%s runs in security groups %v, want the function network's", name, got)
		}
	}
	if !slices.Contains(rec.attached, "arn:aws:iam::aws:policy/service-role/AWSLambdaVPCAccessExecutionRole") {
		t.Errorf("attached %v, want the policy that lets Lambda manage network interfaces: without it the function cannot be created in a VPC", rec.attached)
	}
}

func TestAnAppThatReachesNoVPCOnlyResourceStaysOutsideTheVPC(t *testing.T) {
	t.Parallel()

	cfg, spec := appStackSpec(t)
	spec.App.Grants = []provider.Binding{{Type: provider.BindingBucket, Name: "bucket--uploads"}}
	work, err := releasing(t, cfg).appWork(spec, nil, nil)
	if err != nil {
		t.Fatalf("appWork() = %v", err)
	}

	rec := runAppProgram(t, work)

	for _, name := range rec.registered("aws:lambda/function:Function") {
		if _, placed := rec.inputs("aws:lambda/function:Function", name)["vpcConfig"]; placed {
			t.Errorf("%s was placed in a VPC, want it outside: a function in a VPC loses its internet access", name)
		}
	}
	if slices.Contains(rec.attached, "arn:aws:iam::aws:policy/service-role/AWSLambdaVPCAccessExecutionRole") {
		t.Errorf("attached %v, want no VPC access policy", rec.attached)
	}
}

func TestATransformThatPlacesAFunctionInAVPCKeepsItOutOfTheFunctionNetwork(t *testing.T) {
	t.Parallel()

	cfg, spec := appStackSpec(t)
	spec.App.Grants = []provider.Binding{{Type: provider.BindingPostgres, Name: "db--main"}}
	transformed := &transformPatches{vpcBound: map[string]bool{"fn--web--admin": true}}
	work, err := releasing(t, cfg).appWork(spec, transformed, attachedNetwork())
	if err != nil {
		t.Fatalf("appWork() = %v", err)
	}

	rec := runAppProgram(t, work)

	placed := 0
	for _, name := range rec.registered("aws:lambda/function:Function") {
		if _, ours := rec.inputs("aws:lambda/function:Function", name)["vpcConfig"]; ours {
			placed++
		}
	}
	if placed != 1 {
		t.Errorf("%d functions were placed in the function network, want only the one a transform did not place", placed)
	}
}

func TestTheWorkersOfAnAttachedAppRunInThePrivateSubnetsToo(t *testing.T) {
	t.Parallel()

	release, spec, _, _ := newReleaseHostingWorkers(t, "media")
	spec.App.Grants = []provider.Binding{{Type: provider.BindingKV, Name: "kv--cache"}}
	work, err := release.appWork(spec, nil, attachedNetwork())
	if err != nil {
		t.Fatalf("appWork() = %v", err)
	}
	hosted, err := release.workersWork(spec, work)
	if err != nil {
		t.Fatalf("workersWork() = %v", err)
	}

	rec := &inputRecorder{}
	if err := pulumi.RunErr(hosted.run, pulumi.WithMocks("shop", "prod-workers", rec)); err != nil {
		t.Fatalf("run the workers program: %v", err)
	}

	functions := rec.registered("aws:lambda/function:Function")
	if len(functions) != 1 {
		t.Fatalf("declared %d functions, want the one worker", len(functions))
	}
	placed := rec.object(t, "aws:lambda/function:Function", functions[0], "vpcConfig")
	if got := stringsAt(placed, "subnetIds"); !slices.Equal(got, attachedNetwork().Subnets) {
		t.Errorf("the worker runs in subnets %v, want the function network's private subnets", got)
	}
	if !slices.Contains(rec.attached, "arn:aws:iam::aws:policy/service-role/AWSLambdaVPCAccessExecutionRole") {
		t.Errorf("attached %v, want the worker role to manage network interfaces", rec.attached)
	}
}
