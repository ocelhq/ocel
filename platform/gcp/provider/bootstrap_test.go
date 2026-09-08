package gcp

import "testing"

func TestTheRepositoryPrunesOnlyUntaggedImagesOlderThanAWeek(t *testing.T) {
	t.Parallel()

	policies := pruningUntaggedImages()
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
