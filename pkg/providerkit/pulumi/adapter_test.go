package pulumi_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/pulumi/pulumi/sdk/v3/go/auto"
	"github.com/pulumi/pulumi/sdk/v3/go/common/apitype"
	sdk "github.com/pulumi/pulumi/sdk/v3/go/pulumi"

	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/providerkit"
	"github.com/ocelhq/ocel/pkg/providerkit/pulumi"
)

type program struct{ config auto.ConfigMap }

func (program) Run(*sdk.Context, providerkit.StackPlan) error { return nil }

type configuring struct{ program }

func (c configuring) Configure(context.Context, providerkit.StackPlan) (auto.ConfigMap, error) {
	return c.config, nil
}

func access() pulumi.Access {
	return pulumi.Access{
		BackendURL: "s3://ocel-state/shop",
		Passphrase: "a-passphrase",
		Env:        map[string]string{"VENDOR_REGION": "nowhere"},
	}
}

func plan() providerkit.StackPlan {
	return providerkit.StackPlan{
		Ref: providerkit.StackRef{
			Project: "shop",
			Class:   providerkit.ClassProduction,
			Name:    naming.InfraStack("prod"),
		},
		Kind: providerkit.StackInfra,
	}
}

func step(op apitype.OpType, kind, name string) apitype.StepEventMetadata {
	return apitype.StepEventMetadata{
		Op:   op,
		Type: kind,
		URN:  "urn:pulumi:prod::ocel-shop::" + kind + "::" + name,
	}
}

func TestPreviewTurnsWhatTheEngineWouldDoIntoPlanRows(t *testing.T) {
	t.Parallel()

	engine := &recordingEngine{steps: []apitype.StepEventMetadata{
		step(apitype.OpCreate, "aws:rds/cluster:Cluster", "orders"),
		step(apitype.OpUpdate, "aws:s3/bucket:Bucket", "uploads"),
		step(apitype.OpDelete, "aws:s3/bucket:Bucket", "exports"),
		step(apitype.OpSame, "aws:iam/role:Role", "app"),
		step(apitype.OpReplace, "aws:rds/instance:Instance", "reporting"),
		step(apitype.OpSame, "pulumi:pulumi:Stack", "ocel-shop-prod"),
	}}

	produced, err := pulumi.New(pulumi.Config{Access: access(), Program: program{}, Engine: engine}).
		Preview(context.Background(), plan(), nil)
	if err != nil {
		t.Fatalf("Preview() = %v", err)
	}
	if engine.previewed != pulumi.OpProvision {
		t.Errorf("the engine was asked to preview %q, want the provision it mirrors", engine.previewed)
	}
	if len(produced.Groups) != 1 {
		t.Fatalf("Preview() returned %d groups, want the one stack it previews", len(produced.Groups))
	}
	rows := map[string]providerkit.ChangeAction{}
	for _, change := range produced.Groups[0].Changes {
		rows[change.Name] = change.Action
	}
	want := map[string]providerkit.ChangeAction{
		"orders":    providerkit.ActionCreate,
		"uploads":   providerkit.ActionUpdate,
		"exports":   providerkit.ActionDelete,
		"app":       providerkit.ActionKeep,
		"reporting": providerkit.ActionReplace,
	}
	for name, action := range want {
		if rows[name] != action {
			t.Errorf("%s reads %q, want %q", name, rows[name], action)
		}
	}
	if _, carried := rows["ocel-shop-prod"]; carried {
		t.Error("the stack resource itself is a plan row, and nothing in the customer's account answers to it")
	}
}

func TestPreviewDestroyShowsWhatTheTeardownWouldTakeDown(t *testing.T) {
	t.Parallel()

	engine := &recordingEngine{steps: []apitype.StepEventMetadata{
		step(apitype.OpDelete, "aws:s3/bucket:Bucket", "uploads"),
	}}

	produced, err := pulumi.New(pulumi.Config{Access: access(), Program: program{}, Engine: engine}).
		PreviewDestroy(context.Background(), plan().Ref, nil)
	if err != nil {
		t.Fatalf("PreviewDestroy() = %v", err)
	}
	if engine.previewed != pulumi.OpDestroy {
		t.Errorf("the engine was asked to preview %q, want the destroy it mirrors", engine.previewed)
	}
	if len(produced.Groups) != 1 || produced.Groups[0].Action != providerkit.ActionDelete {
		t.Fatalf("PreviewDestroy() = %+v, want the stack shown as going", produced.Groups)
	}
}

func TestWorkspaceCarriesTheBackendTheProviderNamed(t *testing.T) {
	t.Parallel()

	setup, err := pulumi.New(pulumi.Config{Access: access(), Program: program{}}).Workspace(plan())
	if err != nil {
		t.Fatalf("Workspace() = %v", err)
	}
	if setup.Project.Backend == nil || setup.Project.Backend.URL != access().BackendURL {
		t.Fatalf("the workspace points at %+v, want the backend the provider named", setup.Project.Backend)
	}
	if setup.Project.Runtime.Name() != "go" {
		t.Errorf("the workspace runs %q, want the go runtime a kit provider's program is written in", setup.Project.Runtime.Name())
	}
	if string(setup.Project.Name) != naming.PulumiProject("shop") {
		t.Errorf("the workspace's project is %q, want %q", setup.Project.Name, naming.PulumiProject("shop"))
	}
	if len(setup.Options) == 0 {
		t.Error("the workspace carries no options, so nothing configures a local workspace from it")
	}
}

func TestWorkspaceCarriesThePassphraseAndTheVendorsOwnEnvironment(t *testing.T) {
	t.Parallel()

	setup, err := pulumi.New(pulumi.Config{Access: access(), Program: program{}}).Workspace(plan())
	if err != nil {
		t.Fatalf("Workspace() = %v", err)
	}
	if setup.EnvVars["PULUMI_CONFIG_PASSPHRASE"] != "a-passphrase" {
		t.Error("the workspace does not carry the passphrase, so the state it writes would be unsealed")
	}
	if setup.EnvVars["VENDOR_REGION"] != "nowhere" {
		t.Errorf("the workspace's environment is %v, want the vendor's own variables carried through", setup.EnvVars)
	}
}

func TestWorkspaceTakesTheProjectNameTheProviderPins(t *testing.T) {
	t.Parallel()

	pinned := access()
	pinned.Project = "ocel-shop-pinned"
	setup, err := pulumi.New(pulumi.Config{Access: pinned, Program: program{}}).Workspace(plan())
	if err != nil {
		t.Fatalf("Workspace() = %v", err)
	}
	if string(setup.Project.Name) != pinned.Project {
		t.Errorf("the workspace's project is %q, want the %q the provider pinned", setup.Project.Name, pinned.Project)
	}
}

func TestWorkspaceRefusesAnAccessThatWouldWriteStateUnsealed(t *testing.T) {
	t.Parallel()

	for name, broken := range map[string]pulumi.Access{
		"no backend":    {Passphrase: "a-passphrase"},
		"no passphrase": {BackendURL: "s3://ocel-state/shop"},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := pulumi.New(pulumi.Config{Access: broken, Program: program{}}).Workspace(plan())
			var refusal providerkit.Refusal
			if !errors.As(err, &refusal) || refusal.Code != providerkit.CodeNotReady {
				t.Fatalf("Workspace() with %s = %v, want a not-ready refusal", name, err)
			}
		})
	}
}

func TestWorkspaceRefusesAnAdapterCarryingNoProgram(t *testing.T) {
	t.Parallel()

	if _, err := pulumi.New(pulumi.Config{Access: access()}).Workspace(plan()); err == nil {
		t.Fatal("Workspace() with no program succeeded, want a refusal: the engine would have nothing to run")
	}
}

func TestStackTakesTheConfigTheProgramAsksFor(t *testing.T) {
	t.Parallel()

	wanted := auto.ConfigMap{"aws:region": auto.ConfigValue{Value: "nowhere"}}
	config, err := pulumi.New(pulumi.Config{
		Access:  access(),
		Program: configuring{program{config: wanted}},
	}).Stack(context.Background(), plan())
	if err != nil {
		t.Fatalf("Stack() = %v", err)
	}
	if config["aws:region"].Value != "nowhere" {
		t.Errorf("the stack's config is %v, want what the program's Configurer asked for", config)
	}
}

func TestStackOfAProgramThatConfiguresNothingIsEmpty(t *testing.T) {
	t.Parallel()

	config, err := pulumi.New(pulumi.Config{Access: access(), Program: program{}}).Stack(context.Background(), plan())
	if err != nil {
		t.Fatalf("Stack() = %v", err)
	}
	if len(config) != 0 {
		t.Errorf("the stack's config is %v, want none for a program that configures nothing", config)
	}
}

func TestRunHandsTheEngineTheWorkspaceAndDecodesWhatItAnswers(t *testing.T) {
	t.Parallel()

	engine := &recordingEngine{outputs: auto.OutputMap{"bucket": auto.OutputValue{Value: "shop-uploads"}}}
	result, err := pulumi.New(pulumi.Config{
		Access:  access(),
		Program: decoding{},
		Engine:  engine,
	}).Run(context.Background(), plan(), nil)
	if err != nil {
		t.Fatalf("Run() = %v", err)
	}
	if engine.up.Stack != plan().Ref.Name.String() {
		t.Errorf("the engine was asked to stand up %q, want %q", engine.up.Stack, plan().Ref.Name)
	}
	if engine.up.Parallel != pulumi.DefaultParallel {
		t.Errorf("the engine ran at parallelism %d, want the adapter's %d", engine.up.Parallel, pulumi.DefaultParallel)
	}
	if len(result.Links) != 1 || result.Links[0].Properties["bucket"] != "shop-uploads" {
		t.Errorf("Run() = %+v, want the link the program decoded from the stack's outputs", result)
	}
}

func TestRunTakesTheParallelismTheProviderPins(t *testing.T) {
	t.Parallel()

	engine := &recordingEngine{}
	if _, err := pulumi.New(pulumi.Config{
		Access:   access(),
		Program:  program{},
		Engine:   engine,
		Parallel: 8,
	}).Run(context.Background(), plan(), nil); err != nil {
		t.Fatalf("Run() = %v", err)
	}
	if engine.up.Parallel != 8 {
		t.Errorf("the engine ran at parallelism %d, want the 8 the provider pinned", engine.up.Parallel)
	}
}

func TestRunRefreshesOnlyTheStacksTheProviderSaysToRefresh(t *testing.T) {
	t.Parallel()

	engine := &recordingEngine{}
	adapter := pulumi.New(pulumi.Config{
		Access:  access(),
		Program: program{},
		Engine:  engine,
		Refresh: func(ref providerkit.StackRef, _ pulumi.Op) bool { return ref.Name.Env == "prod" },
	})
	if _, err := adapter.Run(context.Background(), plan(), nil); err != nil {
		t.Fatalf("Run() = %v", err)
	}
	if !engine.up.Refresh {
		t.Error("the engine ran without a refresh over a stack the provider said to refresh")
	}

	staging := plan()
	staging.Ref.Name = naming.InfraStack("staging")
	if _, err := adapter.Run(context.Background(), staging, nil); err != nil {
		t.Fatalf("Run() = %v", err)
	}
	if engine.up.Refresh {
		t.Error("the engine refreshed a stack the provider did not ask it to, and a refresh costs a full read of the account")
	}
}

func TestRunCarriesTheProgramsConfigToTheEngine(t *testing.T) {
	t.Parallel()

	engine := &recordingEngine{}
	wanted := auto.ConfigMap{"aws:defaultTags": auto.ConfigValue{Value: `{"tags":{"ocel:project":"shop"}}`}}
	if _, err := pulumi.New(pulumi.Config{
		Access:  access(),
		Program: configuring{program{config: wanted}},
		Engine:  engine,
	}).Run(context.Background(), plan(), nil); err != nil {
		t.Fatalf("Run() = %v", err)
	}
	if engine.up.Config["aws:defaultTags"].Value != wanted["aws:defaultTags"].Value {
		t.Errorf("the engine was configured with %v, want what the program's Configurer asked for", engine.up.Config)
	}
}

func TestALockedStackReadsAsBusySoTheCLISaysToWaitRatherThanToRetry(t *testing.T) {
	t.Parallel()

	engine := &recordingEngine{err: errors.New("update failed: the stack is currently locked by 1 lock(s)")}
	_, err := pulumi.New(pulumi.Config{Access: access(), Program: program{}, Engine: engine}).
		Run(context.Background(), plan(), nil)
	var refusal providerkit.Refusal
	if !errors.As(err, &refusal) || refusal.Code != providerkit.CodeBusy {
		t.Fatalf("Run() over a locked stack = %v, want a busy refusal", err)
	}
	if !strings.Contains(refusal.Message, "pulumi cancel --stack "+plan().Ref.Name.String()) {
		t.Errorf("the refusal reads %q, want it to name the command that releases the lock", refusal.Message)
	}
}

func TestDestroyHandsTheEngineTheSameWorkspaceRunDoes(t *testing.T) {
	t.Parallel()

	engine := &recordingEngine{}
	if err := pulumi.New(pulumi.Config{Access: access(), Program: program{}, Engine: engine}).
		Destroy(context.Background(), plan().Ref, nil); err != nil {
		t.Fatalf("Destroy() = %v", err)
	}
	if engine.down.Stack != plan().Ref.Name.String() {
		t.Errorf("the engine was asked to take down %q, want %q", engine.down.Stack, plan().Ref.Name)
	}
	if engine.down.Project.Backend == nil || engine.down.Project.Backend.URL != access().BackendURL {
		t.Errorf("the teardown points at %+v, want the backend the provider named", engine.down.Project.Backend)
	}
}

func TestDestroyOfALockedStackReadsAsBusyToo(t *testing.T) {
	t.Parallel()

	engine := &recordingEngine{err: errors.New("destroy failed: the stack is currently locked by 1 lock(s)")}
	err := pulumi.New(pulumi.Config{Access: access(), Program: program{}, Engine: engine}).
		Destroy(context.Background(), plan().Ref, nil)
	var refusal providerkit.Refusal
	if !errors.As(err, &refusal) || refusal.Code != providerkit.CodeBusy {
		t.Fatalf("Destroy() over a locked stack = %v, want a busy refusal", err)
	}
}

func TestDecodeReadsAStacksOutputsIntoTheVendorsOwnType(t *testing.T) {
	t.Parallel()

	type outputs struct {
		Bucket string `json:"bucket"`
		Port   int    `json:"port"`
	}
	decoded, err := pulumi.Decode[outputs](auto.OutputMap{
		"bucket": auto.OutputValue{Value: "shop-uploads"},
		"port":   auto.OutputValue{Value: 5432},
	})
	if err != nil {
		t.Fatalf("Decode() = %v", err)
	}
	if decoded.Bucket != "shop-uploads" || decoded.Port != 5432 {
		t.Errorf("Decode() = %+v, want the outputs the stack answered with", decoded)
	}
}
