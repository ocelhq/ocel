package gcp

import (
	"context"
	"slices"
	"strings"
	"testing"

	"google.golang.org/api/iam/v1"

	"github.com/ocelhq/ocel/pkg/keyvalue"
	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/progress"
	"github.com/ocelhq/ocel/pkg/provider"
)

type warnings struct {
	progress.Log
	warned []string
}

func (w *warnings) Warn(message string) { w.warned = append(w.warned, message) }

func appAccountPath(c *clients, spec provider.StackSpec) (string, string) {
	email := c.AppAccountEmail(spec.Ref.Tier, spec.Ref.Project, spec.App.App)
	return email, "/v1/projects/acme-prod/serviceAccounts/" + email
}

func signersOf(policy *iam.Policy) []string {
	var signers []string
	for _, binding := range policyOrEmpty(policy) {
		if binding.Role == tokenCreatorRole {
			signers = append(signers, binding.Members...)
		}
	}
	return signers
}

func TestAGatedDeployLetsItsCredentialSignAsTheAppAccountAndNothingBeyondIt(t *testing.T) {
	t.Parallel()
	server := appAccountsOnly()
	server.accountPolicies = map[string]*iam.Policy{}
	c := server.open(t)
	spec := previewSpec()
	email, path := appAccountPath(c, spec)
	projectBefore := projectBindingsOf(server)

	allowWarming(context.Background(), c, spec.App.App, email, progress.Discard())

	if got, want := signersOf(server.accountPolicies[path]), []string{memberOf(emulatorPrincipal)}; !slices.Equal(got, want) {
		t.Errorf("the app account lets %v sign tokens as it, want %v", got, want)
	}
	if len(server.accountPolicies) != 1 {
		t.Errorf("the deploy wrote %d account policies, want only the app account's", len(server.accountPolicies))
	}
	if got := projectBindingsOf(server); !slices.Equal(got, projectBefore) || server.projectWrites != 0 {
		t.Errorf("the project policy went from %q to %q, want it untouched", projectBefore, got)
	}
}

func TestADeployWhoseCredentialCannotBindTheSigningRoleWarnsAndGoesOn(t *testing.T) {
	t.Parallel()
	server := appAccountsOnly()
	server.accountPolicies = map[string]*iam.Policy{}
	server.accountPolicyDenied = true
	c := server.open(t)
	spec := previewSpec()
	email, _ := appAccountPath(c, spec)
	log := &warnings{Log: progress.Discard()}

	allowWarming(context.Background(), c, spec.App.App, email, log)

	if len(log.warned) != 1 || !strings.Contains(log.warned[0], email) {
		t.Errorf("the deploy warned %q, want one warning naming %s", log.warned, email)
	}
}

func TestRevokingAnAppsGrantsTakesBackEveryCredentialThatMaySignAsItsAccount(t *testing.T) {
	t.Parallel()
	server := appAccountsOnly()
	c, spec, member := grantedAppAccount(t, server)
	own := "/v1/projects/acme-prod/serviceAccounts/" + strings.TrimPrefix(member, "serviceAccount:")
	server.accountPolicies[own].Bindings = append(server.accountPolicies[own].Bindings,
		&iam.Binding{Role: tokenCreatorRole, Members: []string{"user:ana@example.com", "serviceAccount:ci@acme-prod.iam.gserviceaccount.com"}})

	if err := revokeUnusedAppAccount(context.Background(), c, recordedStacks{names: []naming.StackName{spec.Ref.Name}}, spec.Ref, nil); err != nil {
		t.Fatalf("revokeUnusedAppAccount() = %v", err)
	}

	if got := signersOf(server.accountPolicies[own]); len(got) != 0 {
		t.Errorf("%v may still sign as the account after its app stopped running", got)
	}
}

func TestAnEnvironmentRecordedWhileAnAppsSigningGrantsAreRevokedKeepsThem(t *testing.T) {
	t.Parallel()
	server := appAccountsOnly()
	c, spec, member := grantedAppAccount(t, server)
	own := "/v1/projects/acme-prod/serviceAccounts/" + strings.TrimPrefix(member, "serviceAccount:")
	server.accountPolicies[own].Bindings = append(server.accountPolicies[own].Bindings,
		&iam.Binding{Role: tokenCreatorRole, Members: []string{"user:ana@example.com"}})
	lists := 0
	var backing keyvalue.Store
	records, backing := racingRecords(t, spec, func() { recordApp(t, backing, spec, stackOf("pr-8", "web", "r2")) }, &lists)

	if err := revokeUnusedAppAccount(context.Background(), c, records, spec.Ref, nil); err != nil {
		t.Fatalf("revokeUnusedAppAccount() = %v", err)
	}

	if got := signersOf(server.accountPolicies[own]); !slices.Equal(got, []string{"user:ana@example.com"}) {
		t.Errorf("the account lets %v sign as it, want the signing grant restored once another environment started running the app", got)
	}
}
