package provider_test

import (
	"context"
	"testing"

	"github.com/aws/aws-sdk-go-v2/service/cloudformation"

	"github.com/ocelhq/ocel/pkg/providerkit"
	"github.com/ocelhq/ocel/platform/aws/provider/bootstrap"
)

func TestLiveBootstrapStandsTheAccountUpAndASecondRunPlansNothing(t *testing.T) {
	a := live(t)
	class := providerkit.ClassProduction
	bootstrapper := a.emptied(t, class)
	ctx := context.Background()

	fresh, err := bootstrapper.Describe(ctx, class)
	if err != nil {
		t.Fatalf("Describe() of an account nothing has bootstrapped = %v", err)
	}
	if fresh.Present {
		t.Fatal("Describe() claims a bootstrap on an account nothing has written to")
	}

	req := providerkit.BootstrapRequest{Class: class, Writer: liveWriter, Held: fresh.Held}
	plan, err := bootstrapper.Plan(ctx, req)
	if err != nil {
		t.Fatalf("Plan() = %v", err)
	}
	core := groupNamed(t, plan, "aws/"+bootstrap.StackName)
	if core.Action != providerkit.ActionCreate {
		t.Errorf("Plan() against a fresh account plans %s as %q, want %q", core.Name, core.Action, providerkit.ActionCreate)
	}
	for _, want := range []string{"StateBucket", "StateTable", "ArtifactBucket", "AssetBucket", "VarsKey", "VarsTable", "AppBoundary"} {
		if planned := changeFor(core, want); planned.Action != providerkit.ActionCreate {
			t.Errorf("Plan() shows %s as %q, want it created", want, planned.Action)
		}
	}
	origin, err := bootstrap.OriginSecretParamFor(string(class))
	if err != nil {
		t.Fatal(err)
	}
	params := groupNamed(t, plan, "aws/"+bootstrap.ParamGroupName)
	if params.Action != providerkit.ActionCreate {
		t.Errorf("Plan() against a fresh account plans %s as %q, want %q", params.Name, params.Action, providerkit.ActionCreate)
	}
	for _, want := range []string{origin, bootstrap.PassphraseParamName} {
		if planned := changeFor(params, want); planned.Action != providerkit.ActionCreate {
			t.Errorf("Plan() shows %s as %q, want it created", want, planned.Action)
		}
	}

	if err := bootstrapper.Apply(ctx, req, nil); err != nil {
		t.Fatalf("Apply() = %v", err)
	}

	standing, err := bootstrapper.Describe(ctx, class)
	if err != nil {
		t.Fatalf("Describe() after Apply() = %v", err)
	}
	if !standing.Present {
		t.Fatal("Describe() after Apply() shows no bootstrap")
	}
	stack := stackNamed(t, standing, bootstrap.StackName)
	if !stack.Present || !stack.DigestCurrent {
		t.Errorf("Describe() after Apply() = %+v, want the core stack standing at the digest applied", stack)
	}
	if stack.Writer != string(liveWriter) {
		t.Errorf("the core stack records writer %q, want the writer that applied it", stack.Writer)
	}
	if stack.Schema != uint32(bootstrap.RequiredSchema) {
		t.Errorf("the core stack records schema %d, want %d", stack.Schema, bootstrap.RequiredSchema)
	}

	if status := a.stackStatus(t, bootstrap.StackName); status != "CREATE_COMPLETE" {
		t.Errorf("%s stands at %q in CloudFormation, want CREATE_COMPLETE", bootstrap.StackName, status)
	}
	held, err := bootstrap.CheckDeployedFor(ctx, cloudformation.NewFromConfig(a.aws), string(class))
	if err != nil {
		t.Fatalf("reading back what the bootstrap deployed = %v", err)
	}
	for _, bucket := range []string{held.StateBucket, held.ArtifactBucket, held.AssetBucket} {
		if bucket == "" {
			t.Fatalf("the stack names no bucket for one of its outputs: %+v", held)
		}
		if !a.bucketStands(t, bucket) {
			t.Errorf("%s is named by the stack but no bucket answers for it", bucket)
		}
	}
	for _, table := range []string{held.StateTable, held.VarsTable} {
		if table == "" {
			t.Fatalf("the stack names no table for one of its outputs: %+v", held)
		}
		if !a.tableStands(t, table) {
			t.Errorf("%s is named by the stack but no table answers for it", table)
		}
	}
	if held.VarsKeyARN == "" || held.AppBoundaryARN == "" {
		t.Errorf("the bootstrap stands without a variable key or an app boundary: %+v", held)
	}
	for _, param := range []string{origin, bootstrap.PassphraseParamName} {
		if !a.paramStands(t, param) {
			t.Errorf("%s was planned and applied but SSM holds no such parameter", param)
		}
	}

	again, err := bootstrapper.Plan(ctx, providerkit.BootstrapRequest{Class: class, Writer: liveWriter, Held: standing.Held})
	if err != nil {
		t.Fatalf("a second Plan() = %v", err)
	}
	for _, group := range again.Groups {
		if group.Action != providerkit.ActionKeep {
			t.Errorf("a second Plan() over a bootstrapped account plans %s as %q, want %q", group.Name, group.Action, providerkit.ActionKeep)
		}
		for _, change := range group.Changes {
			if change.Action != providerkit.ActionKeep {
				t.Errorf("a second Plan() shows %s as %q, want it kept", change.Name, change.Action)
			}
		}
	}
}

func TestLiveApplyingTheImageOptimizerStandsItsOwnStackBesideTheCore(t *testing.T) {
	a := live(t)
	class := providerkit.ClassProduction
	bootstrapper := a.emptied(t, class)
	ctx := context.Background()

	feature := bootstrap.FeatureImageOptimization
	req := providerkit.BootstrapRequest{Class: class, Writer: liveWriter, Features: []string{feature}}
	if err := bootstrapper.Apply(ctx, req, nil); err != nil {
		t.Fatalf("Apply(%s) = %v", feature, err)
	}

	name := bootstrap.FeatureStackName(feature, string(class))
	if status := a.stackStatus(t, name); status != "CREATE_COMPLETE" {
		t.Errorf("%s stands at %q in CloudFormation, want CREATE_COMPLETE", name, status)
	}
	standing, err := bootstrapper.Describe(ctx, class)
	if err != nil {
		t.Fatalf("Describe() after Apply(%s) = %v", feature, err)
	}
	stack := stackNamed(t, standing, name)
	if !stack.Present || !stack.DigestCurrent {
		t.Errorf("Describe() = %+v, want the feature stack standing at the digest applied", stack)
	}
	held, err := bootstrap.CheckDeployedFor(ctx, cloudformation.NewFromConfig(a.aws), string(class))
	if err != nil {
		t.Fatal(err)
	}
	if held.ImageOptimizerURL == "" {
		t.Error("the optimizer stands without the URL every front calls it on")
	}
	if !held.Features.Has(feature) {
		t.Errorf("the account reads back features %v, want %s among them", held.Features.Names(), feature)
	}

	again, err := bootstrapper.Plan(ctx, providerkit.BootstrapRequest{Class: class, Writer: liveWriter, Features: []string{feature}, Held: standing.Held})
	if err != nil {
		t.Fatalf("a second Plan(%s) = %v", feature, err)
	}
	if group := groupNamed(t, again, "aws/"+name); group.Action != providerkit.ActionKeep {
		t.Errorf("a second Plan() over a standing %s plans %q, want %q", feature, group.Action, providerkit.ActionKeep)
	}
}

func stackNamed(t *testing.T, described providerkit.Bootstrap, name string) providerkit.BootstrapStack {
	t.Helper()
	for _, stack := range described.Stacks {
		if stack.Name == name {
			return stack
		}
	}
	t.Fatalf("Describe() carries no stack named %q", name)
	return providerkit.BootstrapStack{}
}
