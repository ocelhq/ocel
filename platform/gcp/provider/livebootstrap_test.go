//go:build integration

package gcp_test

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
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
	"github.com/ocelhq/ocel/pkg/envsource"
	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/progress"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/conformance"
	"github.com/ocelhq/ocel/pkg/records"
	"github.com/ocelhq/ocel/pkg/refusal"
	gcp "github.com/ocelhq/ocel/platform/gcp/provider"
	gcpenvsource "github.com/ocelhq/ocel/platform/gcp/provider/envsource"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/bootstrap"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/gcloud"
	"github.com/ocelhq/ocel/platform/gcp/provider/ports"
	"github.com/ocelhq/ocel/platform/gcp/provider/resources/task"
)

func endpoint() string { return os.Getenv("OCEL_FLOCI_GCP_ENDPOINT") }

func liveTierBucket(t *testing.T, tier environment.Tier, class naming.StorageClass) string {
	t.Helper()
	clients := &gcloud.Clients{Names: liveNames(t), Region: liveRegion(), Endpoint: endpoint()}
	clients.Records = ports.KeyValues{Clients: clients.Workload()}
	buckets, err := clients.TierBuckets(context.Background(), tier, class)
	if err != nil {
		t.Fatalf("read the %s %s bucket: %v", tier, class, err)
	}
	return buckets[class]
}

func plannedRow(t *testing.T, tier environment.Tier, change provider.Change) string {
	t.Helper()
	for _, class := range []naming.StorageClass{naming.StorageState, naming.StorageEdge, naming.StorageCache} {
		if stem := liveNames(t).TierBucketStem(tier, class); strings.HasPrefix(change.Name, stem+naming.WordSeparator) {
			return change.Kind + "/" + stem
		}
	}
	return change.Kind + "/" + change.Name
}

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
	apis := slices.Concat(bootstrap.BootstrapAPIs, bootstrap.Keys.APIs, bootstrap.Realtime.APIs, bootstrap.Topics.APIs, gcpenvsource.SyncAPIs, []string{"iamcredentials.googleapis.com"})
	for _, api := range apis {
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

func conformingBootstrap(t *testing.T, p *gcp.Provider) provider.Bootstrap {
	t.Helper()
	return bootstrapOf(t, p)
}

func bootstrapped(t *testing.T, p *gcp.Provider, tier environment.Tier, parts ...string) provider.Bootstrap {
	t.Helper()

	servicesEnabled(t)
	ctx := context.Background()
	bootstrap := bootstrapOf(t, p)
	if err := bootstrap.Apply(ctx, provider.BootstrapRequest{Tier: tier, Parts: parts, WrittenBy: "live-suite"}, nil); err != nil {
		t.Fatalf("Apply(%s) = %v, want the resources every port beneath it reads and writes", tier, err)
	}
	t.Cleanup(func() {
		if err := bootstrap.Remove(ctx, tier, nil); err != nil {
			t.Errorf("Remove(%s) = %v", tier, err)
		}
	})
	return bootstrap
}

func bootstrappedWith(t *testing.T, p *gcp.Provider, tier environment.Tier, parts ...string) provider.Bootstrap {
	t.Helper()
	bootstrap := bootstrapped(t, p, tier)
	if err := gcp.AddParts(context.Background(), p, tier, parts...); err != nil {
		t.Fatalf("AddParts(%s, %v) = %v, want the parts a deploy adds on first use", tier, parts, err)
	}
	return bootstrap
}

func projectDatabaseMade(t *testing.T, p *gcp.Provider, tier environment.Tier, project string) {
	t.Helper()
	if err := gcp.MakeProjectDatabase(context.Background(), p, tier, project); err != nil {
		t.Fatalf("MakeProjectDatabase(%s, %s) = %v, want the database a deploy keeps its project's runs and tag records in", tier, project, err)
	}
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
	installed := bootstrappedWith(t, p, tier, bootstrap.EdgeBucketPart, bootstrap.CacheBucketPart)

	ctx := context.Background()
	described, err := installed.Describe(ctx, tier)
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
		if !strings.HasPrefix(group.Name, string(gcloud.Vendor)+"/") {
			t.Errorf("Plan() returned the group %q, want every group named under the vendor that provisions it", group.Name)
		}
		for _, change := range group.Changes {
			kinds[plannedRow(t, tier, change)] = string(change.Action)
			groups[plannedRow(t, tier, change)] = group.Kind
		}
	}
	rows := map[string]string{
		"firestore:database/" + liveNames(t).RecordsDatabase(tier):                 provider.StackGroupKind,
		"storage:bucket/" + liveNames(t).TierBucketStem(tier, naming.StorageState): provider.StackGroupKind,
		"secretmanager:secret/" + liveNames(t).PassphraseSecret(tier):              provider.ParameterGroupKind,
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
		t.Skip("a second namespace makes custom roles of its own, and Google holds a deleted role's id for 44 days, so this runs against the emulator only")
	}
	tier := environment.TierProduction
	here := bootstrapped(t, p, tier)

	t.Setenv(provider.NamespaceEnvVar, namesOf(t, p).Namespace().String()+"-beside")
	beside := newProvider(t, gcp.Options{Project: liveProject(), Region: liveRegion()})
	if namesOf(t, beside).TierBucketStem(tier, naming.StorageState) == namesOf(t, p).TierBucketStem(tier, naming.StorageState) {
		t.Fatalf("both namespaces name the state bucket %s, and this test turns on them naming different ones", namesOf(t, beside).TierBucketStem(tier, naming.StorageState))
	}

	ctx := context.Background()
	elsewhere := bootstrapOf(t, beside)
	described, err := elsewhere.Describe(ctx, tier)
	if err != nil {
		t.Fatalf("Describe() under a second namespace = %v", err)
	}
	if described.Present {
		t.Errorf("Describe() under namespace %s reads the bootstrap installed under %s as its own", namesOf(t, beside).Namespace(), namesOf(t, p).Namespace())
	}

	plan, err := elsewhere.Plan(ctx, provider.BootstrapRequest{Tier: tier})
	if err != nil {
		t.Fatalf("Plan() under a second namespace = %v", err)
	}
	rows := 0
	for _, group := range plan.Groups {
		for _, change := range group.Changes {
			rows++
			if change.Kind == string(bootstrap.KindDatabase) && emulated() {
				continue
			}
			if change.Action != provider.ActionCreate {
				t.Errorf("Plan() under namespace %s shows %s %s as %q, want a create: nothing of it exists yet",
					namesOf(t, beside).Namespace(), change.Kind, change.Name, change.Action)
			}
		}
	}
	if rows == 0 {
		t.Fatal("Plan() showed no rows at all, and a plan with nothing in it consents to nothing")
	}

	bootstrapped(t, beside, tier)
	for what, bootstrap := range map[string]provider.Bootstrap{
		namesOf(t, p).Namespace().String():      here,
		namesOf(t, beside).Namespace().String(): elsewhere,
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
	servicesDisabled(t, "secretmanager.googleapis.com")

	var refused refusal.Refusal
	_, err := bootstrapOf(t, p).Plan(context.Background(), provider.BootstrapRequest{Tier: environment.TierProduction})
	if !errors.As(err, &refused) || refused.Code != refusal.CodeNotReady {
		t.Fatalf("Plan() with an API off = %v, want a %s refusal: enabling it is a precondition, not a row in the stack", err, refusal.CodeNotReady)
	}
	if !strings.Contains(refused.Message, "secretmanager.googleapis.com") {
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

	writer := client.Bucket(liveTierBucket(t, tier, naming.StorageState)).Object(bootstrap.StampObject).NewWriter(ctx)
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

	object := client.Bucket(liveTierBucket(t, tier, naming.StorageState)).Object("ocel-alb-project-shop/.pulumi/stacks/ocel-alb-project-shop/alb-preview-shop--infra.json")
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
	installed := bootstrappedWith(t, p, environment.TierPreview, bootstrap.KeysPart)

	removal, err := installed.PlanRemove(context.Background(), environment.TierPreview)
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
	database := kept["firestore:database/"+liveNames(t).RecordsDatabase(environment.TierPreview)]
	if database.Action != provider.ActionDisableThenDelete {
		t.Errorf("PlanRemove() shows the tier's records database as %q (%q), want it deleted: the sibling tier keeps its records in its own",
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

func TestProjectTheDatabaseATiersProjectsShareIsProtectedIndexedAndGoneOnceTheTierIsRemoved(t *testing.T) {
	p := live(t)
	onRealGoogleCloud(t)
	tier := environment.TierPreview
	bootstrap := bootstrappedWith(t, p, tier)
	projectDatabaseMade(t, p, tier, liveSlug)

	ctx := context.Background()
	service, err := firestoreadmin.NewService(ctx)
	if err != nil {
		t.Fatalf("reach the Firestore admin API: %v", err)
	}
	path := "projects/" + liveProject() + "/databases/" + liveNames(t).SharedDatabase(tier)
	database, err := service.Projects.Databases.Get(path).Context(ctx).Do()
	if err != nil {
		t.Fatalf("read the database the deploy made: %v", err)
	}
	if database.DeleteProtectionState != "DELETE_PROTECTION_ENABLED" {
		t.Errorf("the database the %s tier's projects share has delete protection %q, want it on", tier, database.DeleteProtectionState)
	}
	runIndexes, err := service.Projects.Databases.CollectionGroups.Indexes.List(path + "/collectionGroups/runs").Context(ctx).Do()
	if err != nil || len(runIndexes.Indexes) < len(task.RunIndexes()) {
		t.Errorf("the runs have the indexes %+v (%v), want the %d they are listed through", runIndexes, err, len(task.RunIndexes()))
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

	if err := p.RemoveProject(ctx, tier, liveSlug, nil); err != nil {
		t.Fatalf("RemoveProject(%s, %s) = %v", tier, liveSlug, err)
	}
	if err := bootstrap.Remove(ctx, tier, nil); err != nil {
		t.Fatalf("Remove(%s) = %v", tier, err)
	}
	if _, err := service.Projects.Databases.Get(path).Context(ctx).Do(); err == nil {
		t.Errorf("the database %s still exists after its tier was removed", path)
	}
}

func TestProjectAZoneIsRefusedBeforeItsBootstrapIsReadNamingTheRegionItSitsIn(t *testing.T) {
	live(t)
	onRealGoogleCloud(t)

	zone := liveRegion() + "-f"
	inAZone := newProvider(t, gcp.Options{Project: liveProject(), Region: zone})
	var refused refusal.Refusal
	_, err := bootstrapOf(t, inAZone).Describe(context.Background(), environment.TierProduction)
	if !errors.As(err, &refused) || refused.Code != refusal.CodeInvalid {
		t.Fatalf("Describe() in the zone %s = %v, want an %s refusal", zone, err, refusal.CodeInvalid)
	}
	if !strings.Contains(refused.Message, fmt.Sprintf("Did you mean %q?", liveRegion())) {
		t.Errorf("Describe() refused with %q, want it to name %s", refused.Message, liveRegion())
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
	secrets, err := secretmanager.NewService(ctx, ports.EmulatorREST(endpoint())...)
	if err != nil {
		t.Fatalf("reach the emulator's secret manager: %v", err)
	}
	if _, err := secrets.Projects.Secrets.Delete("projects/" + liveProject() + "/secrets/" + liveNames(t).PassphraseSecret(tier)).Context(ctx).Do(); err != nil {
		t.Fatalf("delete the passphrase secret out of band: %v", err)
	}

	described, err := bootstrap.Describe(ctx, tier)
	if err != nil {
		t.Fatalf("Describe() = %v", err)
	}
	if len(described.Stacks) != 1 || described.Stacks[0].DigestCurrent {
		t.Errorf("Describe().Stacks = %+v with the passphrase secret gone, want the stack read as behind", described.Stacks)
	}

	plan, err := bootstrap.Plan(ctx, provider.BootstrapRequest{Tier: tier})
	if err != nil {
		t.Fatalf("Plan() = %v", err)
	}
	for _, group := range plan.Groups {
		if group.Kind != provider.ParameterGroupKind {
			continue
		}
		if group.Action == provider.ActionKeep {
			t.Errorf("Plan() shows %s as %q with the passphrase secret gone while its rows create it, and the group is what the operator consents to",
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
		t.Error("Describe() reads a bootstrap already partly taken down as finished, and nothing would ever go back to finish taking it down")
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

	writer := client.Bucket(liveTierBucket(t, tier, naming.StorageState)).Object(bootstrap.StampObject).NewWriter(ctx)
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

func TestLiveTheEdgeBucketIsRemovedSlowlyBecauseItIsEmptiedFirst(t *testing.T) {
	p := live(t)
	tier := environment.TierPreview
	installed := bootstrappedWith(t, p, tier, bootstrap.EdgeBucketPart)

	plan, err := installed.PlanRemove(context.Background(), tier)
	if err != nil {
		t.Fatalf("PlanRemove() = %v", err)
	}
	rows := rowsOf(t, plan)
	edge := rows["storage:bucket/"+liveNames(t).TierBucketStem(tier, naming.StorageEdge)]
	if edge.Action != provider.ActionDelete || !edge.Slow {
		t.Errorf("PlanRemove() shows the edge bucket as %q (slow %v), want a delete marked slow: every object in it is deleted one by one first",
			edge.Action, edge.Slow)
	}
	state := rows["storage:bucket/"+liveTierBucket(t, tier, naming.StorageState)]
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
	database := rowsOf(t, plan)["firestore:database/"+liveNames(t).RecordsDatabase(tier)]
	if database.Action != provider.ActionKeep || strings.Contains(database.Reason, string(environment.TierProduction)) {
		t.Errorf("Plan() shows the tier's records database as %q (%q), want it kept as its own tier's, naming no sibling",
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
	kept := map[environment.Tier]records.Key{}
	for _, each := range []environment.Tier{environment.TierProduction, tier} {
		kept[each] = records.Partition{Tier: each, Root: envsource.RootDigestKey}.Key("digestkey")
		if _, err := p.KeyValues().Write(ctx, records.Entry{Key: kept[each], Value: []byte(`{"sealed":"c2VhbGVk"}`)}); err != nil {
			t.Fatalf("Write(%s) = %v", kept[each], err)
		}
	}
	t.Cleanup(func() {
		if err := records.Forget(ctx, p.KeyValues(), kept[environment.TierProduction]); err != nil {
			t.Errorf("Forget(%s) = %v", kept[environment.TierProduction], err)
		}
	})

	if err := bootstrap.Remove(ctx, tier, nil); err != nil {
		t.Fatalf("Remove(%s) = %v", tier, err)
	}
	if _, err := p.KeyValues().Read(ctx, kept[tier]); !errors.Is(err, records.ErrNotFound) {
		t.Errorf("Read(%s) after its tier was removed = %v, want it gone: it was sealed under the key the removal destroyed, and the database outlives the tier while %s stays",
			kept[tier], err, environment.TierProduction)
	}
	if _, err := p.KeyValues().Read(ctx, kept[environment.TierProduction]); err != nil {
		t.Errorf("Read(%s) after the other tier was removed = %v, want it kept", kept[environment.TierProduction], err)
	}
}
