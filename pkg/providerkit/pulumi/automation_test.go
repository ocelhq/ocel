package pulumi_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/pulumi/pulumi/sdk/v3/go/auto"
	sdk "github.com/pulumi/pulumi/sdk/v3/go/pulumi"

	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/providerkit/provider"
	"github.com/ocelhq/ocel/pkg/providerkit/pulumi"
	"github.com/ocelhq/ocel/pkg/providerkit/refusal"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

type program struct{ config auto.ConfigMap }

func (program) Run(*sdk.Context, provider.StackSpec) error { return nil }

type configuring struct{ program }

func (c configuring) Configure(context.Context, provider.StackSpec) (auto.ConfigMap, error) {
	return c.config, nil
}

func backend() pulumi.Backend {
	return pulumi.Backend{
		URL:        "s3://ocel-state/shop",
		Passphrase: "a-passphrase",
		Env:        map[string]string{"VENDOR_REGION": "nowhere"},
	}
}

func spec() provider.StackSpec {
	return provider.StackSpec{
		Ref: provider.StackRef{
			Project: "shop",
			Class:   edge.ClassProduction,
			Name:    naming.InfraStack("prod"),
		},
		Kind: provider.StackInfra,
	}
}

func row(action provider.ChangeAction, kind, name string) provider.Change {
	return provider.Change{Kind: kind, Name: name, Action: action}
}

func TestPreviewGathersWhatTheEngineWouldDoIntoOneGroup(t *testing.T) {
	t.Parallel()

	engine := &recordingEngine{rows: []provider.Change{
		row(provider.ActionCreate, "Cluster", "orders"),
		row(provider.ActionUpdate, "Bucket", "uploads"),
		row(provider.ActionDelete, "Bucket", "exports"),
		row(provider.ActionKeep, "Role", "app"),
		row(provider.ActionReplace, "Instance", "reporting"),
	}}

	produced, err := pulumi.New(pulumi.Config{Backend: backend(), Program: program{}.Run, Engine: engine}).
		Preview(context.Background(), spec(), nil)
	if err != nil {
		t.Fatalf("Preview() = %v", err)
	}
	if engine.previewed != pulumi.OperationProvision {
		t.Errorf("the engine was asked to preview %q, want the provision it mirrors", engine.previewed)
	}
	if len(produced.Groups) != 1 {
		t.Fatalf("Preview() returned %d groups, want the one stack it previews", len(produced.Groups))
	}
	rows := map[string]provider.ChangeAction{}
	for _, change := range produced.Groups[0].Changes {
		rows[change.Name] = change.Action
	}
	want := map[string]provider.ChangeAction{
		"orders":    provider.ActionCreate,
		"uploads":   provider.ActionUpdate,
		"exports":   provider.ActionDelete,
		"app":       provider.ActionKeep,
		"reporting": provider.ActionReplace,
	}
	for name, action := range want {
		if rows[name] != action {
			t.Errorf("%s reads %q, want %q", name, rows[name], action)
		}
	}
	if produced.Groups[0].Name != naming.InfraStack("prod").String() {
		t.Errorf("the group is named %q, want the stack the engine previewed", produced.Groups[0].Name)
	}
}

func TestPreviewDestroyShowsWhatTheTeardownWouldTakeDown(t *testing.T) {
	t.Parallel()

	engine := &recordingEngine{rows: []provider.Change{row(provider.ActionDelete, "Bucket", "uploads")}}

	produced, err := pulumi.New(pulumi.Config{Backend: backend(), Program: program{}.Run, Engine: engine}).
		PreviewDestroy(context.Background(), spec().Ref, nil)
	if err != nil {
		t.Fatalf("PreviewDestroy() = %v", err)
	}
	if engine.previewed != pulumi.OperationDestroy {
		t.Errorf("the engine was asked to preview %q, want the destroy it mirrors", engine.previewed)
	}
	if len(produced.Groups) != 1 || produced.Groups[0].Action != provider.ActionDelete {
		t.Fatalf("PreviewDestroy() = %+v, want the stack shown as going", produced.Groups)
	}
}

func TestWorkspaceUsesTheBackendTheProviderNamed(t *testing.T) {
	t.Parallel()

	setup, err := pulumi.New(pulumi.Config{Backend: backend(), Program: program{}.Run}).Workspace(spec())
	if err != nil {
		t.Fatalf("Workspace() = %v", err)
	}
	if setup.Project.Backend == nil || setup.Project.Backend.URL != backend().URL {
		t.Fatalf("the workspace points at %+v, want the backend the provider named", setup.Project.Backend)
	}
	if setup.Project.Runtime.Name() != "go" {
		t.Errorf("the workspace runs %q, want the go runtime a kit provider's program is written in", setup.Project.Runtime.Name())
	}
	if string(setup.Project.Name) != naming.PulumiProject("shop") {
		t.Errorf("the workspace's project is %q, want %q", setup.Project.Name, naming.PulumiProject("shop"))
	}
	if len(setup.Options) == 0 {
		t.Error("the workspace has no options, so nothing configures a local workspace from it")
	}
}

func TestWorkspacePassesThePassphraseAndTheVendorsOwnEnvironment(t *testing.T) {
	t.Parallel()

	setup, err := pulumi.New(pulumi.Config{Backend: backend(), Program: program{}.Run}).Workspace(spec())
	if err != nil {
		t.Fatalf("Workspace() = %v", err)
	}
	if setup.EnvVars["PULUMI_CONFIG_PASSPHRASE"] != "a-passphrase" {
		t.Error("the workspace does not include the passphrase, so the state it writes would be unsealed")
	}
	if setup.EnvVars["VENDOR_REGION"] != "nowhere" {
		t.Errorf("the workspace's environment is %v, want the vendor's own variables passed through", setup.EnvVars)
	}
}

func TestWorkspaceTakesTheProjectNameTheProviderPins(t *testing.T) {
	t.Parallel()

	pinned := backend()
	pinned.Project = "ocel-shop-pinned"
	setup, err := pulumi.New(pulumi.Config{Backend: pinned, Program: program{}.Run}).Workspace(spec())
	if err != nil {
		t.Fatalf("Workspace() = %v", err)
	}
	if string(setup.Project.Name) != pinned.Project {
		t.Errorf("the workspace's project is %q, want the %q the provider pinned", setup.Project.Name, pinned.Project)
	}
}

func TestWorkspaceRefusesAnAccessThatWouldWriteStateUnsealed(t *testing.T) {
	t.Parallel()

	for name, broken := range map[string]pulumi.Backend{
		"no backend":    {Passphrase: "a-passphrase"},
		"no passphrase": {URL: "s3://ocel-state/shop"},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := pulumi.New(pulumi.Config{Backend: broken, Program: program{}.Run}).Workspace(spec())
			var refused refusal.Refusal
			if !errors.As(err, &refused) || refused.Code != refusal.CodeNotReady {
				t.Fatalf("Workspace() with %s = %v, want a not-ready refusal", name, err)
			}
		})
	}
}

func TestWorkspaceRefusesAnAutomationWithNoProgram(t *testing.T) {
	t.Parallel()

	if _, err := pulumi.New(pulumi.Config{Backend: backend()}).Workspace(spec()); err == nil {
		t.Fatal("Workspace() with no program succeeded, want a refusal: the engine would have nothing to run")
	}
}

func TestStackTakesTheConfigTheProgramAsksFor(t *testing.T) {
	t.Parallel()

	wanted := auto.ConfigMap{"aws:region": auto.ConfigValue{Value: "nowhere"}}
	config, err := pulumi.New(pulumi.Config{
		Backend:   backend(),
		Program:   program{}.Run,
		Configure: configuring{program{config: wanted}}.Configure,
	}).StackConfig(context.Background(), spec())
	if err != nil {
		t.Fatalf("Stack() = %v", err)
	}
	if config["aws:region"].Value != "nowhere" {
		t.Errorf("the stack's config is %v, want what the config's Configure asked for", config)
	}
}

func TestStackOfAProgramThatConfiguresNothingIsEmpty(t *testing.T) {
	t.Parallel()

	config, err := pulumi.New(pulumi.Config{Backend: backend(), Program: program{}.Run}).StackConfig(context.Background(), spec())
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
		Backend: backend(),
		Program: program{}.Run,
		Decode:  decoding{}.Decode,
		Engine:  engine,
	}).Run(context.Background(), spec(), nil)
	if err != nil {
		t.Fatalf("Run() = %v", err)
	}
	if engine.up.Stack != spec().Ref.Name.String() {
		t.Errorf("the engine was asked to provision %q, want %q", engine.up.Stack, spec().Ref.Name)
	}
	if engine.up.Parallel != pulumi.DefaultParallel {
		t.Errorf("the engine ran at parallelism %d, want the automation's %d", engine.up.Parallel, pulumi.DefaultParallel)
	}
	if len(result.Bindings) != 1 || result.Bindings[0].Properties["bucket"] != "shop-uploads" {
		t.Errorf("Run() = %+v, want the binding the program decoded from the stack's outputs", result)
	}
}

func TestRunTakesTheParallelismTheProviderPins(t *testing.T) {
	t.Parallel()

	engine := &recordingEngine{}
	if _, err := pulumi.New(pulumi.Config{
		Backend:  backend(),
		Program:  program{}.Run,
		Engine:   engine,
		Parallel: 8,
	}).Run(context.Background(), spec(), nil); err != nil {
		t.Fatalf("Run() = %v", err)
	}
	if engine.up.Parallel != 8 {
		t.Errorf("the engine ran at parallelism %d, want the 8 the provider pinned", engine.up.Parallel)
	}
}

func TestRunRefreshesOnlyTheStacksTheProviderSaysToRefresh(t *testing.T) {
	t.Parallel()

	engine := &recordingEngine{}
	automation := pulumi.New(pulumi.Config{
		Backend: backend(),
		Program: program{}.Run,
		Engine:  engine,
		Refresh: func(ref provider.StackRef, _ pulumi.Operation) bool { return ref.Name.Env == "prod" },
	})
	if _, err := automation.Run(context.Background(), spec(), nil); err != nil {
		t.Fatalf("Run() = %v", err)
	}
	if !engine.up.Refresh {
		t.Error("the engine ran without a refresh over a stack the provider said to refresh")
	}

	staging := spec()
	staging.Ref.Name = naming.InfraStack("staging")
	if _, err := automation.Run(context.Background(), staging, nil); err != nil {
		t.Fatalf("Run() = %v", err)
	}
	if engine.up.Refresh {
		t.Error("the engine refreshed a stack the provider did not ask it to, and a refresh costs a full read of the account")
	}
}

func TestRunPassesTheProgramsConfigToTheEngine(t *testing.T) {
	t.Parallel()

	engine := &recordingEngine{}
	wanted := auto.ConfigMap{"aws:defaultTags": auto.ConfigValue{Value: `{"tags":{"ocel:project":"shop"}}`}}
	if _, err := pulumi.New(pulumi.Config{
		Backend:   backend(),
		Program:   program{}.Run,
		Configure: configuring{program{config: wanted}}.Configure,
		Engine:    engine,
	}).Run(context.Background(), spec(), nil); err != nil {
		t.Fatalf("Run() = %v", err)
	}
	if engine.up.Config["aws:defaultTags"].Value != wanted["aws:defaultTags"].Value {
		t.Errorf("the engine was configured with %v, want what the config's Configure asked for", engine.up.Config)
	}
}

func TestALockedStackReadsAsBusySoTheCLISaysToWaitRatherThanToRetry(t *testing.T) {
	t.Parallel()

	engine := &recordingEngine{err: errors.New("update failed: the stack is currently locked by 1 lock(s)")}
	_, err := pulumi.New(pulumi.Config{Backend: backend(), Program: program{}.Run, Engine: engine}).
		Run(context.Background(), spec(), nil)
	var refused refusal.Refusal
	if !errors.As(err, &refused) || refused.Code != refusal.CodeBusy {
		t.Fatalf("Run() over a locked stack = %v, want a busy refusal", err)
	}
	if !strings.Contains(refused.Message, "pulumi cancel --stack "+spec().Ref.Name.String()) {
		t.Errorf("the refusal reads %q, want it to name the command that releases the lock", refused.Message)
	}
}

func TestDestroyHandsTheEngineTheSameWorkspaceRunDoes(t *testing.T) {
	t.Parallel()

	engine := &recordingEngine{}
	if err := pulumi.New(pulumi.Config{Backend: backend(), Program: program{}.Run, Engine: engine}).
		Destroy(context.Background(), spec().Ref, nil); err != nil {
		t.Fatalf("Destroy() = %v", err)
	}
	if engine.down.Stack != spec().Ref.Name.String() {
		t.Errorf("the engine was asked to take down %q, want %q", engine.down.Stack, spec().Ref.Name)
	}
	if engine.down.Project.Backend == nil || engine.down.Project.Backend.URL != backend().URL {
		t.Errorf("the teardown points at %+v, want the backend the provider named", engine.down.Project.Backend)
	}
}

func TestDestroyOfALockedStackReadsAsBusyToo(t *testing.T) {
	t.Parallel()

	engine := &recordingEngine{err: errors.New("destroy failed: the stack is currently locked by 1 lock(s)")}
	err := pulumi.New(pulumi.Config{Backend: backend(), Program: program{}.Run, Engine: engine}).
		Destroy(context.Background(), spec().Ref, nil)
	var refused refusal.Refusal
	if !errors.As(err, &refused) || refused.Code != refusal.CodeBusy {
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
