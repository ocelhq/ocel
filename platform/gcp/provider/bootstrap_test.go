package gcp

import (
	"errors"
	"testing"

	"google.golang.org/api/artifactregistry/v1"

	"github.com/ocelhq/ocel/pkg/providerkit"
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
		t.Errorf("the repository is created in %q mode, want %q: unset reads as MODE_UNSPECIFIED, and an org holding disallowUnspecifiedMode refuses the create outright",
			got, standardImages)
	}
}

func TestARepositoryThatDriftedFromWhatTheBootstrapNamesIsMended(t *testing.T) {
	t.Parallel()

	desired := imageRepository()
	for name, tc := range map[string]struct {
		held  *artifactregistry.Repository
		mends bool
	}{
		"the repository this bootstrap made": {held: desired},
		"one whose policies were edited away": {
			held:  &artifactregistry.Repository{Format: dockerImages, Mode: standardImages},
			mends: true,
		},
		"one holding a policy nothing here named": {
			held: &artifactregistry.Repository{
				Format: dockerImages, Mode: standardImages,
				CleanupPolicies: map[string]artifactregistry.CleanupPolicy{
					dropUntaggedPolicy: desired.CleanupPolicies[dropUntaggedPolicy],
					"keep-recent":      {Id: "keep-recent", Action: "KEEP"},
				},
			},
			mends: true,
		},
		"one pruning untagged images the moment they are pushed": {
			held: &artifactregistry.Repository{
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

			stands, err := repositoryStanding("ocel-acme-prod-production", tc.held)
			if err != nil {
				t.Fatalf("repositoryStanding() = %v, want a verdict on a DOCKER repository", err)
			}
			if !stands.held {
				t.Error("a repository that stands reads as absent, and the bootstrap would try to create it again")
			}
			if mends := stands.mends != ""; mends != tc.mends {
				t.Errorf("repositoryStanding() mends %q, want mending=%t: what the survey does not report, the apply does not do", stands.mends, tc.mends)
			}
		})
	}
}

func TestARepositoryOfAnotherFormatIsRefusedRatherThanMended(t *testing.T) {
	t.Parallel()

	var refusal providerkit.Refusal
	_, err := repositoryStanding("ocel-acme-prod-production", &artifactregistry.Repository{Format: "MAVEN"})
	if !errors.As(err, &refusal) || refusal.Code != providerkit.CodeInvalid {
		t.Fatalf("repositoryStanding() over a MAVEN repository = %v, want an %s refusal: Artifact Registry never changes a format, so no patch mends this", err, providerkit.CodeInvalid)
	}
}
