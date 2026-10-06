//go:build integration

package gcp_test

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"cloud.google.com/go/storage"
	firestoreadmin "google.golang.org/api/firestore/v1"
	"google.golang.org/api/iam/v1"
	"google.golang.org/api/secretmanager/v1"
	"google.golang.org/api/serviceusage/v1"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/keyvalue"
	"github.com/ocelhq/ocel/pkg/progress"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/conformance"
	"github.com/ocelhq/ocel/pkg/refusal"
	gcp "github.com/ocelhq/ocel/platform/gcp/provider"
	"github.com/ocelhq/ocel/platform/gcp/provider/ports"
)

func endpoint() string { return os.Getenv("OCEL_FLOCI_GCP_ENDPOINT") }

func servicesEnabled(t *testing.T) {
	t.Helper()
	if !emulated() {
		return
	}

	ctx := context.Background()
	service, err := serviceusage.NewService(ctx, ports.EmulatorREST(endpoint())...)
	if err != nil {
		t.Fatalf("reach the emulator's service usage API: %v", err)
	}
	for _, api := range append(slices.Clone(gcp.BootstrapAPIs), gcp.TasksAPIs...) {
		name := "projects/" + liveProject() + "/services/" + api
		if _, err := service.Services.Enable(name, &serviceusage.EnableServiceRequest{}).Context(ctx).Do(); err != nil {
			t.Fatalf("enable %s: %v", api, err)
		}
	}
}

func bootstrapOf(t *testing.T, p *gcp.Provider) provider.Bootstrap {
	t.Helper()
	bootstrap, err := p.Bootstrap(p.Facts().DefaultEdge)
	if err != nil {
		t.Fatalf("Bootstrap() = %v", err)
	}
	return bootstrap
}

type emulatedBootstrap struct{ provider.Bootstrap }

func (b emulatedBootstrap) Catalogue() []provider.Feature {
	return slices.DeleteFunc(b.Bootstrap.Catalogue(), func(f provider.Feature) bool { return f.Name == gcp.KVFeature })
}

func conformingBootstrap(t *testing.T, p *gcp.Provider) provider.Bootstrap {
	t.Helper()
	if emulated() {
		return emulatedBootstrap{bootstrapOf(t, p)}
	}
	return bootstrapOf(t, p)
}

func bootstrapped(t *testing.T, p *gcp.Provider, tier environment.Tier, features ...string) provider.Bootstrap {
	t.Helper()

	servicesEnabled(t)
	ctx := context.Background()
	bootstrap := bootstrapOf(t, p)
	if err := bootstrap.Apply(ctx, provider.BootstrapRequest{Tier: tier, Features: features, WrittenBy: "live-suite"}, nil); err != nil {
		t.Fatalf("Apply(%s) = %v, want the resources every port beneath it reads and writes", tier, err)
	}
	t.Cleanup(func() {
		if err := bootstrap.Remove(ctx, tier, nil); err != nil {
			t.Errorf("Remove(%s) = %v", tier, err)
		}
	})
	return bootstrap
}

func TestLiveBootstrapper(t *testing.T) {
	p := live(t)
	servicesEnabled(t)

	conformance.RunBootstrap(t, conformingBootstrap(t, p), p.Facts().DefaultEdge)
}

func TestLiveTheBootstrapProvisionsTheStackTheDataPortsRead(t *testing.T) {
	p := live(t)
	tier := environment.TierProduction
	bootstrapped(t, p, environment.TierPreview)
	bootstrap := bootstrapped(t, p, tier)

	ctx := context.Background()
	described, err := bootstrap.Describe(ctx, tier)
	if err != nil {
		t.Fatalf("Describe() after Apply() = %v", err)
	}
	if !described.Present || described.Unfinished {
		t.Fatalf("Describe() = %+v, want a bootstrap that is installed and finished", described)
	}
	if len(described.Stacks) != 1 || !described.Stacks[0].DigestCurrent {
		t.Fatalf("Describe().Stacks = %+v, want one stack recording the item list Apply wrote", described.Stacks)
	}
	if described.Stacks[0].WrittenBy != "live-suite" {
		t.Errorf("Describe().Stacks[0].Writer = %q, want the writer the apply recorded", described.Stacks[0].WrittenBy)
	}

	t.Run("Cipher", func(t *testing.T) { conformance.RunCipher(t, p.Cipher()) })
	t.Run("ArtifactStore", func(t *testing.T) { conformance.RunArtifactStore(t, p.Facts(), p.Artifacts()) })
	t.Run("Store", func(t *testing.T) { conformance.RunStore(t, p.KeyValues()) })
}

func TestLiveAPlanDrawnAfterAnApplyKeepsEverythingItProvisioned(t *testing.T) {
	p := live(t)
	tier := environment.TierPreview
	bootstrap := bootstrapped(t, p, tier)

	plan, err := bootstrap.Plan(context.Background(), provider.BootstrapRequest{Tier: tier})
	if err != nil {
		t.Fatalf("Plan() after Apply() = %v", err)
	}
	rows := 0
	for _, group := range plan.Groups {
		if group.Action != provider.ActionKeep {
			t.Errorf("Plan() shows %s as %q after an apply, want it kept: a second apply must be a no-op", group.Name, group.Action)
		}
		for _, change := range group.Changes {
			rows++
			if change.Action != provider.ActionKeep {
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
	tier := environment.TierProduction

	plan, err := bootstrapOf(t, p).Plan(context.Background(), provider.BootstrapRequest{Tier: tier})
	if err != nil {
		t.Fatalf("Plan() = %v", err)
	}

	kinds := map[string]string{}
	groups := map[string]string{}
	for _, group := range plan.Groups {
		if !strings.HasPrefix(group.Name, string(gcp.Vendor)+"/") {
			t.Errorf("Plan() returned the group %q, want every group named under the vendor that provisions it", group.Name)
		}
		for _, change := range group.Changes {
			kinds[change.Kind+"/"+change.Name] = string(change.Action)
			groups[change.Kind+"/"+change.Name] = group.Kind
		}
	}
	rows := map[string]string{
		"firestore:database/" + liveNames(t).Database():                 provider.StackGroupKind,
		"firestore:database/" + liveNames(t).TagDatabase(tier):          provider.StackGroupKind,
		"storage:bucket/" + liveNames(t).Bucket(tier):                   provider.StackGroupKind,
		"storage:bucket/" + liveNames(t).StateBucket(tier):              provider.StackGroupKind,
		"kms:keyring/" + liveNames(t).KeyRing():                         provider.StackGroupKind,
		"kms:key/" + string(tier):                                       provider.StackGroupKind,
		"secretmanager:secret/" + liveNames(t).PassphraseSecret(tier):   provider.ParameterGroupKind,
		"iam:serviceaccount/" + liveNames(t).EnvSourceSyncAccount(tier): provider.StackGroupKind,
		"run:service/" + liveNames(t).EnvSourceSync(tier):               provider.StackGroupKind,
		"cloudscheduler:job/" + liveNames(t).EnvSourceSync(tier):        provider.StackGroupKind,
	}
	if !emulated() {
		rows["artifactregistry:repository/"+liveNames(t).Repository(tier)] = provider.StackGroupKind
	}
	for row, group := range rows {
		if _, shown := kinds[row]; !shown {
			t.Errorf("Plan() shows no row for %s, and what the plan does not show, the apply may not do", row)
			continue
		}
		if groups[row] != group {
			t.Errorf("Plan() shows %s under a %q group, want it under %q", row, groups[row], group)
		}
	}
}

func TestLiveABootstrapUnderOneNamespaceIsNoBootstrapUnderAnother(t *testing.T) {
	p := live(t)
	if !emulated() {
		t.Skip("a second namespace provisions a second key ring, and Google never deletes one, so this runs against the emulator only")
	}
	tier := environment.TierProduction
	here := bootstrapped(t, p, tier)

	t.Setenv(provider.NamespaceEnvVar, names(t, p).Namespace().String()+"-beside")
	beside := newProvider(t, gcp.Options{Project: liveProject(), Region: liveRegion()})
	if names(t, beside).Bucket(tier) == names(t, p).Bucket(tier) {
		t.Fatalf("both namespaces name the bucket %s, and this test turns on them naming different ones", names(t, beside).Bucket(tier))
	}

	ctx := context.Background()
	elsewhere := bootstrapOf(t, beside)
	described, err := elsewhere.Describe(ctx, tier)
	if err != nil {
		t.Fatalf("Describe() under a second namespace = %v", err)
	}
	if described.Present {
		t.Errorf("Describe() under namespace %s reads the bootstrap installed under %s as its own", names(t, beside).Namespace(), names(t, p).Namespace())
	}

	plan, err := elsewhere.Plan(ctx, provider.BootstrapRequest{Tier: tier})
	if err != nil {
		t.Fatalf("Plan() under a second namespace = %v", err)
	}
	rows := 0
	for _, group := range plan.Groups {
		for _, change := range group.Changes {
			rows++
			if change.Kind == string(gcp.KindDatabase) && emulated() {
				continue
			}
			if change.Action != provider.ActionCreate {
				t.Errorf("Plan() under namespace %s shows %s %s as %q, want a create: nothing of it exists yet",
					names(t, beside).Namespace(), change.Kind, change.Name, change.Action)
			}
		}
	}
	if rows == 0 {
		t.Fatal("Plan() showed no rows at all, and a plan with nothing in it consents to nothing")
	}

	bootstrapped(t, beside, tier)
	for what, bootstrap := range map[string]provider.Bootstrap{
		names(t, p).Namespace().String():      here,
		names(t, beside).Namespace().String(): elsewhere,
	} {
		installed, err := bootstrap.Describe(ctx, tier)
		if err != nil {
			t.Fatalf("Describe() under namespace %s = %v", what, err)
		}
		if !installed.Present || installed.Unfinished || !installed.Stacks[0].DigestCurrent {
			t.Errorf("Describe() under namespace %s = %+v, want a bootstrap installed beside the other, not under it", what, installed)
		}
	}
}

func TestLiveAPlanIsRefusedWhileAnApiTheStackNeedsIsOff(t *testing.T) {
	p := live(t)
	servicesDisabled(t, "cloudkms.googleapis.com")

	var refused refusal.Refusal
	_, err := bootstrapOf(t, p).Plan(context.Background(), provider.BootstrapRequest{Tier: environment.TierProduction})
	if !errors.As(err, &refused) || refused.Code != refusal.CodeNotReady {
		t.Fatalf("Plan() with an API off = %v, want a %s refusal: enabling it is a precondition, not a row in the stack", err, refusal.CodeNotReady)
	}
	if !strings.Contains(refused.Message, "cloudkms.googleapis.com") {
		t.Errorf("Plan() refused with %q, want it to name the API that is off", refused.Message)
	}
}

func servicesDisabled(t *testing.T, api string) {
	t.Helper()
	if !emulated() {
		t.Skipf("switching %s off is a real change to a real project, so this runs against the emulator only", api)
	}

	ctx := context.Background()
	service, err := serviceusage.NewService(ctx, ports.EmulatorREST(endpoint())...)
	if err != nil {
		t.Fatalf("reach the emulator's service usage API: %v", err)
	}
	name := "projects/" + liveProject() + "/services/" + api
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
	tier := environment.TierPreview
	bootstrap := bootstrapOf(t, p)

	ctx := context.Background()
	if err := bootstrap.Apply(ctx, provider.BootstrapRequest{Tier: tier, WrittenBy: "live-suite"}, nil); err != nil {
		t.Fatalf("Apply() = %v", err)
	}
	t.Cleanup(func() {
		if err := bootstrap.Remove(ctx, tier, nil); err != nil {
			t.Errorf("Remove() = %v", err)
		}
	})

	interrupt(t, p, tier)

	described, err := bootstrap.Describe(ctx, tier)
	if err != nil {
		t.Fatalf("Describe() = %v", err)
	}
	if !described.Unfinished {
		t.Fatal("Describe() reads an apply that stopped half way as finished, and nothing would ever go back to finish it")
	}

	if err := bootstrap.Apply(ctx, provider.BootstrapRequest{Tier: tier, WrittenBy: "live-suite"}, nil); err != nil {
		t.Fatalf("Apply() over an unfinished bootstrap = %v, want it finished", err)
	}
	repaired, err := bootstrap.Describe(ctx, tier)
	if err != nil {
		t.Fatalf("Describe() after the second Apply() = %v", err)
	}
	if repaired.Unfinished || !repaired.Present {
		t.Fatalf("Describe() = %+v after a re-apply, want a bootstrap that is installed and finished", repaired)
	}
}

func interrupt(t *testing.T, p *gcp.Provider, tier environment.Tier) {
	t.Helper()

	ctx := context.Background()
	client, err := storage.NewClient(ctx, ports.EmulatorStorage(endpoint())...)
	if err != nil {
		t.Fatalf("reach the emulator's object store: %v", err)
	}
	t.Cleanup(func() { client.Close() })

	writer := client.Bucket(liveNames(t).Bucket(tier)).Object(gcp.StampObject).NewWriter(ctx)
	if _, err := writer.Write([]byte(`{"state":"applying","writer":"live-suite","digest":"halfway"}`)); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestLiveAStateBucketStoringAStackIsNotSweptOutFromUnderIt(t *testing.T) {
	p := live(t)
	tier := environment.TierPreview
	bootstrap := bootstrapped(t, p, tier)

	ctx := context.Background()
	client, err := storage.NewClient(ctx, ports.EmulatorStorage(endpoint())...)
	if err != nil {
		t.Fatalf("reach the emulator's object store: %v", err)
	}
	defer client.Close()

	object := client.Bucket(liveNames(t).StateBucket(tier)).Object(".pulumi/stacks/ocel/shop.json")
	writer := object.NewWriter(ctx)
	if _, err := writer.Write([]byte("{}")); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}

	var refused refusal.Refusal
	err = bootstrap.Remove(ctx, tier, nil)
	if !errors.As(err, &refused) || refused.Code != refusal.CodeNotReady {
		t.Fatalf("Remove() with a stack still in the state bucket = %v, want a %s refusal", err, refusal.CodeNotReady)
	}
	if !strings.Contains(refused.Message, "shop") {
		t.Errorf("Remove() refused with %q, want it to name the stack still deployed", refused.Message)
	}
	if err := object.Delete(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestLiveASharedResourceStaysWhileTheSiblingTierIsInstalled(t *testing.T) {
	p := live(t)
	servicesEnabled(t)
	bootstrapped(t, p, environment.TierProduction)
	bootstrap := bootstrapped(t, p, environment.TierPreview)

	removal, err := bootstrap.PlanRemove(context.Background(), environment.TierPreview)
	if err != nil {
		t.Fatalf("PlanRemove() = %v", err)
	}
	kept := map[string]provider.Change{}
	for _, group := range removal.Groups {
		for _, change := range group.Changes {
			kept[change.Kind+"/"+change.Name] = change
		}
	}
	ring := kept["kms:keyring/"+liveNames(t).KeyRing()]
	if ring.Action != provider.ActionKeep || ring.Reason == "" {
		t.Errorf("PlanRemove() shows the key ring as %q (%q), want it kept with a reason: Google never deletes a key ring", ring.Action, ring.Reason)
	}
	database := kept["firestore:database/"+liveNames(t).Database()]
	if database.Action != provider.ActionKeep || !strings.Contains(database.Reason, string(environment.TierProduction)) {
		t.Errorf("PlanRemove() shows the database as %q (%q), want it kept with a reason naming the sibling tier still installed",
			database.Action, database.Reason)
	}
}

func TestLiveThePassphraseIsMintedOnceAndNotWrittenOverAgain(t *testing.T) {
	p := live(t)
	tier := environment.TierProduction
	bootstrap := bootstrapped(t, p, tier)

	ctx := context.Background()
	minted := storedPassphrase(t, tier)
	if len(minted) == 0 {
		t.Fatal("the bootstrap minted an empty passphrase, and every Pulumi stack in this project is encrypted under it")
	}
	if err := bootstrap.Apply(ctx, provider.BootstrapRequest{Tier: tier, WrittenBy: "live-suite"}, nil); err != nil {
		t.Fatalf("Apply() a second time = %v", err)
	}
	if again := storedPassphrase(t, tier); !bytes.Equal(minted, again) {
		t.Fatal("a second apply wrote a new passphrase, and nothing opens the state the first one encrypted")
	}
}

func storedPassphrase(t *testing.T, tier environment.Tier) []byte {
	t.Helper()

	ctx := context.Background()
	service, err := secretmanager.NewService(ctx, ports.EmulatorREST(endpoint())...)
	if err != nil {
		t.Fatalf("reach the emulator's secret manager: %v", err)
	}
	name := "projects/" + liveProject() + "/secrets/" + liveNames(t).PassphraseSecret(tier) + "/versions/latest"
	version, err := service.Projects.Secrets.Versions.Access(name).Context(ctx).Do()
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	decoded, err := base64.StdEncoding.DecodeString(version.Payload.Data)
	if err != nil {
		t.Fatalf("decode the passphrase: %v", err)
	}
	return decoded
}

func onRealGoogleCloud(t *testing.T) {
	t.Helper()
	if emulated() {
		t.Skip("the emulator serves no Firestore admin API: it has one implicit database, no delete protection and no list of locations")
	}
}

func TestProjectTheDatabaseIsARowOfItsOwnProvisionedUnderDeleteProtection(t *testing.T) {
	p := live(t)
	onRealGoogleCloud(t)
	bootstrapped(t, p, environment.TierProduction)

	ctx := context.Background()
	service, err := firestoreadmin.NewService(ctx)
	if err != nil {
		t.Fatalf("reach the Firestore admin API: %v", err)
	}
	database, err := service.Projects.Databases.Get("projects/" + liveProject() + "/databases/ocel").Context(ctx).Do()
	if err != nil {
		t.Fatalf("read the database the bootstrap provisioned: %v", err)
	}
	if database.DeleteProtectionState != "DELETE_PROTECTION_ENABLED" {
		t.Errorf("the ocel database has delete protection %q, want it on: every record both tiers write lives in it",
			database.DeleteProtectionState)
	}
	if database.LocationId != liveRegion() {
		t.Errorf("the database is in %q, want the one region the bootstrap was given, %q", database.LocationId, liveRegion())
	}
}

func TestProjectATiersTagsDatabaseIsProtectedIndexedAndGoneOnceTheTierIsRemoved(t *testing.T) {
	p := live(t)
	onRealGoogleCloud(t)
	tier := environment.TierPreview
	bootstrap := bootstrapped(t, p, tier)

	ctx := context.Background()
	service, err := firestoreadmin.NewService(ctx)
	if err != nil {
		t.Fatalf("reach the Firestore admin API: %v", err)
	}
	path := "projects/" + liveProject() + "/databases/" + liveNames(t).TagDatabase(tier)
	database, err := service.Projects.Databases.Get(path).Context(ctx).Do()
	if err != nil {
		t.Fatalf("read the tags database the bootstrap provisioned: %v", err)
	}
	if database.DeleteProtectionState != "DELETE_PROTECTION_ENABLED" {
		t.Errorf("the tags database has delete protection %q, want it on", database.DeleteProtectionState)
	}
	listed, err := service.Projects.Databases.CollectionGroups.Indexes.List(path + "/collectionGroups/tags").Context(ctx).Do()
	if err != nil {
		t.Fatalf("list the indexes of the tag records: %v", err)
	}
	ready := false
	for _, index := range listed.Indexes {
		if index.State == "READY" && len(index.Fields) >= 2 &&
			index.Fields[0].FieldPath == "prefix" && index.Fields[0].Order == "ASCENDING" &&
			index.Fields[1].FieldPath == "writtenAt" && index.Fields[1].Order == "ASCENDING" {
			ready = true
		}
	}
	if !ready {
		t.Errorf("the tag records have the indexes %+v, want a READY one on prefix then writtenAt", listed.Indexes)
	}

	if err := bootstrap.Remove(ctx, tier, nil); err != nil {
		t.Fatalf("Remove(%s) = %v", tier, err)
	}
	if _, err := service.Projects.Databases.Get(path).Context(ctx).Do(); err == nil {
		t.Errorf("the tags database %s still exists after its tier was removed", path)
	}
}

func TestProjectARegionFirestoreDoesNotServeIsRefusedNamingTheOnesItDoes(t *testing.T) {
	live(t)
	onRealGoogleCloud(t)

	elsewhere := newProvider(t, gcp.Options{Project: liveProject(), Region: "no-such-region1"})
	var refused refusal.Refusal
	_, err := bootstrapOf(t, elsewhere).Plan(context.Background(),
		provider.BootstrapRequest{Tier: environment.TierProduction})
	if !errors.As(err, &refused) || refused.Code != refusal.CodeInvalid {
		t.Fatalf("Plan() in a region Firestore does not serve = %v, want an %s refusal", err, refusal.CodeInvalid)
	}
	if !strings.Contains(refused.Message, liveRegion()) {
		t.Errorf("Plan() refused with %q, want it to list the regions Firestore does serve", refused.Message)
	}
}

type watcher struct{ said func(string) }

func (w watcher) Say(message string) { w.said(message) }

func (w watcher) Warn(message string) { w.said(message) }

func (w watcher) Error(message string) { w.said(message) }

func (w watcher) Detail(message string) { w.said(message) }

func (w watcher) Debug(message string) { w.said(message) }

func (watcher) Span(string, time.Time, time.Time, error, ...progress.Attr) {}

func TestLiveAStackMissingOneOfItsResourcesIsNotReportedAsCurrent(t *testing.T) {
	p := live(t)
	tier := environment.TierPreview
	bootstrap := bootstrapped(t, p, tier)

	ctx := context.Background()
	client, err := storage.NewClient(ctx, ports.EmulatorStorage(endpoint())...)
	if err != nil {
		t.Fatalf("reach the emulator's object store: %v", err)
	}
	defer client.Close()
	if err := client.Bucket(liveNames(t).StateBucket(tier)).Delete(ctx); err != nil {
		t.Fatalf("delete the state bucket out of band: %v", err)
	}

	described, err := bootstrap.Describe(ctx, tier)
	if err != nil {
		t.Fatalf("Describe() = %v", err)
	}
	if len(described.Stacks) != 1 || described.Stacks[0].DigestCurrent {
		t.Errorf("Describe().Stacks = %+v with a bucket gone, want the stack read as behind", described.Stacks)
	}

	plan, err := bootstrap.Plan(ctx, provider.BootstrapRequest{Tier: tier})
	if err != nil {
		t.Fatalf("Plan() = %v", err)
	}
	for _, group := range plan.Groups {
		if group.Kind != provider.StackGroupKind {
			continue
		}
		if group.Action == provider.ActionKeep {
			t.Errorf("Plan() shows %s as %q with a bucket gone while its rows create it, and the group is what the operator consents to",
				group.Name, group.Action)
		}
	}
}

func TestLiveARemovalUnderWayReadsAsUnfinishedRatherThanDone(t *testing.T) {
	p := live(t)
	servicesEnabled(t)
	tier := environment.TierPreview
	bootstrap := bootstrapOf(t, p)

	ctx := context.Background()
	if err := bootstrap.Apply(ctx, provider.BootstrapRequest{Tier: tier, WrittenBy: "live-suite"}, nil); err != nil {
		t.Fatalf("Apply() = %v", err)
	}

	read, taken := false, false
	watch := watcher{said: func(message string) {
		removing := strings.HasPrefix(message, "Removed ") || strings.HasPrefix(message, "Scheduled ")
		if read || !removing {
			return
		}
		read = true
		described, err := bootstrap.Describe(ctx, tier)
		if err != nil {
			t.Errorf("Describe() while the removal runs = %v", err)
			return
		}
		taken = described.Unfinished
	}}
	if err := bootstrap.Remove(ctx, tier, watch); err != nil {
		t.Fatalf("Remove() = %v", err)
	}
	if !read {
		t.Fatal("Remove() took nothing down at all, so nothing here was tested")
	}
	if !taken {
		t.Error("Describe() reads a bootstrap whose key is already destroyed as finished: the next apply mints a new key version and every value sealed under the old one stays shut")
	}
}

func TestLiveAnApplyRefusesRatherThanWriteOverAnotherRunsStamp(t *testing.T) {
	p := live(t)
	tier := environment.TierProduction
	bootstrap := bootstrapped(t, p, tier)

	ctx := context.Background()
	written := false
	watch := watcher{said: func(string) {
		if written {
			return
		}
		written = true
		stamped(t, tier, `{"state":"applying","writer":"the-other-run","digest":"elsewhere"}`)
	}}

	var refused refusal.Refusal
	err := bootstrap.Apply(ctx, provider.BootstrapRequest{Tier: tier, WrittenBy: "live-suite"}, watch)
	if !errors.As(err, &refused) || refused.Code != refusal.CodeBusy {
		t.Fatalf("Apply() over a stamp another run wrote = %v, want a %s refusal", err, refusal.CodeBusy)
	}
	if !strings.Contains(refused.Message, "the-other-run") {
		t.Errorf("Apply() refused with %q, want it to name the run it collided with", refused.Message)
	}
}

func stamped(t *testing.T, tier environment.Tier, body string) {
	t.Helper()

	ctx := context.Background()
	client, err := storage.NewClient(ctx, ports.EmulatorStorage(endpoint())...)
	if err != nil {
		t.Fatalf("reach the emulator's object store: %v", err)
	}
	defer client.Close()

	writer := client.Bucket(liveNames(t).Bucket(tier)).Object(gcp.StampObject).NewWriter(ctx)
	if _, err := writer.Write([]byte(body)); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestLiveASecretWithNoVersionInItIsNotPresentAndAReApplyMintsOne(t *testing.T) {
	p := live(t)
	tier := environment.TierPreview
	bootstrap := bootstrapped(t, p, tier)

	ctx := context.Background()
	service, err := secretmanager.NewService(ctx, ports.EmulatorREST(endpoint())...)
	if err != nil {
		t.Fatalf("reach the emulator's secret manager: %v", err)
	}
	name := liveNames(t).PassphraseSecret(tier)
	parent := "projects/" + liveProject()
	if _, err := service.Projects.Secrets.Delete(parent + "/secrets/" + name).Context(ctx).Do(); err != nil {
		t.Fatalf("delete the passphrase secret out of band: %v", err)
	}
	if _, err := service.Projects.Secrets.Create(parent, &secretmanager.Secret{
		Replication: &secretmanager.Replication{Automatic: &secretmanager.Automatic{}},
	}).SecretId(name).Context(ctx).Do(); err != nil {
		t.Fatalf("put the secret back with nothing in it: %v", err)
	}

	plan, err := bootstrap.Plan(ctx, provider.BootstrapRequest{Tier: tier})
	if err != nil {
		t.Fatalf("Plan() = %v", err)
	}
	for _, group := range plan.Groups {
		for _, change := range group.Changes {
			if change.Kind != "secretmanager:secret" {
				continue
			}
			if change.Action != provider.ActionCreate {
				t.Errorf("Plan() shows the passphrase secret as %q while it has no version at all, and nothing would ever put one in it", change.Action)
			}
		}
	}

	if err := bootstrap.Apply(ctx, provider.BootstrapRequest{Tier: tier, WrittenBy: "live-suite"}, nil); err != nil {
		t.Fatalf("Apply() over a secret with no version = %v", err)
	}
	if len(storedPassphrase(t, tier)) == 0 {
		t.Error("the apply left the passphrase secret empty, and every Pulumi stack in this tier is encrypted under it")
	}
}

func protectionLifted(t *testing.T) {
	t.Helper()

	ctx := context.Background()
	service, err := firestoreadmin.NewService(ctx)
	if err != nil {
		t.Fatalf("reach the Firestore admin API: %v", err)
	}
	path := "projects/" + liveProject() + "/databases/ocel"
	operation, err := service.Projects.Databases.Patch(path, &firestoreadmin.GoogleFirestoreAdminV1Database{
		DeleteProtectionState: "DELETE_PROTECTION_DISABLED",
	}).UpdateMask("deleteProtectionState").Context(ctx).Do()
	if err != nil {
		t.Fatalf("lift delete protection out of band: %v", err)
	}
	for range 60 {
		if operation.Done {
			return
		}
		time.Sleep(time.Second)
		if operation, err = service.Projects.Databases.Operations.Get(operation.Name).Context(ctx).Do(); err != nil {
			t.Fatalf("wait for delete protection to come off: %v", err)
		}
	}
	t.Fatal("delete protection never came off, so this test never got to say anything")
}

func TestProjectADatabaseLeftUnprotectedIsAnUpdateRowAnApplyMends(t *testing.T) {
	p := live(t)
	onRealGoogleCloud(t)
	tier := environment.TierProduction
	bootstrap := bootstrapped(t, p, tier)

	ctx := context.Background()
	protectionLifted(t)

	plan, err := bootstrap.Plan(ctx, provider.BootstrapRequest{Tier: tier})
	if err != nil {
		t.Fatalf("Plan() = %v", err)
	}
	shown := provider.Change{}
	for _, group := range plan.Groups {
		for _, change := range group.Changes {
			if change.Kind == "firestore:database" {
				shown = change
			}
		}
	}
	if shown.Action != provider.ActionUpdate || shown.Reason == "" {
		t.Errorf("Plan() shows the database as %q (%q) with delete protection off, want an update row saying why",
			shown.Action, shown.Reason)
	}

	if err := bootstrap.Apply(ctx, provider.BootstrapRequest{Tier: tier, WrittenBy: "live-suite"}, nil); err != nil {
		t.Fatalf("Apply() over an unprotected database = %v", err)
	}
	service, err := firestoreadmin.NewService(ctx)
	if err != nil {
		t.Fatal(err)
	}
	database, err := service.Projects.Databases.Get("projects/" + liveProject() + "/databases/ocel").Context(ctx).Do()
	if err != nil {
		t.Fatalf("read the database the apply mended: %v", err)
	}
	if database.DeleteProtectionState != "DELETE_PROTECTION_ENABLED" {
		t.Errorf("the ocel database has delete protection %q after an apply, want it back on", database.DeleteProtectionState)
	}
}

func elsewhereRegion() string {
	if liveRegion() == "us-east1" {
		return "europe-west1"
	}
	return "us-east1"
}

func TestProjectADatabaseInAnotherRegionIsRefusedRatherThanUsed(t *testing.T) {
	p := live(t)
	onRealGoogleCloud(t)
	tier := environment.TierProduction
	bootstrapped(t, p, tier)

	elsewhere := newProvider(t, gcp.Options{Project: liveProject(), Region: elsewhereRegion()})
	var refused refusal.Refusal
	err := bootstrapOf(t, elsewhere).Apply(context.Background(),
		provider.BootstrapRequest{Tier: tier, WrittenBy: "live-suite"}, nil)
	if !errors.As(err, &refused) || refused.Code != refusal.CodeInvalid {
		t.Fatalf("Apply() against a database that exists in another region = %v, want an %s refusal: the conflict is not a success",
			err, refusal.CodeInvalid)
	}
	if !strings.Contains(refused.Message, liveRegion()) {
		t.Errorf("Apply() refused with %q, want it to name the region the database actually lives in", refused.Message)
	}
}

func rowsOf(t *testing.T, plan provider.Plan) map[string]provider.Change {
	t.Helper()
	rows := map[string]provider.Change{}
	for _, group := range plan.Groups {
		for _, change := range group.Changes {
			rows[change.Kind+"/"+change.Name] = change
		}
	}
	return rows
}

func TestLiveTheArtifactBucketIsRemovedSlowlyBecauseItIsEmptiedFirst(t *testing.T) {
	p := live(t)
	tier := environment.TierPreview
	bootstrap := bootstrapped(t, p, tier)

	plan, err := bootstrap.PlanRemove(context.Background(), tier)
	if err != nil {
		t.Fatalf("PlanRemove() = %v", err)
	}
	rows := rowsOf(t, plan)
	artifacts := rows["storage:bucket/"+liveNames(t).Bucket(tier)]
	if artifacts.Action != provider.ActionDelete || !artifacts.Slow {
		t.Errorf("PlanRemove() shows the artifact bucket as %q (slow %v), want a delete marked slow: every artifact in it is deleted one by one first",
			artifacts.Action, artifacts.Slow)
	}
	state := rows["storage:bucket/"+liveNames(t).StateBucket(tier)]
	if state.Slow {
		t.Error("PlanRemove() marks the state bucket slow, and it is refused unless it is already empty")
	}
}

func TestLiveASharedRowNamesTheSiblingTierInEveryPlanItAppearsIn(t *testing.T) {
	p := live(t)
	tier := environment.TierPreview
	bootstrapped(t, p, environment.TierProduction)
	bootstrap := bootstrapped(t, p, tier)

	ctx := context.Background()
	plan, err := bootstrap.Plan(ctx, provider.BootstrapRequest{Tier: tier})
	if err != nil {
		t.Fatalf("Plan() = %v", err)
	}
	database := rowsOf(t, plan)["firestore:database/"+liveNames(t).Database()]
	if database.Action != provider.ActionKeep || !strings.Contains(database.Reason, string(environment.TierProduction)) {
		t.Errorf("Plan() shows the database as %q (%q), want it kept for a reason naming the sibling tier that shares it, as the removal plan does",
			database.Action, database.Reason)
	}
}

func accounts(t *testing.T) *iam.Service {
	t.Helper()
	service, err := iam.NewService(context.Background(), ports.EmulatorREST(endpoint())...)
	if err != nil {
		t.Fatalf("reach the IAM API: %v", err)
	}
	return service
}

func TestLiveRemovingATierBesideItsSiblingForgetsEveryRecordItKeptAndNoneOfTheSiblings(t *testing.T) {
	p := live(t)
	bootstrapped(t, p, environment.TierProduction)
	tier := environment.TierPreview

	ctx := context.Background()
	bootstrap := bootstrapOf(t, p)
	if err := bootstrap.Apply(ctx, provider.BootstrapRequest{Tier: tier, WrittenBy: "live-suite"}, nil); err != nil {
		t.Fatalf("Apply(%s) = %v", tier, err)
	}
	kept := map[environment.Tier]keyvalue.Key{}
	for _, each := range []environment.Tier{environment.TierProduction, tier} {
		kept[each] = keyvalue.Partition{Tier: each, Root: keyvalue.RootEnvSourceDigestKey}.Key("digestkey")
		if _, err := p.KeyValues().Write(ctx, keyvalue.Entry{Key: kept[each], Value: []byte(`{"sealed":"c2VhbGVk"}`)}); err != nil {
			t.Fatalf("Write(%s) = %v", kept[each], err)
		}
	}
	t.Cleanup(func() {
		if err := keyvalue.Forget(ctx, p.KeyValues(), kept[environment.TierProduction]); err != nil {
			t.Errorf("Forget(%s) = %v", kept[environment.TierProduction], err)
		}
	})

	if err := bootstrap.Remove(ctx, tier, nil); err != nil {
		t.Fatalf("Remove(%s) = %v", tier, err)
	}
	if _, err := p.KeyValues().Read(ctx, kept[tier]); !errors.Is(err, keyvalue.ErrNotFound) {
		t.Errorf("Read(%s) after its tier was removed = %v, want it gone: it was sealed under the key the removal destroyed, and the database outlives the tier while %s stays",
			kept[tier], err, environment.TierProduction)
	}
	if _, err := p.KeyValues().Read(ctx, kept[environment.TierProduction]); err != nil {
		t.Errorf("Read(%s) after the other tier was removed = %v, want it kept", kept[environment.TierProduction], err)
	}
}
