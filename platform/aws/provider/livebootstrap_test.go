//go:build integration

package aws_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/aws/aws-sdk-go-v2/service/cloudformation"

	"github.com/ocelhq/ocel/pkg/buildoutput"
	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/naming"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/stackrecords"
	aws "github.com/ocelhq/ocel/platform/aws/provider"
	"github.com/ocelhq/ocel/platform/aws/provider/bootstrap"
	"github.com/ocelhq/ocel/platform/aws/provider/tier/imageoptimizer"
)

func TestLiveBootstrapProvisionsTheAccountAndASecondRunPlansNothing(t *testing.T) {
	a := live(t)
	tier := environment.TierProduction
	boot := a.emptied(t, tier)
	ctx := context.Background()

	fresh, err := boot.Describe(ctx, tier)
	if err != nil {
		t.Fatalf("Describe() of an account nothing has bootstrapped = %v", err)
	}
	if fresh.Present {
		t.Fatal("Describe() claims a bootstrap on an account nothing has written to")
	}

	req := provider.BootstrapRequest{Tier: tier, WrittenBy: liveWriter}
	plan, err := boot.Plan(ctx, req)
	if err != nil {
		t.Fatalf("Plan() = %v", err)
	}
	core := groupNamed(t, plan, "aws/"+coreStackName)
	if core.Action != provider.ActionCreate {
		t.Errorf("Plan() against a fresh account plans %s as %q, want %q", core.Name, core.Action, provider.ActionCreate)
	}
	for _, want := range []string{"StateBucket", "StateTable", "ArtifactBucket", "AssetBucket", "VariablesTable", "AppBoundary"} {
		if planned := changeFor(core, want); planned.Action != provider.ActionCreate {
			t.Errorf("Plan() shows %s as %q, want it created", want, planned.Action)
		}
	}
	origin, err := defaultNamespace.OriginSecretParamFor(tier)
	if err != nil {
		t.Fatal(err)
	}
	params := groupNamed(t, plan, "aws/"+bootstrap.ParamGroupName)
	if params.Action != provider.ActionCreate {
		t.Errorf("Plan() against a fresh account plans %s as %q, want %q", params.Name, params.Action, provider.ActionCreate)
	}
	for _, want := range []string{origin, passphraseParam} {
		if planned := changeFor(params, want); planned.Action != provider.ActionCreate {
			t.Errorf("Plan() shows %s as %q, want it created", want, planned.Action)
		}
	}

	if err := boot.Apply(ctx, req, nil); err != nil {
		t.Fatalf("Apply() = %v", err)
	}

	installed, err := boot.Describe(ctx, tier)
	if err != nil {
		t.Fatalf("Describe() after Apply() = %v", err)
	}
	if !installed.Present {
		t.Fatal("Describe() after Apply() shows no bootstrap")
	}
	stack := stackNamed(t, installed, coreStackName)
	if !stack.Present || !stack.DigestCurrent {
		t.Errorf("Describe() after Apply() = %+v, want the core stack present at the digest applied", stack)
	}
	if stack.WrittenBy != string(liveWriter) {
		t.Errorf("the core stack records writer %q, want the writer that applied it", stack.WrittenBy)
	}

	if status := a.stackStatus(t, coreStackName); status != "CREATE_COMPLETE" {
		t.Errorf("%s is in state %q in CloudFormation, want CREATE_COMPLETE", coreStackName, status)
	}
	deployed, err := bootstrap.CheckDeployedFor(ctx, cloudformation.NewFromConfig(a.aws), defaultNamespace, tier)
	if err != nil {
		t.Fatalf("reading back what the bootstrap deployed = %v", err)
	}
	for _, bucket := range []string{deployed.StateBucket, deployed.ArtifactBucket, deployed.AssetBucket} {
		if bucket == "" {
			t.Fatalf("the stack names no bucket for one of its outputs: %+v", deployed)
		}
		if !a.bucketExists(t, bucket) {
			t.Errorf("%s is named by the stack but no bucket answers for it", bucket)
		}
	}
	for _, table := range []string{deployed.StateTable, deployed.VariablesTable} {
		if table == "" {
			t.Fatalf("the stack names no table for one of its outputs: %+v", deployed)
		}
		if !a.tableExists(t, table) {
			t.Errorf("%s is named by the stack but no table answers for it", table)
		}
	}
	if deployed.AppBoundaryARN == "" {
		t.Errorf("the bootstrap is installed without an app boundary: %+v", deployed)
	}
	if deployed.VariablesKeyARN != "" {
		t.Errorf("a run that never asked for %s made a key anyway: %+v", provider.VariablesKeyPart, deployed)
	}
	for _, param := range []string{origin, passphraseParam} {
		if !a.paramExists(t, param) {
			t.Errorf("%s was planned and applied but SSM has no such parameter", param)
		}
	}

	again, err := boot.Plan(ctx, provider.BootstrapRequest{Tier: tier, WrittenBy: liveWriter})
	if err != nil {
		t.Fatalf("a second Plan() = %v", err)
	}
	for _, group := range again.Groups {
		if group.Action != provider.ActionKeep {
			t.Errorf("a second Plan() over a bootstrapped account plans %s as %q, want %q", group.Name, group.Action, provider.ActionKeep)
		}
		for _, change := range group.Changes {
			if change.Action != provider.ActionKeep {
				t.Errorf("a second Plan() shows %s as %q, want it kept", change.Name, change.Action)
			}
		}
	}
}

func TestLiveTheFirstDeployOfAnAppThatOptimizesImagesAddsTheImageOptimizerAndRemovingTheTierRemovesIt(t *testing.T) {
	a := live(t)
	tier := environment.TierProduction
	boot := a.emptied(t, tier)
	ctx := context.Background()

	if err := boot.Apply(ctx, provider.BootstrapRequest{Tier: tier, WrittenBy: liveWriter}, nil); err != nil {
		t.Fatalf("Apply() = %v", err)
	}
	project := t.TempDir()
	root, err := buildoutput.Root(project)
	if err != nil {
		t.Fatal(err)
	}
	app := buildoutput.AppRoot(root, "web")
	if err := os.MkdirAll(app, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(app, buildoutput.HostingFile), []byte(`{"version":1,"framework":"next","needs":{}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(app, naming.ImageConfigFile), []byte(`{}`), 0o600); err != nil {
		t.Fatal(err)
	}

	p, err := aws.New(ctx, provider.Settings{ProjectDir: project, WrittenBy: liveWriter, Options: provider.Options{"region": liveRegion}})
	if err != nil {
		t.Fatalf("New() = %v", err)
	}
	if err := p.Hooks().PreflightDeploy(ctx, provider.DeployPreflight{Deploy: provider.DeploySpec{
		Slug: "shop",
		Tier: tier,
		Apps: []provider.AppEntry{{App: "web", Manifest: &contractv1.ManifestApp{Name: "web", Framework: &contractv1.Framework{Name: "next"}}}},
	}}); err != nil {
		t.Fatalf("PreflightDeploy() = %v", err)
	}
	function := imageoptimizer.NamesFor(string(defaultNamespace), tier).Function
	if !a.functionExists(function) {
		t.Errorf("Lambda has no %s once the first deploy that optimizes images added it", function)
	}
	recorded, present, err := stackrecords.ReadBootstrapPart(ctx, p.KeyValues(), tier, imageoptimizer.Part)
	if err != nil || !present || recorded.State != stackrecords.PartReady || recorded.AddedBy != "shop" || imageoptimizer.URLOf(recorded.Resources) == "" {
		t.Errorf("part record = %+v, %v, %v, want it ready, added by shop, with the url every front calls it on", recorded, present, err)
	}

	if err := boot.Remove(ctx, tier, nil); err != nil {
		t.Fatalf("Remove(%s) = %v", tier, err)
	}
	if a.functionExists(function) {
		t.Errorf("the image optimizer %s outlived the tier it served", function)
	}
}

func stackNamed(t *testing.T, described provider.BootstrapDescription, name string) provider.BootstrapStack {
	t.Helper()
	for _, stack := range described.Stacks {
		if stack.Name == name {
			return stack
		}
	}
	t.Fatalf("Describe() has no stack named %q", name)
	return provider.BootstrapStack{}
}
