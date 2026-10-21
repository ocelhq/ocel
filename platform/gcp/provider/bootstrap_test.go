package gcp

import (
	"context"
	"errors"
	"net/http"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"cloud.google.com/go/kms/apiv1/kmspb"
	"cloud.google.com/go/storage"
	"google.golang.org/api/artifactregistry/v1"
	"google.golang.org/api/cloudresourcemanager/v1"
	storagejson "google.golang.org/api/storage/v1"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/progress"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/fake"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/pkg/stackrecords"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/bootstrap"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/gcloud"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/googletest"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/names"
)

func TestTheRepositoryPrunesOnlyUntaggedImagesOlderThanAWeek(t *testing.T) {
	t.Parallel()

	policies := bootstrap.ImageRepository().CleanupPolicies
	if len(policies) != 1 {
		t.Fatalf("the repository is created under %d cleanup policies, want the one: a KEEP window beside a DELETE lets the window fill with tagged versions and the DELETE take every untagged one", len(policies))
	}
	policy, named := policies[bootstrap.DropUntaggedPolicy]
	if !named {
		t.Fatalf("the repository is created under %v, want a policy named %q", policies, bootstrap.DropUntaggedPolicy)
	}
	if policy.Action != bootstrap.DeleteImages {
		t.Errorf("the %s policy acts %q, want %q", bootstrap.DropUntaggedPolicy, policy.Action, bootstrap.DeleteImages)
	}
	if policy.Condition == nil {
		t.Fatalf("the %s policy names no condition, and one that matches everything deletes everything", bootstrap.DropUntaggedPolicy)
	}
	if policy.Condition.TagState != bootstrap.UntaggedImages {
		t.Errorf("the %s policy matches tag state %q, want %q: a tagged image is one a service still runs", bootstrap.DropUntaggedPolicy, policy.Condition.TagState, bootstrap.UntaggedImages)
	}
	if policy.Condition.OlderThan != bootstrap.UntaggedLifetime {
		t.Errorf("the %s policy matches versions older than %q, want %q: without it an in-flight push's untagged child manifests are deleted under it",
			bootstrap.DropUntaggedPolicy, policy.Condition.OlderThan, bootstrap.UntaggedLifetime)
	}
}

func TestTheRepositoryIsCreatedUnderAModeAnOrgPolicyCanAllow(t *testing.T) {
	t.Parallel()

	if got := bootstrap.ImageRepository().Mode; got != bootstrap.StandardImages {
		t.Errorf("the repository is created in %q mode, want %q: unset reads as MODE_UNSPECIFIED, and an org enforcing disallowUnspecifiedMode refuses the create outright",
			got, bootstrap.StandardImages)
	}
}

func TestARepositoryThatDriftedFromWhatTheBootstrapNamesIsMended(t *testing.T) {
	t.Parallel()

	desired := bootstrap.ImageRepository()
	for name, tc := range map[string]struct {
		existing *artifactregistry.Repository
		mends    bool
	}{
		"the repository this bootstrap made": {existing: desired},
		"one whose policies were edited away": {
			existing: &artifactregistry.Repository{Format: bootstrap.DockerImages, Mode: bootstrap.StandardImages},
			mends:    true,
		},
		"one with a policy nothing here named": {
			existing: &artifactregistry.Repository{
				Format: bootstrap.DockerImages, Mode: bootstrap.StandardImages,
				CleanupPolicies: map[string]artifactregistry.CleanupPolicy{
					bootstrap.DropUntaggedPolicy: desired.CleanupPolicies[bootstrap.DropUntaggedPolicy],
					"keep-recent":                {Id: "keep-recent", Action: "KEEP"},
				},
			},
			mends: true,
		},
		"one pruning untagged images the moment they are pushed": {
			existing: &artifactregistry.Repository{
				Format: bootstrap.DockerImages, Mode: bootstrap.StandardImages,
				CleanupPolicies: map[string]artifactregistry.CleanupPolicy{
					bootstrap.DropUntaggedPolicy: {
						Id: bootstrap.DropUntaggedPolicy, Action: bootstrap.DeleteImages,
						Condition: &artifactregistry.CleanupPolicyCondition{TagState: bootstrap.UntaggedImages},
					},
				},
			},
			mends: true,
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			found, err := bootstrap.RepositoryPresenceOf("ocel-acme-prod-production", tc.existing)
			if err != nil {
				t.Fatalf("repositoryPresenceOf() = %v, want a verdict on a DOCKER repository", err)
			}
			if !found.Present {
				t.Error("a repository that exists reads as absent, and the bootstrap would try to create it again")
			}
			if mends := found.Mends != ""; mends != tc.mends {
				t.Errorf("repositoryPresenceOf() mends %q, want mending=%t: what the survey does not report, the apply does not do", found.Mends, tc.mends)
			}
		})
	}
}

func TestARepositoryOfAnotherFormatIsRefusedRatherThanMended(t *testing.T) {
	t.Parallel()

	var refused refusal.Refusal
	_, err := bootstrap.RepositoryPresenceOf("ocel-acme-prod-production", &artifactregistry.Repository{Format: "MAVEN"})
	if !errors.As(err, &refused) || refused.Code != refusal.CodeInvalid {
		t.Fatalf("repositoryPresenceOf() over a MAVEN repository = %v, want an %s refusal: Artifact Registry never changes a format, so no patch mends this", err, refusal.CodeInvalid)
	}
}

func TestRemovingAKeyDestroysEveryVersionItStillHas(t *testing.T) {
	t.Parallel()

	key := "projects/acme-prod/locations/europe-west1/keyRings/ocel/cryptoKeys/production/cryptoKeyVersions/"
	named := bootstrap.Destroyable([]*kmspb.CryptoKeyVersion{
		{Name: key + "1", State: kmspb.CryptoKeyVersion_DESTROYED},
		{Name: key + "2", State: kmspb.CryptoKeyVersion_ENABLED},
		{Name: key + "3", State: kmspb.CryptoKeyVersion_DISABLED},
		{Name: key + "4", State: kmspb.CryptoKeyVersion_DESTROY_SCHEDULED},
		{Name: key + "5", State: kmspb.CryptoKeyVersion_PENDING_GENERATION},
		{Name: key + "6", State: kmspb.CryptoKeyVersion_ENABLED},
	})
	if want := []string{key + "2", key + "3", key + "6"}; !slices.Equal(named, want) {
		t.Errorf("removal destroys %v, want %v: every bootstrap after the first minted a version, and a version left enabled keeps billing and keeps opening what it sealed",
			named, want)
	}
}

func TestABucketIsCreatedUnderUniformAccessWithPublicAccessPrevented(t *testing.T) {
	t.Parallel()

	for _, class := range names.TierStorageClasses {
		attrs := bootstrap.BucketAttrs("europe-west1", bootstrap.Resource{Kind: bootstrap.KindBucket, Class: class})
		if !attrs.UniformBucketLevelAccess.Enabled {
			t.Errorf("the %s bucket is created with object ACLs live, and an ACL on one object would reach past the IAM the bootstrap grants", class)
		}
		if attrs.PublicAccessPrevention != storage.PublicAccessPreventionEnforced {
			t.Errorf("the %s bucket is created under public access prevention %v, want it enforced: an allUsers grant would publish every object and stack state in it",
				class, attrs.PublicAccessPrevention)
		}
		if attrs.Location != "europe-west1" {
			t.Errorf("the %s bucket is created in %q, want the region the bootstrap names", class, attrs.Location)
		}
	}
}

func TestTheStateBucketKeepsAReplacedVersionAWeekAndNoLonger(t *testing.T) {
	t.Parallel()

	attrs := bootstrap.BucketAttrs("europe-west1", bootstrap.Resource{Kind: bootstrap.KindBucket, Class: naming.StorageState})
	if !attrs.VersioningEnabled {
		t.Error("the state bucket is created unversioned, so a bad state write is unrecoverable")
	}
	want := []storage.LifecycleRule{{
		Action:    storage.LifecycleAction{Type: storage.DeleteAction},
		Condition: storage.LifecycleCondition{DaysSinceNoncurrentTime: 7},
	}}
	if !reflect.DeepEqual(attrs.Lifecycle.Rules, want) {
		t.Errorf("the state bucket's lifecycle = %+v, want %+v: a version replaced by every deploy would otherwise be billed forever", attrs.Lifecycle.Rules, want)
	}
}

func TestTheCacheBucketDeletesAFetchOrUseCacheEntryThirtyDaysAfterItsLastWriteAndNeverTheTagClock(t *testing.T) {
	t.Parallel()

	attrs := bootstrap.BucketAttrs("europe-west1", bootstrap.Resource{Kind: bootstrap.KindBucket, Class: naming.StorageCache})
	want := []storage.LifecycleRule{{
		Action:    storage.LifecycleAction{Type: storage.DeleteAction},
		Condition: storage.LifecycleCondition{AgeInDays: 30, MatchesPrefix: []string{"fetch-cache/", "use-cache/"}},
	}}
	if !reflect.DeepEqual(attrs.Lifecycle.Rules, want) || attrs.VersioningEnabled {
		t.Errorf("the cache bucket is created as versioned=%t with lifecycle %+v, want unversioned with %+v: the tag clock is rewritten only on a revalidation, "+
			"and once it expired a revalidated tag would reach no other instance",
			attrs.VersioningEnabled, attrs.Lifecycle.Rules, want)
	}
}

func TestACacheBucketWhoseRuleDeletesEveryObjectIsMended(t *testing.T) {
	t.Parallel()

	made := bootstrap.BucketAttrs("europe-west1", bootstrap.Resource{Kind: bootstrap.KindBucket, Class: naming.StorageCache})
	everything := *made
	everything.Lifecycle = storage.Lifecycle{Rules: []storage.LifecycleRule{{
		Action:    storage.LifecycleAction{Type: storage.DeleteAction},
		Condition: storage.LifecycleCondition{AgeInDays: 30},
	}}}
	if found := bootstrap.BucketPresenceOf(&everything, naming.StorageCache, false); found.Mends == "" {
		t.Error("a cache bucket that deletes its tag clock after 30 days reads as current, and the bootstrap would leave it deleting it")
	}
}

func TestTheEdgeBucketDeletesNothingByAge(t *testing.T) {
	t.Parallel()

	attrs := bootstrap.BucketAttrs("europe-west1", bootstrap.Resource{Kind: bootstrap.KindBucket, Class: naming.StorageEdge})
	if len(attrs.Lifecycle.Rules) != 0 || attrs.VersioningEnabled {
		t.Errorf("the edge bucket is created as versioned=%t with lifecycle %+v, want neither: a release's static files live as long as the release", attrs.VersioningEnabled, attrs.Lifecycle.Rules)
	}
}

func TestABucketThatDriftedIsMended(t *testing.T) {
	t.Parallel()

	made := bootstrap.BucketAttrs("europe-west1", bootstrap.Resource{Kind: bootstrap.KindBucket, Class: naming.StorageCache})
	for name, tc := range map[string]struct {
		existing *storage.BucketAttrs
		mends    bool
	}{
		"the bucket this bootstrap made": {existing: made},
		"one whose access went back to ACLs": {
			existing: &storage.BucketAttrs{PublicAccessPrevention: storage.PublicAccessPreventionEnforced, Lifecycle: made.Lifecycle},
			mends:    true,
		},
		"one that may be granted to allUsers": {
			existing: &storage.BucketAttrs{
				UniformBucketLevelAccess: storage.UniformBucketLevelAccess{Enabled: true},
				PublicAccessPrevention:   storage.PublicAccessPreventionInherited,
				Lifecycle:                made.Lifecycle,
			},
			mends: true,
		},
		"one whose lifecycle was cleared": {
			existing: &storage.BucketAttrs{
				UniformBucketLevelAccess: storage.UniformBucketLevelAccess{Enabled: true},
				PublicAccessPrevention:   storage.PublicAccessPreventionEnforced,
			},
			mends: true,
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			found := bootstrap.BucketPresenceOf(tc.existing, naming.StorageCache, false)
			if !found.Present {
				t.Error("a bucket that exists reads as absent, and the bootstrap would try to create it again")
			}
			if mends := found.Mends != ""; mends != tc.mends {
				t.Errorf("bucketPresenceOf() mends %q, want mending=%t: what the survey does not report, the apply does not do", found.Mends, tc.mends)
			}
			if emulated := bootstrap.BucketPresenceOf(tc.existing, naming.StorageCache, true); emulated.Mends != "" {
				t.Errorf("bucketPresenceOf() under the emulator mends %q, and the emulator keeps no access configuration to mend", emulated.Mends)
			}
		})
	}
}

func TestEveryBootstrapRemovalStepSaysWhatItTookOrKeptAndWhy(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		removal bootstrap.Removal
		want    string
	}{
		{
			removal: bootstrap.Removal{Resource: bootstrap.Resource{Kind: bootstrap.KindBucket, Name: "ocel-prod-edge"}, Action: provider.ActionDelete},
			want:    "INFO Removed bucket ocel-prod-edge",
		},
		{
			removal: bootstrap.Removal{Resource: bootstrap.Resource{Kind: bootstrap.KindDatabase, Name: "ocel"}, Action: provider.ActionDisableThenDelete},
			want:    "INFO Removed Firestore database ocel",
		},
		{
			removal: bootstrap.Removal{Resource: bootstrap.Resource{Kind: bootstrap.KindKey, Name: "production"}, Action: provider.ActionDelete, Reason: bootstrap.ReasonKeyScheduled},
			want:    "INFO Scheduled every version of KMS key production for destruction, which Cloud KMS carries out after 30 days, its default",
		},
		{
			removal: bootstrap.Removal{Resource: bootstrap.Resource{Kind: bootstrap.KindKey, Name: "production"}, Action: provider.ActionDelete, Reason: bootstrap.ReasonKeyScheduled, DestroyAfter: 24 * time.Hour},
			want:    "INFO Scheduled every version of KMS key production for destruction, which Cloud KMS carries out after 24 hours, the key's own period",
		},
		{
			removal: bootstrap.Removal{Resource: bootstrap.Resource{Kind: bootstrap.KindKey, Name: "production"}, Action: provider.ActionDelete, Reason: bootstrap.ReasonKeyScheduled, DestroyAfter: 7 * 24 * time.Hour},
			want:    "INFO Scheduled every version of KMS key production for destruction, which Cloud KMS carries out after 7 days, the key's own period",
		},
		{
			removal: bootstrap.Removal{Resource: bootstrap.Resource{Kind: bootstrap.KindKeyRing, Name: "ocel"}, Action: provider.ActionKeep, Reason: bootstrap.ReasonRingKept},
			want:    "INFO Kept KMS key ring ocel: " + bootstrap.ReasonRingKept,
		},
		{
			removal: bootstrap.Removal{Resource: bootstrap.Resource{Kind: bootstrap.KindSecret, Name: "ocel-production"}, Action: provider.ActionKeep, Reason: bootstrap.ReasonAbsent},
			want:    "DEBUG Nothing to remove: secret ocel-production does not exist",
		},
	} {
		progress := &fake.Log{}
		tc.removal.Report(progress)
		if got := progress.Lines(); !slices.Equal(got, []string{tc.want}) {
			t.Errorf("removing %s said %q, want %q", tc.removal.Resource.ID(), got, tc.want)
		}
	}
}

func TestABootstrapResourceAlreadyCurrentIsNotedOnlyAtDebug(t *testing.T) {
	t.Parallel()

	target := bootstrap.Resource{Kind: bootstrap.KindRepository, Name: "ocel-acme-prod-production"}
	read := bootstrap.Survey{Held: map[string]bool{target.ID(): true}}
	progress := &fake.Log{}

	if err := (composed(bootstrap.Engine{})).Provision(context.Background(), read, target, progress); err != nil {
		t.Fatalf("provision() = %v", err)
	}
	want := []string{"DEBUG The Artifact Registry repository ocel-acme-prod-production is already current"}
	if got := progress.Lines(); !slices.Equal(got, want) {
		t.Errorf("provision() said %q, want %q", got, want)
	}
}

func TestRemovingATierDeletesItsOwnRecordsDatabaseWhileItsSiblingIsInstalled(t *testing.T) {
	t.Parallel()

	names := names.New("ocel", "acme-prod")
	tier := environment.TierProduction
	read := bootstrap.Survey{Tier: tier, Names: names, Held: map[string]bool{}, Sibling: true}
	for _, each := range bootstrap.Resources(names, tier, false) {
		read.Held[each.ID()] = true
	}

	actions := map[string]provider.ChangeAction{}
	for _, taking := range bootstrap.Removals(read) {
		if taking.Resource.Kind == bootstrap.KindDatabase {
			actions[taking.Resource.Name] = taking.Action
		}
	}
	if got := actions[names.RecordsDatabase(tier)]; got != provider.ActionDisableThenDelete {
		t.Errorf("removing a tier whose sibling is installed does %s to the tier's records database, want it deleted: the sibling keeps its records in its own", got)
	}
	if _, named := actions[names.RecordsDatabase(tier.Sibling())]; named {
		t.Errorf("removing the %s tier names the %s tier's records database", tier, tier.Sibling())
	}
}

func replacingTheTierKey(t *testing.T) (bootstrap.Engine, *googletest.IAM) {
	t.Helper()
	server := googletest.GrantedIAM()
	b := composed(bootstrap.Engine{Clients: server.Open(t), Records: fake.NewKeyValues()})
	server.Destroyed = map[string]bool{gcloud.KeyPath(b.Clients, string(environment.TierProduction)): true}
	return b, server
}

func TestADeployAddingTheKeysPartRefusesToReplaceATierKeyWhosePrimaryIsDestroyed(t *testing.T) {
	t.Parallel()
	b, server := replacingTheTierKey(t)

	err := b.EnsurePart(t.Context(), environment.TierProduction, "shop", bootstrap.Keys.For(b, environment.TierProduction))

	var refused refusal.Refusal
	if !errors.As(err, &refused) || refused.Code != refusal.CodeNotReady || !strings.Contains(err.Error(), "KMS key production") || !strings.Contains(err.Error(), "`ocel bootstrap production --yes`") {
		t.Fatalf("EnsurePart() = %v, want a %s refusal naming the KMS key it would replace and the bootstrap that consents to it", err, refusal.CodeNotReady)
	}
	if len(server.Minted) != 0 {
		t.Errorf("the deploy minted %v before refusing, want nothing", server.Minted)
	}
}

func TestTheBootstrapARefusedDeployNamesReplacesATierKeyWhosePartIsNotRecorded(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	tier := environment.TierProduction
	b, server := replacingTheTierKey(t)
	if err := b.EnsurePart(ctx, tier, "shop", bootstrap.Keys.For(b, tier)); err == nil || !strings.Contains(err.Error(), "`ocel bootstrap production --yes`") {
		t.Fatalf("the deploy's EnsurePart() = %v, want the refusal naming the consenting bootstrap", err)
	}
	if _, present, err := stackrecords.ReadBootstrapPart(ctx, b.Records, tier, bootstrap.KeysPart); err != nil || present {
		t.Fatalf("the keys part is recorded after the refusal: %t, %v, want no record, so only the bootstrap's own survey finds the key", present, err)
	}

	replaced, err := b.SurveyReplacedParts(ctx, bootstrap.Survey{Tier: tier}, nil)
	if err != nil {
		t.Fatalf("SurveyReplacedParts() = %v", err)
	}
	if !slices.Equal(replaced.Names(), []string{bootstrap.KeysPart}) || !slices.ContainsFunc(replaced.Replacing(), func(phrase string) bool { return strings.Contains(phrase, "KMS key production") }) {
		t.Fatalf("SurveyReplacedParts() names %v replacing %v, want the keys part replacing KMS key production", replaced.Names(), replaced.Replacing())
	}
	planned := replaced.Planned(b)
	if len(planned) != 1 || planned[0].Part != bootstrap.KeysPart || !slices.ContainsFunc(planned[0].Changes, func(change provider.Change) bool {
		return change.Name == string(tier) && change.Action == provider.ActionReplace
	}) {
		t.Errorf("the bootstrap plans %+v, want the keys part replacing the production key, so the consent covers it", planned)
	}
	if err := b.ReplaceParts(ctx, tier, replaced); err != nil {
		t.Fatalf("ReplaceParts() = %v", err)
	}
	if want := []string{gcloud.KeyPath(b.Clients, string(tier))}; !slices.Equal(server.Minted, want) {
		t.Errorf("`ocel bootstrap production --yes` minted versions of %v, want one of %v", server.Minted, want)
	}
	if _, present, err := stackrecords.ReadBootstrapPart(ctx, b.Records, tier, bootstrap.KeysPart); err != nil || !present {
		t.Errorf("the keys part is recorded %t, %v after the bootstrap replaced its key, want it recorded", present, err)
	}
}

func TestABootstrapRepairingARecordedKeysPartMintsANewPrimaryOnlyWithConsent(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	tier := environment.TierProduction
	b, server := replacingTheTierKey(t)
	if err := stackrecords.MarkBootstrapPartReady(ctx, b.Records, tier, bootstrap.KeysPart, stackrecords.BootstrapPart{WrittenBy: string(b.WrittenBy)}); err != nil {
		t.Fatal(err)
	}

	if _, err := b.RepairParts(ctx, tier, false); err == nil || len(server.Minted) != 0 {
		t.Errorf("RepairParts() without consent = %v and minted %v, want a refusal and nothing minted", err, server.Minted)
	}
	if _, err := b.RepairParts(ctx, tier, true); err != nil {
		t.Fatalf("RepairParts() with consent = %v", err)
	}
	if want := []string{gcloud.KeyPath(b.Clients, string(tier))}; !slices.Equal(server.Minted, want) {
		t.Errorf("minted versions of %v, want one of %v", server.Minted, want)
	}
}

func bootstrappingOver(t *testing.T, server *googletest.IAM, installed ...string) bootstrap.Engine {
	t.Helper()
	rest := server.Rest(t)
	clients := server.Serve(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1/projects/"+googletest.Project+":testIamPermissions":
			if asked := googletest.ReadBody[cloudresourcemanager.TestIamPermissionsRequest](w, r); asked != nil {
				googletest.WriteBody(w, &cloudresourcemanager.TestIamPermissionsResponse{Permissions: asked.Permissions})
			}
		case r.Method == http.MethodPost && strings.HasPrefix(r.URL.Path, "/upload/storage/v1/b/"):
			googletest.WriteBody(w, &storagejson.Object{Name: bootstrap.StampObject, Generation: 1})
		default:
			rest(w, r)
		}
	})
	b := composed(bootstrap.Engine{
		Clients:  clients,
		Records:  fake.NewKeyValues(),
		Surveyed: &bootstrap.Surveys{},
		Edges:    &frontRegistry{front: &countingFront{installed: true}},
	})
	b.SweepAccounts = func(context.Context, bootstrap.Engine, provider.BootstrapRequest, progress.Log) error { return nil }
	read := surveyed(installed...)
	read.Region, read.Emulated, read.Held = clients.Region, true, map[string]bool{}
	for _, item := range bootstrap.Resources(read.Names, read.Tier, read.Emulated) {
		read.Held[item.ID()] = true
	}
	b.Surveyed.Remember(read)
	return b
}

func destroyingTheTierKey(t *testing.T) (bootstrap.Engine, *googletest.IAM) {
	t.Helper()
	server := googletest.GrantedIAM()
	b := bootstrappingOver(t, server)
	server.Destroyed = map[string]bool{gcloud.KeyPath(b.Clients, string(environment.TierProduction)): true}
	return b, server
}

func TestABootstrapPlanShowsTheKeysPartReplacingADestroyedTierKeyNoPartRecordNames(t *testing.T) {
	t.Parallel()
	b, _ := destroyingTheTierKey(t)

	plan, err := b.Plan(t.Context(), provider.BootstrapRequest{Tier: environment.TierProduction})
	if err != nil {
		t.Fatalf("Plan() = %v", err)
	}

	if !slices.ContainsFunc(plan.Groups, func(group provider.ChangeGroup) bool {
		return group.Part == bootstrap.KeysPart && slices.ContainsFunc(group.Changes, func(change provider.Change) bool {
			return change.Name == string(environment.TierProduction) && change.Action == provider.ActionReplace
		})
	}) {
		t.Errorf("Plan() = %+v, want the keys part replacing the production key, so the consent it asks for covers it", plan.Groups)
	}
}

func TestABootstrapRefusingReplacementsRefusesToReplaceADestroyedTierKeyBeforeItProvisionsAnything(t *testing.T) {
	t.Parallel()
	b, server := destroyingTheTierKey(t)

	err := b.Apply(t.Context(), provider.BootstrapRequest{Tier: environment.TierProduction, RefuseReplacements: true}, nil)

	var refused refusal.Refusal
	if !errors.As(err, &refused) || !strings.Contains(refused.Message, "KMS key production") || !strings.Contains(refused.Message, "--yes") {
		t.Fatalf("Apply() refusing replacements = %v, want a refusal naming the KMS key it would replace and --yes", err)
	}
	if len(server.Minted) != 0 {
		t.Errorf("Apply() refusing replacements minted %v, want nothing", server.Minted)
	}
}

func TestABootstrapConsentingToReplacementsMintsANewPrimaryForADestroyedTierKeyNoPartRecordNames(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	tier := environment.TierProduction
	b, server := destroyingTheTierKey(t)

	if err := b.Apply(ctx, provider.BootstrapRequest{Tier: tier}, nil); err != nil {
		t.Fatalf("Apply() = %v", err)
	}

	if want := []string{gcloud.KeyPath(b.Clients, string(tier))}; !slices.Equal(server.Minted, want) {
		t.Errorf("`ocel bootstrap production --yes` minted versions of %v, want one of %v", server.Minted, want)
	}
	if _, present, err := stackrecords.ReadBootstrapPart(ctx, b.Records, tier, bootstrap.KeysPart); err != nil || !present {
		t.Errorf("the keys part is recorded %t, %v after the bootstrap replaced its key, want it recorded", present, err)
	}
}

func TestABootstrapRemovingTheKeysPartNeitherPlansNorRefusesNorMakesReplacingItsDestroyedKey(t *testing.T) {
	t.Parallel()
	tier := environment.TierProduction
	removing := provider.BootstrapRequest{Tier: tier, Remove: []string{bootstrap.KeysPart}}

	b, _ := destroyingTheTierKey(t)
	plan, err := b.Plan(t.Context(), removing)
	if err != nil {
		t.Fatalf("Plan() removing keys = %v", err)
	}
	if at := slices.IndexFunc(plan.Groups, func(group provider.ChangeGroup) bool { return group.Part == bootstrap.KeysPart }); at >= 0 {
		t.Errorf("Plan() removing keys plans %+v, want no keys part replaced by the run that removes it", plan.Groups[at])
	}

	for _, consented := range []bool{false, true} {
		b, server := destroyingTheTierKey(t)
		removing.RefuseReplacements = !consented
		if err := b.Apply(t.Context(), removing, nil); err != nil {
			t.Errorf("Apply() removing keys (consented %t) = %v, want nothing refused: the run replaces nothing it removes", consented, err)
		}
		if len(server.Minted) != 0 {
			t.Errorf("Apply() removing keys (consented %t) minted %v, want nothing: the run removes the keys part", consented, server.Minted)
		}
		if _, present, err := stackrecords.ReadBootstrapPart(t.Context(), b.Records, tier, bootstrap.KeysPart); err != nil || present {
			t.Errorf("the keys part is recorded %t, %v after a run removing it, want no record", present, err)
		}
	}
}

func TestABootstrapRefusingReplacementsGoesAheadWhenNothingWouldBeReplaced(t *testing.T) {
	t.Parallel()

	read := bootstrap.Survey{Tier: environment.TierProduction, Names: names.New("ocel", "acme-prod"), Held: map[string]bool{}}

	if err := bootstrap.RefuseReplacements(read, bootstrap.Resources(read.Names, read.Tier, false), "re-run with --yes"); err != nil {
		t.Errorf("refuseReplacements() over a tier that replaces nothing = %v, want nil: making what is missing and mending what drifted replace nothing", err)
	}
}
