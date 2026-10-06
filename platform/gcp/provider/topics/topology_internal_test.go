package topics

import (
	"slices"
	"testing"

	pubsub "google.golang.org/api/pubsub/v1"
)

func TestGivingAMemberARoleLeavesTheBindingsItWasGivenUnchanged(t *testing.T) {
	t.Parallel()
	const role, member, other = "roles/pubsub.publisher", "serviceAccount:a@x", "serviceAccount:b@x"
	given := []*pubsub.Binding{{Role: role, Members: []string{other}}}

	got := withMember(given, role, member)

	if len(got) != 1 || !slices.Equal(got[0].Members, []string{other, member}) {
		t.Fatalf("withMember() = %+v, want the role held by both members", got)
	}
	if !slices.Equal(given[0].Members, []string{other}) {
		t.Errorf("the binding it was given now has members %v, want it untouched", given[0].Members)
	}
}
