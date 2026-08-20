package deploy

import (
	"context"
	"errors"
	"testing"

	"github.com/ocelhq/ocel/pkg/naming"
	deploymentsv1 "github.com/ocelhq/ocel/pkg/proto/deployments/v1"
	cloudflare "github.com/ocelhq/ocel/platform/edge/cloudflare/deploy"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

func phaseConfig(t *testing.T, ed *recordingEdge, up *fakeUploader) Config {
	t.Helper()
	return Config{
		Edge:           ed,
		StoreEndpoint:  fakeStoreEndpoint,
		Slug:           "shop",
		Env:            ProductionEnv,
		Class:          deploymentsv1.Environment_CLASS_PRODUCTION,
		ArtifactRoot:   twoAppTree(t),
		ArtifactBucket: "artifacts",
		AssetBucket:    "assets",
		StateTable:     "ocel-state",
		StateTableARN:  fixtureStateTableARN,
		Uploader:       up,
		Stacks:         &fakeStackIndex{},
	}
}

func TestPlanDeployHandsOnEveryValueTheNextPhasesRead(t *testing.T) {
	t.Parallel()

	current := edge.StoreSchemaVersion
	ed := &recordingEdge{kind: cloudflare.Kind, storeSchemaVersion: &current}
	cfg := phaseConfig(t, ed, &fakeUploader{exists: map[string]bool{}})
	manifest := deployedManifest(twoAppManifest())

	plan, err := planDeploy(context.Background(), cfg, manifest, nil)
	if err != nil {
		t.Fatalf("planDeploy: %v", err)
	}
	if !plan.ready {
		t.Fatal("a plan that returned no error is not ready, so every phase after it would refuse")
	}
	if len(plan.apps) != 2 {
		t.Errorf("apps = %d, want the manifest's two", len(plan.apps))
	}
	if plan.sessions.KeyPrefix == "" {
		t.Error("sessions carry no key prefix, so the membrane would write outside this deploy's scope")
	}
	for _, app := range []string{"web", "admin"} {
		if _, ok := plan.builds.identities[app]; !ok {
			t.Errorf("builds carry no identity for %s", app)
		}
	}
}

func TestUploadRefusesAPlanItWasNeverHanded(t *testing.T) {
	t.Parallel()

	up := &fakeUploader{exists: map[string]bool{}}
	ed := &recordingEdge{kind: cloudflare.Kind}
	cfg := phaseConfig(t, ed, up)

	_, err := uploadArtifacts(context.Background(), cfg, deployedManifest(twoAppManifest()), deployPlan{}, nil)

	var phase *PhaseError
	if !errors.As(err, &phase) {
		t.Fatalf("uploadArtifacts err = %v (%T), want *PhaseError", err, err)
	}
	if phase.Phase != "upload" || phase.After != "plan" {
		t.Errorf("PhaseError = %+v, want the upload naming the plan it never got", phase)
	}
	if len(up.puts) != 0 {
		t.Errorf("the refused upload put %v; a phase that refuses touches nothing", up.puts)
	}
}

func TestProvisionRefusesWhatAPriorPhaseNeverHandedIt(t *testing.T) {
	t.Parallel()

	manifest := deployedManifest(twoAppManifest())

	t.Run("without a plan", func(t *testing.T) {
		t.Parallel()

		ed := &recordingEdge{kind: cloudflare.Kind}
		cfg := phaseConfig(t, ed, &fakeUploader{exists: map[string]bool{}})

		_, err := provisionDeploy(context.Background(), cfg, &Realized{}, manifest, deployPlan{}, uploadedArtifacts{ready: true}, nil, nil)
		requirePhaseError(t, err, "provision", "plan")
		if len(ed.reconciles) != 0 {
			t.Errorf("the refused provision reconciled %d stacks, want none", len(ed.reconciles))
		}
	})

	t.Run("without the uploads", func(t *testing.T) {
		t.Parallel()

		ed := &recordingEdge{kind: cloudflare.Kind}
		cfg := phaseConfig(t, ed, &fakeUploader{exists: map[string]bool{}})

		_, err := provisionDeploy(context.Background(), cfg, &Realized{}, manifest, deployPlan{ready: true}, uploadedArtifacts{}, nil, nil)
		requirePhaseError(t, err, "provision", "upload")
		if len(ed.reconciles) != 0 {
			t.Errorf("the refused provision reconciled %d stacks, want none", len(ed.reconciles))
		}
	})
}

func TestPromoteRefusesAProvisionThatNeverFinished(t *testing.T) {
	t.Parallel()

	ed := &recordingEdge{kind: cloudflare.Kind}
	cfg := phaseConfig(t, ed, &fakeUploader{exists: map[string]bool{}})
	state := edge.StackState{edge.StackKeySlug: "shop"}

	res, err := promoteDeploy(context.Background(), cfg, deployedManifest(twoAppManifest()), provisionedDeploy{state: state}, nil)

	requirePhaseError(t, err, "promote", "provision")
	if res.StackState[edge.StackKeySlug] != "shop" {
		t.Errorf("StackState = %v, want the state the provision got to before it stopped", res.StackState)
	}
	if len(ed.staged) != 0 {
		t.Errorf("the refused promote staged %d records, want none", len(ed.staged))
	}
}

func requirePhaseError(t *testing.T, err error, phase, after string) {
	t.Helper()
	var got *PhaseError
	if !errors.As(err, &got) {
		t.Fatalf("err = %v (%T), want *PhaseError", err, err)
	}
	if got.Phase != phase || got.After != after {
		t.Errorf("PhaseError = %+v, want phase %q after %q", got, phase, after)
	}
}

func TestReclamationCarriesItsStackTeardown(t *testing.T) {
	t.Parallel()

	stack := naming.AppStack(ProductionEnv, "web", testRelease(t, "b1"))
	r := Reclamation{
		Teardown: Teardown{
			Slug:   "shop",
			Stacks: &fakeStackIndex{},
			Pulumi: PulumiAccess{PulumiProject: "ocel", BackendURL: "file:///state"},
		},
		ISRWriter: ISRWriterAccess{Endpoint: "https://writer.example", BootstrapCred: "cred-1"},
	}

	got := r.forStack(stack)
	if got.Project != "shop" || got.Stack != stack {
		t.Errorf("forStack = %+v, want the reclaimed stack under this project", got)
	}
	if got.Pulumi.PulumiProject != "ocel" || got.Pulumi.BackendURL != "file:///state" {
		t.Errorf("forStack Pulumi = %+v, want the access the caller declared", got.Pulumi)
	}
	if got.Stacks == nil {
		t.Error("forStack dropped the stack index, so the teardown could not forget what it destroyed")
	}
}

func TestProjectTeardownCarriesItsStackTeardown(t *testing.T) {
	t.Parallel()

	infra := naming.InfraStack(ProductionEnv)
	values := &valueRecorder{}
	td := ProjectTeardown{
		Teardown: Teardown{
			Slug:   "shop",
			Env:    ProductionEnv,
			Stacks: &fakeStackIndex{},
			Stores: ObjectStores{AssetBucket: "assets"},
		},
		Values: values,
	}

	got := td.forStack(infra)
	if got.Project != "shop" || got.Stack != infra {
		t.Errorf("forStack = %+v, want the infra stack under this project", got)
	}
	if td.Stores.AssetBucket != "assets" {
		t.Errorf("Stores = %+v, want the buckets the purge sweeps", td.Stores)
	}
}
