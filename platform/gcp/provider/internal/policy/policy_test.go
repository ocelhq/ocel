package policy_test

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"google.golang.org/api/googleapi"

	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/platform/gcp/provider/googleretry"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/policy"
)

func policyBindings(members ...string) []policy.Binding {
	return []policy.Binding{
		{Role: "roles/viewer", Members: slices.Clone(members)},
		{Role: "roles/editor", Members: []string{"user:kept@acme.example", "user:other@acme.example"}},
	}
}

func snapshotOf(t *testing.T, bindings any) string {
	t.Helper()
	encoded, err := json.Marshal(bindings)
	if err != nil {
		t.Fatal(err)
	}
	return string(encoded)
}

func TestGrantingAMemberARoleLeavesTheBindingsItWasGivenUnchanged(t *testing.T) {
	t.Parallel()
	bindings := policyBindings("user:kept@acme.example")
	before := snapshotOf(t, bindings)

	got, changed := policy.Grant("roles/viewer", "user:new@acme.example", nil)(bindings)

	if !changed || !slices.Contains(got[0].Members, "user:new@acme.example") {
		t.Fatalf("Grant() = %s, %v, want the member granted", snapshotOf(t, got), changed)
	}
	if after := snapshotOf(t, bindings); after != before {
		t.Errorf("the bindings it was given are now %s, want %s", after, before)
	}
}

func TestTakingAMemberOffARoleLeavesTheBindingsItWasGivenUnchanged(t *testing.T) {
	t.Parallel()
	bindings := policyBindings("user:kept@acme.example", "user:gone@acme.example")
	before := snapshotOf(t, bindings)

	got, changed := policy.Revoke("roles/viewer", "user:gone@acme.example", nil)(bindings)

	if !changed || slices.Contains(got[0].Members, "user:gone@acme.example") {
		t.Fatalf("Revoke() = %s, %v, want the member taken off", snapshotOf(t, got), changed)
	}
	if after := snapshotOf(t, bindings); after != before {
		t.Errorf("the bindings it was given are now %s, want %s", after, before)
	}
}

func TestAGrantIsAddedOnceAndTakenAwayOnce(t *testing.T) {
	t.Parallel()
	const member = "serviceAccount:ocel-connector@project.iam.gserviceaccount.com"
	only := &policy.Condition{Expression: `resource.name == "projects/example-project/databases/ocel"`}

	bindings, changed := policy.Grant("roles/datastore.user", member, only)(nil)
	if !changed || len(bindings) != 1 || !slices.Contains(bindings[0].Members, member) || bindings[0].Condition != only {
		t.Fatalf("granting on an empty policy = %+v, %v, want one binding on the condition", bindings, changed)
	}
	if _, again := policy.Grant("roles/datastore.user", member, only)(bindings); again {
		t.Error("granting twice rewrote the policy, so every deploy would churn IAM")
	}
	bindings, changed = policy.Revoke("roles/datastore.user", member, only)(bindings)
	if !changed || len(bindings) != 0 {
		t.Errorf("revoking left %+v, want the binding it emptied dropped", bindings)
	}
	if _, again := policy.Revoke("roles/datastore.user", member, only)(bindings); again {
		t.Error("revoking twice rewrote the policy")
	}
}

func TestAGrantUnderAnotherConditionIsADifferentBinding(t *testing.T) {
	t.Parallel()
	const member = "serviceAccount:ocel-connector@project.iam.gserviceaccount.com"
	shop := &policy.Condition{Expression: "shop"}
	ocel := &policy.Condition{Expression: "ocel"}
	existing := []policy.Binding{
		{Role: "roles/datastore.user", Members: []string{member}, Condition: shop},
		{Role: "roles/datastore.user", Members: []string{member}},
	}

	bindings, changed := policy.Grant("roles/datastore.user", member, ocel)(existing)
	if !changed || len(bindings) != 3 {
		t.Fatalf("granting under another condition = %+v, %v, want a third binding of its own", bindings, changed)
	}
	bindings, changed = policy.Revoke("roles/datastore.user", member, ocel)(bindings)
	if !changed || len(bindings) != 2 || bindings[0].Condition != shop || bindings[1].Condition != nil {
		t.Errorf("revoking left %+v, and taking one condition's grant away may not touch another's or the unconditional one", bindings)
	}
}

func TestAnotherMembersGrantSurvivesARevoke(t *testing.T) {
	t.Parallel()
	const member = "serviceAccount:app@project.iam.gserviceaccount.com"
	existing := []policy.Binding{{Role: "roles/viewer", Members: []string{"user:someone@example.com", member}}}

	bindings, changed := policy.Revoke("roles/viewer", member, nil)(existing)

	if !changed || !slices.Equal(bindings[0].Members, []string{"user:someone@example.com"}) {
		t.Errorf("revoking left %v, and taking one member off may not take anybody else's access with it", bindings)
	}
}

func TestTakingAMemberOffRolesLeavesEveryOtherRoleAndMember(t *testing.T) {
	t.Parallel()
	member, other := "serviceAccount:app@x.iam.gserviceaccount.com", "serviceAccount:other@x.iam.gserviceaccount.com"
	first, second := &policy.Condition{Expression: "a"}, &policy.Condition{Expression: "b"}
	bindings := []policy.Binding{
		{Role: "roles/one", Condition: first, Members: []string{member}},
		{Role: "roles/two", Condition: second, Members: []string{member, other}},
		{Role: "roles/three", Members: []string{member}},
		{Role: "roles/four", Members: []string{other}},
		{Role: "roles/five", Members: []string{member}},
	}
	before := snapshotOf(t, bindings)

	kept, changed := policy.RevokeRoles(member, []string{"roles/one", "roles/two", "roles/three"})(bindings)

	if !changed || len(kept) != 3 || kept[0].Role != "roles/two" || !slices.Equal(kept[0].Members, []string{other}) ||
		kept[1].Role != "roles/four" || kept[2].Role != "roles/five" || !slices.Equal(kept[2].Members, []string{member}) {
		t.Errorf("RevokeRoles() kept %+v, want roles/two held by the other member, roles/four, and roles/five still held by the member", kept)
	}
	if after := snapshotOf(t, bindings); after != before {
		t.Errorf("the bindings it was given are now %s, want %s", after, before)
	}
}

func TestOnlyMemberLeavesARoleHeldByThatMemberAlone(t *testing.T) {
	t.Parallel()
	const member = "serviceAccount:sync@x.iam.gserviceaccount.com"
	bindings := []policy.Binding{
		{Role: "roles/run.invoker", Members: []string{"allUsers", member}},
		{Role: "roles/viewer", Members: []string{"user:kept@x.example"}},
	}

	got, changed := policy.OnlyMember("roles/run.invoker", member)(bindings)
	if !changed || len(got) != 2 || got[1].Role != "roles/run.invoker" || !slices.Equal(got[1].Members, []string{member}) {
		t.Fatalf("OnlyMember() = %+v, %v, want the invoker held by the member alone", got, changed)
	}
	if _, again := policy.OnlyMember("roles/run.invoker", member)(got); again {
		t.Error("OnlyMember() changed a role the member already held alone")
	}
}

type policyServer struct {
	bindings []policy.Binding
	stale    int
	reads    int
	writes   int
}

func (s *policyServer) resource() policy.Resource {
	return policy.Resource{Name: "the test resource", Read: func(context.Context) ([]policy.Binding, policy.Write, error) {
		s.reads++
		return slices.Clone(s.bindings), func(_ context.Context, bindings []policy.Binding) error {
			s.writes++
			if s.stale > 0 {
				s.stale--
				return &googleapi.Error{Code: http.StatusPreconditionFailed}
			}
			s.bindings = bindings
			return nil
		}, nil
	}}
}

func TestAnEditReadsThePolicyAgainWhenTheWriteFoundItStale(t *testing.T) {
	t.Parallel()
	server := &policyServer{stale: 1}

	changed, err := policy.Edit(context.Background(), server.resource(), policy.Grant("roles/viewer", "user:new@x.example", nil))

	if err != nil || !changed {
		t.Fatalf("Edit() = %v, %v, want the grant to land after the 412", changed, err)
	}
	if server.reads != 2 || server.writes != 2 {
		t.Errorf("Edit() read %d and wrote %d times, want 2 and 2: a stale write is rebuilt on a fresh read", server.reads, server.writes)
	}
}

func TestAnEditOfAPolicyThatKeepsChangingIsRefusedRatherThanRetriedForever(t *testing.T) {
	t.Parallel()
	server := &policyServer{stale: math.MaxInt}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	_, err := policy.Edit(ctx, server.resource(), policy.Grant("roles/viewer", "user:new@x.example", nil))

	if googleretry.AnsweredCode(err) != http.StatusPreconditionFailed {
		t.Fatalf("Edit() over a policy that never stops changing = %v, want the 412 it kept answering", err)
	}
	if server.writes < 2 {
		t.Errorf("Edit() wrote %d times, want the stale write rebuilt and written again", server.writes)
	}
}

func TestAnEditThatChangesNothingWritesNothing(t *testing.T) {
	t.Parallel()
	server := &policyServer{bindings: []policy.Binding{{Role: "roles/viewer", Members: []string{"user:held@x.example"}}}}

	changed, err := policy.Edit(context.Background(), server.resource(), policy.Grant("roles/viewer", "user:held@x.example", nil))

	if err != nil || changed || server.writes != 0 {
		t.Errorf("Edit() = %v, %v after %d writes, want nothing written", changed, err, server.writes)
	}
}

func TestAnAbsentResourceIsToldApartThroughWrapping(t *testing.T) {
	t.Parallel()
	answered := &googleapi.Error{Code: http.StatusNotFound}
	missing := policy.Resource{Name: "gone", Read: func(context.Context) ([]policy.Binding, policy.Write, error) {
		return nil, nil, policy.Absent(answered)
	}}

	_, err := policy.Edit(context.Background(), missing, policy.Grant("roles/viewer", "user:x@x.example", nil))

	var kept *googleapi.Error
	if !policy.IsAbsent(err) || !errors.As(err, &kept) {
		t.Errorf("Edit() = %v, want it absent and still carrying Google's answer", err)
	}
}

func crowded(members int) []policy.Binding {
	held := make([]string, members)
	for i := range held {
		held[i] = "serviceAccount:app-" + strconv.Itoa(i) + "@x.iam.gserviceaccount.com"
	}
	return []policy.Binding{{Role: "roles/viewer", Members: held}}
}

func TestAGrantThatWouldTakeAPolicyPastGooglesPrincipalLimitIsRefusedBeforeItIsWritten(t *testing.T) {
	t.Parallel()
	server := &policyServer{bindings: crowded(policy.MaxMembers)}

	_, err := policy.Edit(context.Background(), server.resource(), policy.Grant("roles/editor", "user:one-more@x.example", nil))

	var refused refusal.Refusal
	if !errors.As(err, &refused) || !strings.Contains(err.Error(), "1,500") {
		t.Errorf("Edit() = %v, want a refusal naming the 1,500 principals Google allows in a policy", err)
	}
	if server.writes != 0 {
		t.Errorf("Edit() wrote the policy %d times, want none: Google would refuse it with an error that names no remedy", server.writes)
	}
}

func TestARevokeFromAPolicyPastThePrincipalLimitIsWritten(t *testing.T) {
	t.Parallel()
	server := &policyServer{bindings: crowded(policy.MaxMembers + 1)}

	changed, err := policy.Edit(context.Background(), server.resource(), policy.Revoke("roles/viewer", "serviceAccount:app-0@x.iam.gserviceaccount.com", nil))

	if err != nil || !changed {
		t.Errorf("Edit() = %v, %v, want the revoke written: it only makes room", changed, err)
	}
}
