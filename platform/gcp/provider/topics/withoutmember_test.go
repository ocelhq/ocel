package topics

import (
	"slices"
	"testing"

	pubsub "google.golang.org/api/pubsub/v1"
)

func TestTakingAMemberOffABindingLeavesTheBindingsItWasGivenAlone(t *testing.T) {
	t.Parallel()
	const role, member, other = "roles/pubsub.publisher", "serviceAccount:a@x", "serviceAccount:b@x"
	given := []*pubsub.Binding{{Role: role, Members: []string{member, other}}, {Role: "roles/pubsub.viewer", Members: []string{member}}}

	kept, removed := withoutMember(given, role, member)

	if !removed || len(kept) != 2 || !slices.Equal(kept[0].Members, []string{other}) {
		t.Fatalf("withoutMember() = %+v, %v, want the role held by the other member alone", kept, removed)
	}
	if !slices.Equal(given[0].Members, []string{member, other}) {
		t.Errorf("the binding it was given now has members %v, want it untouched", given[0].Members)
	}

	kept, removed = withoutMember(given[:1], "roles/pubsub.viewer", member)
	if removed || len(kept) != 1 {
		t.Errorf("withoutMember() = %+v, %v, want nothing removed where the role is not bound", kept, removed)
	}

	sole := []*pubsub.Binding{{Role: role, Members: []string{member}}}
	kept, removed = withoutMember(sole, role, member)
	if !removed || len(kept) != 0 {
		t.Errorf("withoutMember() = %+v, %v, want the emptied binding dropped", kept, removed)
	}
	if !slices.Equal(sole[0].Members, []string{member}) || len(sole) != 1 {
		t.Errorf("the bindings it was given are now %+v, want them untouched", sole)
	}
}
