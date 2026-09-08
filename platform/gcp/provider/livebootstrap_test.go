package gcp_test

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"os"
	"strings"
	"testing"

	"cloud.google.com/go/storage"
	"google.golang.org/api/secretmanager/v1"
	"google.golang.org/api/serviceusage/v1"

	"github.com/ocelhq/ocel/pkg/providerkit"
	"github.com/ocelhq/ocel/pkg/providerkit/conformance"
	gcp "github.com/ocelhq/ocel/platform/gcp/provider"
)

func endpoint() string { return os.Getenv("OCEL_FLOCI_GCP_ENDPOINT") }

func servicesEnabled(t *testing.T) {
	t.Helper()

	ctx := context.Background()
	service, err := serviceusage.NewService(ctx, gcp.EmulatorREST(endpoint())...)
	if err != nil {
		t.Fatalf("reach the emulator's service usage API: %v", err)
	}
	for _, api := range gcp.BootstrapAPIs {
		name := "projects/" + liveProject + "/services/" + api
		if _, err := service.Services.Enable(name, &serviceusage.EnableServiceRequest{}).Context(ctx).Do(); err != nil {
			t.Fatalf("enable %s: %v", api, err)
		}
	}
}

func bootstrapperOf(t *testing.T, p *gcp.Provider) providerkit.Bootstrapper {
	t.Helper()
	bootstrapper, err := p.Bootstrap(p.Edges().Default())
	if err != nil {
		t.Fatalf("Bootstrap() = %v", err)
	}
	return bootstrapper
}

func bootstrapped(t *testing.T, p *gcp.Provider, class providerkit.Class) providerkit.Bootstrapper {
	t.Helper()

	servicesEnabled(t)
	ctx := context.Background()
	bootstrapper := bootstrapperOf(t, p)
	if err := bootstrapper.Apply(ctx, providerkit.BootstrapRequest{Class: class, Writer: "live-suite"}, nil); err != nil {
		t.Fatalf("Apply(%s) = %v, want the resources every port beneath it reads and writes", class, err)
	}
	t.Cleanup(func() {
		if err := bootstrapper.Remove(ctx, class, nil); err != nil {
			t.Errorf("Remove(%s) = %v", class, err)
		}
	})
	return bootstrapper
}

func TestLiveBootstrapper(t *testing.T) {
	p := live(t)
	servicesEnabled(t)

	conformance.RunBootstrapper(t, bootstrapperOf(t, p))
}

func TestLiveTheBootstrapStandsUpTheStackTheDataPortsRead(t *testing.T) {
	p := live(t)
	class := providerkit.ClassProduction
	bootstrapped(t, p, providerkit.ClassPreview)
	bootstrapper := bootstrapped(t, p, class)

	ctx := context.Background()
	described, err := bootstrapper.Describe(ctx, class)
	if err != nil {
		t.Fatalf("Describe() after Apply() = %v", err)
	}
	if !described.Present || described.Unfinished {
		t.Fatalf("Describe() = %+v, want a bootstrap that stands and finished", described)
	}
	if len(described.Stacks) != 1 || !described.Stacks[0].DigestCurrent {
		t.Fatalf("Describe().Stacks = %+v, want one stack carrying the item list Apply wrote", described.Stacks)
	}
	if described.Stacks[0].Writer != "live-suite" {
		t.Errorf("Describe().Stacks[0].Writer = %q, want the writer the apply recorded", described.Stacks[0].Writer)
	}

	t.Run("Sealer", func(t *testing.T) { conformance.RunSealer(t, p.Sealer()) })
	t.Run("ArtifactStore", func(t *testing.T) { conformance.RunArtifactStore(t, p.Artifacts()) })
	t.Run("RecordStore", func(t *testing.T) { conformance.RunRecordStore(t, p.Records()) })
}

func TestLiveAPlanDrawnAfterAnApplyKeepsEverythingItStoodUp(t *testing.T) {
	p := live(t)
	class := providerkit.ClassPreview
	bootstrapper := bootstrapped(t, p, class)

	plan, err := bootstrapper.Plan(context.Background(), providerkit.BootstrapRequest{Class: class})
	if err != nil {
		t.Fatalf("Plan() after Apply() = %v", err)
	}
	rows := 0
	for _, group := range plan.Groups {
		if group.Action != providerkit.ActionKeep {
			t.Errorf("Plan() shows %s as %q after an apply, want it kept: a second apply must be a no-op", group.Name, group.Action)
		}
		for _, change := range group.Changes {
			rows++
			if change.Action != providerkit.ActionKeep {
				t.Errorf("Plan() shows %s %s as %q after an apply, want it kept", change.Kind, change.Name, change.Action)
			}
		}
	}
	if rows == 0 {
		t.Fatal("Plan() showed no rows at all, and a plan with nothing in it consents to nothing")
	}
}

func TestLiveAPlanNamesEveryResourceTheStackIsMadeOf(t *testing.T) {
	p := live(t)
	servicesEnabled(t)
	class := providerkit.ClassProduction

	plan, err := bootstrapperOf(t, p).Plan(context.Background(), providerkit.BootstrapRequest{Class: class})
	if err != nil {
		t.Fatalf("Plan() = %v", err)
	}

	kinds := map[string]string{}
	groups := map[string]string{}
	for _, group := range plan.Groups {
		if !strings.HasPrefix(group.Name, string(gcp.Vendor)+"/") {
			t.Errorf("Plan() returned the group %q, want every group named under the vendor that stands it up", group.Name)
		}
		for _, change := range group.Changes {
			kinds[change.Kind+"/"+change.Name] = string(change.Action)
			groups[change.Kind+"/"+change.Name] = group.Kind
		}
	}
	for row, group := range map[string]string{
		"firestore:database/ocel":                                   providerkit.StackGroupKind,
		"storage:bucket/" + gcp.BucketName(liveProject, class):      providerkit.StackGroupKind,
		"storage:bucket/" + gcp.StateBucketName(liveProject, class): providerkit.StackGroupKind,
		"kms:keyring/" + gcp.KeyRing:                                providerkit.StackGroupKind,
		"kms:key/" + string(class):                                  providerkit.StackGroupKind,
		"secretmanager:secret/" + gcp.PassphraseSecret(class):       providerkit.ParameterGroupKind,
	} {
		if _, shown := kinds[row]; !shown {
			t.Errorf("Plan() shows no row for %s, and what the plan does not show, the apply may not do", row)
			continue
		}
		if groups[row] != group {
			t.Errorf("Plan() shows %s under a %q group, want it under %q", row, groups[row], group)
		}
	}
}

func TestLiveAPlanIsRefusedWhileAnApiTheStackNeedsIsOff(t *testing.T) {
	p := live(t)
	servicesDisabled(t, "cloudkms.googleapis.com")

	var refusal providerkit.Refusal
	_, err := bootstrapperOf(t, p).Plan(context.Background(), providerkit.BootstrapRequest{Class: providerkit.ClassProduction})
	if !errors.As(err, &refusal) || refusal.Code != providerkit.CodeNotReady {
		t.Fatalf("Plan() with an API off = %v, want a %s refusal: enabling it is a precondition, not a row in the stack", err, providerkit.CodeNotReady)
	}
	if !strings.Contains(refusal.Message, "cloudkms.googleapis.com") {
		t.Errorf("Plan() refused with %q, want it to name the API that is off", refusal.Message)
	}
}

func servicesDisabled(t *testing.T, api string) {
	t.Helper()

	ctx := context.Background()
	service, err := serviceusage.NewService(ctx, gcp.EmulatorREST(endpoint())...)
	if err != nil {
		t.Fatalf("reach the emulator's service usage API: %v", err)
	}
	name := "projects/" + liveProject + "/services/" + api
	if _, err := service.Services.Disable(name, &serviceusage.DisableServiceRequest{}).Context(ctx).Do(); err != nil {
		t.Fatalf("disable %s: %v", api, err)
	}
	t.Cleanup(func() {
		if _, err := service.Services.Enable(name, &serviceusage.EnableServiceRequest{}).Context(ctx).Do(); err != nil {
			t.Errorf("enable %s again: %v", api, err)
		}
	})
}

func TestLiveAnApplyThatNeverFinishedReadsAsUnfinishedAndAReApplyFinishesIt(t *testing.T) {
	p := live(t)
	servicesEnabled(t)
	class := providerkit.ClassPreview
	bootstrapper := bootstrapperOf(t, p)

	ctx := context.Background()
	if err := bootstrapper.Apply(ctx, providerkit.BootstrapRequest{Class: class, Writer: "live-suite"}, nil); err != nil {
		t.Fatalf("Apply() = %v", err)
	}
	t.Cleanup(func() {
		if err := bootstrapper.Remove(ctx, class, nil); err != nil {
			t.Errorf("Remove() = %v", err)
		}
	})

	interrupt(t, p, class)

	described, err := bootstrapper.Describe(ctx, class)
	if err != nil {
		t.Fatalf("Describe() = %v", err)
	}
	if !described.Unfinished {
		t.Fatal("Describe() reads an apply that stopped half way as finished, and nothing would ever go back to finish it")
	}

	if err := bootstrapper.Apply(ctx, providerkit.BootstrapRequest{Class: class, Writer: "live-suite"}, nil); err != nil {
		t.Fatalf("Apply() over an unfinished bootstrap = %v, want it finished", err)
	}
	healed, err := bootstrapper.Describe(ctx, class)
	if err != nil {
		t.Fatalf("Describe() after the second Apply() = %v", err)
	}
	if healed.Unfinished || !healed.Present {
		t.Fatalf("Describe() = %+v after a re-apply, want a bootstrap that stands and finished", healed)
	}
}

func interrupt(t *testing.T, p *gcp.Provider, class providerkit.Class) {
	t.Helper()

	ctx := context.Background()
	client, err := storage.NewClient(ctx, gcp.EmulatorStorage(endpoint())...)
	if err != nil {
		t.Fatalf("reach the emulator's object store: %v", err)
	}
	t.Cleanup(func() { client.Close() })

	writer := client.Bucket(gcp.BucketName(liveProject, class)).Object(gcp.StampObject).NewWriter(ctx)
	if _, err := writer.Write([]byte(`{"schema":1,"state":"applying","writer":"live-suite","digest":"halfway"}`)); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestLiveAStateBucketHoldingAStackIsNotSweptOutFromUnderIt(t *testing.T) {
	p := live(t)
	class := providerkit.ClassPreview
	bootstrapper := bootstrapped(t, p, class)

	ctx := context.Background()
	client, err := storage.NewClient(ctx, gcp.EmulatorStorage(endpoint())...)
	if err != nil {
		t.Fatalf("reach the emulator's object store: %v", err)
	}
	defer client.Close()

	object := client.Bucket(gcp.StateBucketName(liveProject, class)).Object(".pulumi/stacks/ocel/shop.json")
	writer := object.NewWriter(ctx)
	if _, err := writer.Write([]byte("{}")); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}

	var refusal providerkit.Refusal
	err = bootstrapper.Remove(ctx, class, nil)
	if !errors.As(err, &refusal) || refusal.Code != providerkit.CodeNotReady {
		t.Fatalf("Remove() with a stack still in the state bucket = %v, want a %s refusal", err, providerkit.CodeNotReady)
	}
	if !strings.Contains(refusal.Message, "shop") {
		t.Errorf("Remove() refused with %q, want it to name the stack still standing", refusal.Message)
	}
	if err := object.Delete(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestLiveASharedResourceStaysWhileTheSiblingClassStands(t *testing.T) {
	p := live(t)
	servicesEnabled(t)
	bootstrapped(t, p, providerkit.ClassProduction)
	bootstrapper := bootstrapped(t, p, providerkit.ClassPreview)

	removal, err := bootstrapper.PlanRemoval(context.Background(), providerkit.ClassPreview)
	if err != nil {
		t.Fatalf("PlanRemoval() = %v", err)
	}
	kept := map[string]providerkit.Change{}
	for _, group := range removal.Groups {
		for _, change := range group.Changes {
			kept[change.Kind+"/"+change.Name] = change
		}
	}
	ring := kept["kms:keyring/"+gcp.KeyRing]
	if ring.Action != providerkit.ActionKeep || ring.Reason == "" {
		t.Errorf("PlanRemoval() shows the key ring as %q (%q), want it kept with a reason: Google never deletes a key ring", ring.Action, ring.Reason)
	}
	database := kept["firestore:database/ocel"]
	if database.Action != providerkit.ActionKeep || !strings.Contains(database.Reason, string(providerkit.ClassProduction)) {
		t.Errorf("PlanRemoval() shows the database as %q (%q), want it kept with a reason naming the sibling class still standing",
			database.Action, database.Reason)
	}
}

func TestLiveThePassphraseIsMintedOnceAndNotWrittenOverAgain(t *testing.T) {
	p := live(t)
	class := providerkit.ClassProduction
	bootstrapper := bootstrapped(t, p, class)

	ctx := context.Background()
	minted := passphraseHeld(t, class)
	if len(minted) == 0 {
		t.Fatal("the bootstrap minted an empty passphrase, and every Pulumi stack in this project is encrypted under it")
	}
	if err := bootstrapper.Apply(ctx, providerkit.BootstrapRequest{Class: class, Writer: "live-suite"}, nil); err != nil {
		t.Fatalf("Apply() a second time = %v", err)
	}
	if again := passphraseHeld(t, class); !bytes.Equal(minted, again) {
		t.Fatal("a second apply wrote a new passphrase, and nothing opens the state the first one encrypted")
	}
}

func passphraseHeld(t *testing.T, class providerkit.Class) []byte {
	t.Helper()

	ctx := context.Background()
	service, err := secretmanager.NewService(ctx, gcp.EmulatorREST(endpoint())...)
	if err != nil {
		t.Fatalf("reach the emulator's secret manager: %v", err)
	}
	name := "projects/" + liveProject + "/secrets/" + gcp.PassphraseSecret(class) + "/versions/latest"
	held, err := service.Projects.Secrets.Versions.Access(name).Context(ctx).Do()
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	decoded, err := base64.StdEncoding.DecodeString(held.Payload.Data)
	if err != nil {
		t.Fatalf("decode the passphrase: %v", err)
	}
	return decoded
}
