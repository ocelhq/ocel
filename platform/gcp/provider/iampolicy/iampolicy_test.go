package iampolicy_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"testing"

	"google.golang.org/api/googleapi"

	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/platform/gcp/provider/iampolicy"
)

func policyBindings(members ...string) []iampolicy.Binding {
	return []iampolicy.Binding{
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

	got, changed := iampolicy.Grant("roles/viewer", "user:new@acme.example", nil)(bindings)

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

	got, changed := iampolicy.Revoke("roles/viewer", "user:gone@acme.example", nil)(bindings)

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
	only := &iampolicy.Condition{Expression: `resource.name == "projects/example-project/databases/ocel"`}

	bindings, changed := iampolicy.Grant("roles/datastore.user", member, only)(nil)
	if !changed || len(bindings) != 1 || !slices.Contains(bindings[0].Members, member) || bindings[0].Condition != only {
		t.Fatalf("granting on an empty policy = %+v, %v, want one binding on the condition", bindings, changed)
	}
	if _, again := iampolicy.Grant("roles/datastore.user", member, only)(bindings); again {
		t.Error("granting twice rewrote the policy, so every deploy would churn IAM")
	}
	bindings, changed = iampolicy.Revoke("roles/datastore.user", member, only)(bindings)
	if !changed || len(bindings) != 0 {
		t.Errorf("revoking left %+v, want the binding it emptied dropped", bindings)
	}
	if _, again := iampolicy.Revoke("roles/datastore.user", member, only)(bindings); again {
		t.Error("revoking twice rewrote the policy")
	}
}

func TestAGrantUnderAnotherConditionIsADifferentBinding(t *testing.T) {
	t.Parallel()
	const member = "serviceAccount:ocel-connector@project.iam.gserviceaccount.com"
	shop := &iampolicy.Condition{Expression: "shop"}
	ocel := &iampolicy.Condition{Expression: "ocel"}
	existing := []iampolicy.Binding{
		{Role: "roles/datastore.user", Members: []string{member}, Condition: shop},
		{Role: "roles/datastore.user", Members: []string{member}},
	}

	bindings, changed := iampolicy.Grant("roles/datastore.user", member, ocel)(existing)
	if !changed || len(bindings) != 3 {
		t.Fatalf("granting under another condition = %+v, %v, want a third binding of its own", bindings, changed)
	}
	bindings, changed = iampolicy.Revoke("roles/datastore.user", member, ocel)(bindings)
	if !changed || len(bindings) != 2 || bindings[0].Condition != shop || bindings[1].Condition != nil {
		t.Errorf("revoking left %+v, and taking one condition's grant away may not touch another's or the unconditional one", bindings)
	}
}

func TestAnotherMembersGrantSurvivesARevoke(t *testing.T) {
	t.Parallel()
	const member = "serviceAccount:app@project.iam.gserviceaccount.com"
	existing := []iampolicy.Binding{{Role: "roles/viewer", Members: []string{"user:someone@example.com", member}}}

	bindings, changed := iampolicy.Revoke("roles/viewer", member, nil)(existing)

	if !changed || !slices.Equal(bindings[0].Members, []string{"user:someone@example.com"}) {
		t.Errorf("revoking left %v, and taking one member off may not take anybody else's access with it", bindings)
	}
}

func TestTakingAMemberOffRolesLeavesEveryOtherRoleAndMember(t *testing.T) {
	t.Parallel()
	member, other := "serviceAccount:app@x.iam.gserviceaccount.com", "serviceAccount:other@x.iam.gserviceaccount.com"
	first, second := &iampolicy.Condition{Expression: "a"}, &iampolicy.Condition{Expression: "b"}
	bindings := []iampolicy.Binding{
		{Role: "roles/one", Condition: first, Members: []string{member}},
		{Role: "roles/two", Condition: second, Members: []string{member, other}},
		{Role: "roles/three", Members: []string{member}},
		{Role: "roles/four", Members: []string{other}},
		{Role: "roles/five", Members: []string{member}},
	}
	before := snapshotOf(t, bindings)

	kept, changed := iampolicy.RevokeRoles(member, []string{"roles/one", "roles/two", "roles/three"})(bindings)

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
	bindings := []iampolicy.Binding{
		{Role: "roles/run.invoker", Members: []string{"allUsers", member}},
		{Role: "roles/viewer", Members: []string{"user:kept@x.example"}},
	}

	got, changed := iampolicy.OnlyMember("roles/run.invoker", member)(bindings)
	if !changed || len(got) != 2 || got[1].Role != "roles/run.invoker" || !slices.Equal(got[1].Members, []string{member}) {
		t.Fatalf("OnlyMember() = %+v, %v, want the invoker held by the member alone", got, changed)
	}
	if _, again := iampolicy.OnlyMember("roles/run.invoker", member)(got); again {
		t.Error("OnlyMember() changed a role the member already held alone")
	}
}

type policyServer struct {
	bindings []iampolicy.Binding
	stale    int
	reads    int
	writes   int
}

func (s *policyServer) resource() iampolicy.Resource {
	return iampolicy.Resource{Name: "the test resource", Read: func(context.Context) ([]iampolicy.Binding, iampolicy.Write, error) {
		s.reads++
		return slices.Clone(s.bindings), func(_ context.Context, bindings []iampolicy.Binding) error {
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

	changed, err := iampolicy.Edit(context.Background(), server.resource(), iampolicy.Grant("roles/viewer", "user:new@x.example", nil))

	if err != nil || !changed {
		t.Fatalf("Edit() = %v, %v, want the grant to land after the 412", changed, err)
	}
	if server.reads != 2 || server.writes != 2 {
		t.Errorf("Edit() read %d and wrote %d times, want 2 and 2: a stale write is rebuilt on a fresh read", server.reads, server.writes)
	}
}

func TestAnEditOfAPolicyThatKeepsChangingIsRefusedRatherThanRetriedForever(t *testing.T) {
	t.Parallel()
	server := &policyServer{stale: iampolicy.EditAttempts + 1}

	_, err := iampolicy.Edit(context.Background(), server.resource(), iampolicy.Grant("roles/viewer", "user:new@x.example", nil))

	if err == nil {
		t.Fatal("Edit() over a policy that never stops changing = nil, want a refusal")
	}
	if server.writes != iampolicy.EditAttempts {
		t.Errorf("Edit() wrote %d times, want %d", server.writes, iampolicy.EditAttempts)
	}
}

func TestAnEditThatChangesNothingWritesNothing(t *testing.T) {
	t.Parallel()
	server := &policyServer{bindings: []iampolicy.Binding{{Role: "roles/viewer", Members: []string{"user:held@x.example"}}}}

	changed, err := iampolicy.Edit(context.Background(), server.resource(), iampolicy.Grant("roles/viewer", "user:held@x.example", nil))

	if err != nil || changed || server.writes != 0 {
		t.Errorf("Edit() = %v, %v after %d writes, want nothing written", changed, err, server.writes)
	}
}

func TestAnAbsentResourceIsToldApartThroughWrapping(t *testing.T) {
	t.Parallel()
	answered := &googleapi.Error{Code: http.StatusNotFound}
	missing := iampolicy.Resource{Name: "gone", Read: func(context.Context) ([]iampolicy.Binding, iampolicy.Write, error) {
		return nil, nil, iampolicy.Absent(answered)
	}}

	_, err := iampolicy.Edit(context.Background(), missing, iampolicy.Grant("roles/viewer", "user:x@x.example", nil))

	var kept *googleapi.Error
	if !iampolicy.IsAbsent(err) || !errors.As(err, &kept) {
		t.Errorf("Edit() = %v, want it absent and still carrying Google's answer", err)
	}
}

func crowded(members int) []iampolicy.Binding {
	held := make([]string, members)
	for i := range held {
		held[i] = "serviceAccount:app-" + strconv.Itoa(i) + "@x.iam.gserviceaccount.com"
	}
	return []iampolicy.Binding{{Role: "roles/viewer", Members: held}}
}

func TestAGrantThatWouldTakeAPolicyPastGooglesPrincipalLimitIsRefusedBeforeItIsWritten(t *testing.T) {
	t.Parallel()
	server := &policyServer{bindings: crowded(iampolicy.MaxMembers)}

	_, err := iampolicy.Edit(context.Background(), server.resource(), iampolicy.Grant("roles/editor", "user:one-more@x.example", nil))

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
	server := &policyServer{bindings: crowded(iampolicy.MaxMembers + 1)}

	changed, err := iampolicy.Edit(context.Background(), server.resource(), iampolicy.Revoke("roles/viewer", "serviceAccount:app-0@x.iam.gserviceaccount.com", nil))

	if err != nil || !changed {
		t.Errorf("Edit() = %v, %v, want the revoke written: it only makes room", changed, err)
	}
}
