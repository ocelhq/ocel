package gcp

import (
	"errors"
	"slices"
	"testing"

	"cloud.google.com/go/kms/apiv1/kmspb"
	"cloud.google.com/go/storage"
	"google.golang.org/api/artifactregistry/v1"

	"github.com/ocelhq/ocel/pkg/providerkit/refusal"
)

func TestTheRepositoryPrunesOnlyUntaggedImagesOlderThanAWeek(t *testing.T) {
	t.Parallel()

	policies := imageRepository().CleanupPolicies
	if len(policies) != 1 {
		t.Fatalf("the repository is created under %d cleanup policies, want the one: a KEEP window beside a DELETE lets the window fill with tagged versions and the DELETE take every untagged one", len(policies))
	}
	policy, named := policies[dropUntaggedPolicy]
	if !named {
		t.Fatalf("the repository is created under %v, want a policy named %q", policies, dropUntaggedPolicy)
	}
	if policy.Action != deleteImages {
		t.Errorf("the %s policy acts %q, want %q", dropUntaggedPolicy, policy.Action, deleteImages)
	}
	if policy.Condition == nil {
		t.Fatalf("the %s policy names no condition, and one that matches everything deletes everything", dropUntaggedPolicy)
	}
	if policy.Condition.TagState != untaggedImages {
		t.Errorf("the %s policy matches tag state %q, want %q: a tagged image is one a service still runs", dropUntaggedPolicy, policy.Condition.TagState, untaggedImages)
	}
	if policy.Condition.OlderThan != untaggedLifetime {
		t.Errorf("the %s policy matches versions older than %q, want %q: without it an in-flight push's untagged child manifests are deleted under it",
			dropUntaggedPolicy, policy.Condition.OlderThan, untaggedLifetime)
	}
}

func TestTheRepositoryIsCreatedUnderAModeAnOrgPolicyCanAllow(t *testing.T) {
	t.Parallel()

	if got := imageRepository().Mode; got != standardImages {
		t.Errorf("the repository is created in %q mode, want %q: unset reads as MODE_UNSPECIFIED, and an org enforcing disallowUnspecifiedMode refuses the create outright",
			got, standardImages)
	}
}

func TestARepositoryThatDriftedFromWhatTheBootstrapNamesIsMended(t *testing.T) {
	t.Parallel()

	desired := imageRepository()
	for name, tc := range map[string]struct {
		existing *artifactregistry.Repository
		mends    bool
	}{
		"the repository this bootstrap made": {existing: desired},
		"one whose policies were edited away": {
			existing: &artifactregistry.Repository{Format: dockerImages, Mode: standardImages},
			mends:    true,
		},
		"one with a policy nothing here named": {
			existing: &artifactregistry.Repository{
				Format: dockerImages, Mode: standardImages,
				CleanupPolicies: map[string]artifactregistry.CleanupPolicy{
					dropUntaggedPolicy: desired.CleanupPolicies[dropUntaggedPolicy],
					"keep-recent":      {Id: "keep-recent", Action: "KEEP"},
				},
			},
			mends: true,
		},
		"one pruning untagged images the moment they are pushed": {
			existing: &artifactregistry.Repository{
				Format: dockerImages, Mode: standardImages,
				CleanupPolicies: map[string]artifactregistry.CleanupPolicy{
					dropUntaggedPolicy: {
						Id: dropUntaggedPolicy, Action: deleteImages,
						Condition: &artifactregistry.CleanupPolicyCondition{TagState: untaggedImages},
					},
				},
			},
			mends: true,
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			found, err := repositoryPresenceOf("ocel-acme-prod-production", tc.existing)
			if err != nil {
				t.Fatalf("repositoryPresenceOf() = %v, want a verdict on a DOCKER repository", err)
			}
			if !found.present {
				t.Error("a repository that exists reads as absent, and the bootstrap would try to create it again")
			}
			if mends := found.mends != ""; mends != tc.mends {
				t.Errorf("repositoryPresenceOf() mends %q, want mending=%t: what the survey does not report, the apply does not do", found.mends, tc.mends)
			}
		})
	}
}

func TestARepositoryOfAnotherFormatIsRefusedRatherThanMended(t *testing.T) {
	t.Parallel()

	var refused refusal.Refusal
	_, err := repositoryPresenceOf("ocel-acme-prod-production", &artifactregistry.Repository{Format: "MAVEN"})
	if !errors.As(err, &refused) || refused.Code != refusal.CodeInvalid {
		t.Fatalf("repositoryPresenceOf() over a MAVEN repository = %v, want an %s refusal: Artifact Registry never changes a format, so no patch mends this", err, refusal.CodeInvalid)
	}
}

func TestRemovingAKeyDestroysEveryVersionItStillHas(t *testing.T) {
	t.Parallel()

	key := "projects/acme-prod/locations/europe-west1/keyRings/ocel/cryptoKeys/production/cryptoKeyVersions/"
	named := destroyable([]*kmspb.CryptoKeyVersion{
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

	attrs := bucketAttrs("europe-west1", item{Kind: KindBucket, Name: "ocel-acme-prod-production-state", Versioned: true})
	if !attrs.UniformBucketLevelAccess.Enabled {
		t.Error("the bucket is created with object ACLs live, and an ACL on one object would reach past the IAM the bootstrap grants")
	}
	if attrs.PublicAccessPrevention != storage.PublicAccessPreventionEnforced {
		t.Errorf("the bucket is created under public access prevention %v, want it enforced: an allUsers grant would publish every artifact and stack state in it",
			attrs.PublicAccessPrevention)
	}
	if !attrs.VersioningEnabled || attrs.Location != "europe-west1" {
		t.Errorf("the bucket is created as %+v, want the versioning and location the item names kept", attrs)
	}
}

func TestABucketThatDriftedOpenIsMended(t *testing.T) {
	t.Parallel()

	for name, tc := range map[string]struct {
		existing *storage.BucketAttrs
		mends    bool
	}{
		"the bucket this bootstrap made": {existing: bucketAttrs("europe-west1", item{Kind: KindBucket})},
		"one whose access went back to ACLs": {
			existing: &storage.BucketAttrs{PublicAccessPrevention: storage.PublicAccessPreventionEnforced},
			mends:    true,
		},
		"one that may be granted to allUsers": {
			existing: &storage.BucketAttrs{
				UniformBucketLevelAccess: storage.UniformBucketLevelAccess{Enabled: true},
				PublicAccessPrevention:   storage.PublicAccessPreventionInherited,
			},
			mends: true,
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			found := bucketPresenceOf(tc.existing, false)
			if !found.present {
				t.Error("a bucket that exists reads as absent, and the bootstrap would try to create it again")
			}
			if mends := found.mends != ""; mends != tc.mends {
				t.Errorf("bucketPresenceOf() mends %q, want mending=%t: what the survey does not report, the apply does not do", found.mends, tc.mends)
			}
			if emulated := bucketPresenceOf(tc.existing, true); emulated.mends != "" {
				t.Errorf("bucketPresenceOf() under the emulator mends %q, and the emulator keeps no access configuration to mend", emulated.mends)
			}
		})
	}
}
