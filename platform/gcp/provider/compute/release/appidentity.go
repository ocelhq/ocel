package release

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"strings"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/records"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/pkg/stackrecords"
	"github.com/ocelhq/ocel/platform/gcp/provider/googleretry"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/bootstrap"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/gcloud"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/grants"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/names"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/policy"
)

const (
	appIdentityRecord  = "appidentity"
	accountClaimRecord = "accountclaims"

	actAsPermission        = "iam.serviceAccounts.actAs"
	readPolicyPermission   = "iam.serviceAccounts.getIamPolicy"
	actAsRoleForDeployment = bootstrap.RunAsRole

	serviceAgentDomainPrefix = "gcp-sa-"
)

type appIdentity struct {
	Account  string   `json:"account,omitempty"`
	Previous []string `json:"previous,omitempty"`
}

func (i appIdentity) isEmpty() bool { return i.Account == "" && len(i.Previous) == 0 }

func (i appIdentity) equal(other appIdentity) bool {
	return i.Account == other.Account && slices.Equal(i.Previous, other.Previous)
}

func (i appIdentity) switchedTo(account, own string) appIdentity {
	current, running := cmp.Or(i.Account, own), cmp.Or(account, own)
	previous := slices.Clone(i.Previous)
	if current != running && !slices.Contains(previous, current) {
		previous = append(previous, current)
	}
	previous = slices.DeleteFunc(previous, func(each string) bool { return each == running })
	slices.Sort(previous)
	if len(previous) == 0 {
		previous = nil
	}
	return appIdentity{Account: account, Previous: previous}
}

func (i appIdentity) claimed(own string) []string {
	var claimed []string
	for _, each := range append([]string{i.Account}, i.Previous...) {
		if each != "" && each != own {
			claimed = append(claimed, each)
		}
	}
	return claimed
}

type accountClaim struct {
	Project string   `json:"project"`
	App     string   `json:"app"`
	Envs    []string `json:"envs"`
}

func appIdentityKey(tier environment.Tier, project, env, app string) records.Key {
	return records.Partition{Tier: tier, Root: stackrecords.RootBootstrap}.Key(appIdentityRecord, project, env, app)
}

func accountClaimKey(tier environment.Tier, account string) records.Key {
	return records.Partition{Tier: tier, Root: stackrecords.RootBootstrap}.Key(accountClaimRecord, account)
}

func readAppIdentity(ctx context.Context, store records.Store, tier environment.Tier, project, env, app string) (appIdentity, error) {
	key := appIdentityKey(tier, project, env, app)
	entry, err := records.ReadOrEmpty(ctx, store, key)
	if err != nil {
		return appIdentity{}, err
	}
	return decodeAppIdentity(entry)
}

func decodeAppIdentity(entry records.Entry) (appIdentity, error) {
	var recorded appIdentity
	if len(entry.Value) == 0 {
		return recorded, nil
	}
	if err := json.Unmarshal(entry.Value, &recorded); err != nil {
		return appIdentity{}, fmt.Errorf("read the accounts recorded under %s: %w", entry.Key, err)
	}
	return recorded, nil
}

func recordAppIdentity(ctx context.Context, store records.Store, tier environment.Tier, project, env, app string, identity appIdentity) error {
	key := appIdentityKey(tier, project, env, app)
	if identity.isEmpty() {
		return records.Forget(ctx, store, key)
	}
	value, err := json.Marshal(identity)
	if err != nil {
		return err
	}
	return records.Change(ctx, store, key, func(entry records.Entry) ([]byte, bool, error) {
		return value, string(entry.Value) != string(value), nil
	})
}

func forgetPreviousIdentity(ctx context.Context, store records.Store, tier environment.Tier, project, env, app, account string) error {
	key := appIdentityKey(tier, project, env, app)
	emptied := false
	err := records.Change(ctx, store, key, func(entry records.Entry) ([]byte, bool, error) {
		identity, err := decodeAppIdentity(entry)
		if err != nil || !slices.Contains(identity.Previous, account) {
			return nil, false, err
		}
		identity.Previous = slices.DeleteFunc(identity.Previous, func(each string) bool { return each == account })
		if emptied = identity.isEmpty(); emptied {
			return nil, false, nil
		}
		value, err := json.Marshal(identity)
		return value, err == nil, err
	})
	if err != nil || !emptied {
		return err
	}
	return records.ForgetMatching(ctx, store, key, func(entry records.Entry) (bool, error) {
		identity, err := decodeAppIdentity(entry)
		return err == nil && identity.Account == "" && slices.Equal(identity.Previous, []string{account}), err
	})
}

func forgetCurrentIdentity(ctx context.Context, store records.Store, tier environment.Tier, project, env, app, account, own string) error {
	key := appIdentityKey(tier, project, env, app)
	forgotten := func(identity appIdentity) appIdentity {
		return appIdentity{Previous: slices.DeleteFunc(slices.Clone(identity.Previous), func(each string) bool { return each == own })}
	}
	emptied := false
	err := records.Change(ctx, store, key, func(entry records.Entry) ([]byte, bool, error) {
		identity, err := decodeAppIdentity(entry)
		if err != nil || identity.Account != account {
			return nil, false, err
		}
		left := forgotten(identity)
		if emptied = left.isEmpty(); emptied || left.equal(identity) {
			return nil, false, nil
		}
		value, err := json.Marshal(left)
		return value, err == nil, err
	})
	if err != nil || !emptied {
		return err
	}
	return records.ForgetMatching(ctx, store, key, func(entry records.Entry) (bool, error) {
		identity, err := decodeAppIdentity(entry)
		return err == nil && identity.Account == account && forgotten(identity).isEmpty(), err
	})
}

func readClaim(entry records.Entry) (accountClaim, error) {
	var claim accountClaim
	if len(entry.Value) == 0 {
		return claim, nil
	}
	if err := json.Unmarshal(entry.Value, &claim); err != nil {
		return claim, fmt.Errorf("read who runs as the account recorded under %s: %w", entry.Key, err)
	}
	return claim, nil
}

func readAccountClaim(ctx context.Context, store records.Store, tier environment.Tier, account string) (accountClaim, error) {
	entry, err := records.ReadOrEmpty(ctx, store, accountClaimKey(tier, account))
	if err != nil {
		return accountClaim{}, err
	}
	return readClaim(entry)
}

func refuseClaimedElsewhere(ref provider.StackRef, app, account string, claim accountClaim) error {
	if len(claim.Envs) == 0 || claim.Project == ref.Project && claim.App == app {
		return nil
	}
	return refusal.Refuse(refusal.CodeInvalid,
		"a transform runs app %s of %s as %s, which app %s of %s already runs as, and revoking one app's grants would take the other's: give each app an account of its own",
		app, ref.Project, account, claim.App, claim.Project)
}

func claimAccount(ctx context.Context, store records.Store, ref provider.StackRef, app, account string) (bool, error) {
	claimed := false
	err := records.Change(ctx, store, accountClaimKey(ref.Tier, account), func(entry records.Entry) ([]byte, bool, error) {
		claim, err := readClaim(entry)
		if err != nil {
			return nil, false, err
		}
		if err := refuseClaimedElsewhere(ref, app, account, claim); err != nil {
			return nil, false, err
		}
		if claimed = !slices.Contains(claim.Envs, ref.Name.Env); !claimed {
			return nil, false, nil
		}
		claim.Project, claim.App = ref.Project, app
		claim.Envs = append(claim.Envs, ref.Name.Env)
		slices.Sort(claim.Envs)
		value, err := json.Marshal(claim)
		return value, err == nil, err
	})
	return claimed, err
}

func releaseAccount(ctx context.Context, store records.Store, tier environment.Tier, project, env, app, account string) error {
	key := accountClaimKey(tier, account)
	claimedBy := func(claim accountClaim) bool {
		return claim.Project == project && claim.App == app && slices.Contains(claim.Envs, env)
	}
	emptied := false
	err := records.Change(ctx, store, key, func(entry records.Entry) ([]byte, bool, error) {
		claim, err := readClaim(entry)
		if err != nil || !claimedBy(claim) {
			return nil, false, err
		}
		if emptied = len(claim.Envs) == 1; emptied {
			return nil, false, nil
		}
		claim.Envs = slices.DeleteFunc(claim.Envs, func(each string) bool { return each == env })
		value, err := json.Marshal(claim)
		return value, err == nil, err
	})
	if err != nil || !emptied {
		return err
	}
	return records.ForgetMatching(ctx, store, key, func(entry records.Entry) (bool, error) {
		claim, err := readClaim(entry)
		return err == nil && claimedBy(claim) && len(claim.Envs) == 1, err
	})
}

func keptByOtherEnvs(ctx context.Context, c *gcloud.Clients, store records.Store, tier environment.Tier, project, env, app string, accounts ...string) ([]grants.Grant, error) {
	var kept []grants.Grant
	for _, account := range slices.Compact(slices.Sorted(slices.Values(accounts))) {
		if account == "" {
			continue
		}
		claim, err := readAccountClaim(ctx, store, tier, account)
		if err != nil {
			return nil, err
		}
		if claim.Project != project || claim.App != app {
			continue
		}
		for _, other := range claim.Envs {
			if other == env {
				continue
			}
			held, err := grants.Read(ctx, store, tier, c.AppAccount(project, other, app))
			if err != nil {
				return nil, err
			}
			kept = append(kept, held...)
		}
	}
	return kept, nil
}

func grantsHeldBy(recorded []grants.Grant, email, own, account string) []grants.Grant {
	member := "serviceAccount:" + email
	return slices.DeleteFunc(slices.Clone(recorded), func(grant grants.Grant) bool {
		ownAccount := email == own && grant.Target.Kind == grants.Account && grant.Target.Name == account
		return grant.Member != member && !ownAccount
	})
}

func splitOwnAccountGrants(want []grants.Grant, account string) ([]grants.Grant, []grants.Grant) {
	var granted, owed []grants.Grant
	for _, grant := range want {
		if grant.Target.Kind == grants.Account && grant.Target.Name == account {
			owed = append(owed, grant)
			continue
		}
		granted = append(granted, grant)
	}
	return granted, owed
}

func refuseUnheldIdentity(ctx context.Context, c *gcloud.Clients, app, account string, owed []grants.Grant) error {
	asked := []string{actAsPermission}
	if len(owed) > 0 {
		asked = append(asked, readPolicyPermission)
	}
	held, err := c.ReadAccountPermissions(ctx, account, asked)
	if gcloud.Absent(err) {
		return refusal.Refuse(refusal.CodeInvalid,
			"a transform runs app %s as %s, which Google Cloud does not find: create it, or name an account that exists", app, account)
	}
	if err != nil {
		return err
	}
	if !slices.Contains(held, actAsPermission) {
		return refusal.Refuse(refusal.CodeDenied,
			"a transform runs app %s as %s, and the deploy credential may not act as it: grant the deploy credential %s on %s",
			app, account, actAsRoleForDeployment, account)
	}
	if len(owed) == 0 {
		return nil
	}
	if !slices.Contains(held, readPolicyPermission) {
		return refusal.Refuse(refusal.CodeDenied,
			"a transform runs app %s as %s, and the deploy credential may not read its IAM policy to check what the app needs on it: grant the deploy credential %s on %s",
			app, account, readPolicyPermission, account)
	}
	bindings, project, err := readHeldBindings(ctx, c, account)
	if err != nil {
		return err
	}
	roles := map[string][]string{}
	var missing []string
	for _, grant := range owed {
		holds, err := isPermitted(ctx, c, roles, bindings, grant)
		if err != nil {
			return err
		}
		if !holds {
			missing = append(missing, grant.Role+" to "+grant.Member)
		}
	}
	switch {
	case len(missing) == 0:
		return nil
	case project == "":
		return refusal.Refuse(refusal.CodeDenied,
			"a transform runs app %s as %s, which does not grant what the app needs on it: grant %s on %s",
			app, account, strings.Join(missing, ", "), account)
	}
	return refusal.Refuse(refusal.CodeDenied,
		"a transform runs app %s as %s, which neither it nor project %s grants what the app needs on it: grant %s on %s",
		app, account, project, strings.Join(missing, ", "), account)
}

var owedPermissions = map[string]string{
	bootstrap.RunAsRole:     actAsPermission,
	grants.TokenCreatorRole: "iam.serviceAccounts.signBlob",
}

func homeProject(account string) string {
	_, domain, _ := strings.Cut(account, "@")
	project, found := strings.CutSuffix(domain, names.AccountDomain)
	if !found || strings.HasPrefix(project, serviceAgentDomainPrefix) {
		return ""
	}
	return project
}

func readHeldBindings(ctx context.Context, c *gcloud.Clients, account string) ([]policy.Binding, string, error) {
	own, err := policy.Read(ctx, gcloud.EmailAccountPolicy(c, account))
	if err != nil {
		return nil, "", err
	}
	project := homeProject(account)
	if project == "" {
		return own, "", nil
	}
	held, err := policy.Read(ctx, gcloud.ProjectPolicyOf(c, project))
	if err != nil {
		return nil, "", err
	}
	return slices.Concat(own, held), project, nil
}

func isPermitted(ctx context.Context, c *gcloud.Clients, roles map[string][]string, held []policy.Binding, grant grants.Grant) (bool, error) {
	permission, known := owedPermissions[grant.Role]
	for _, binding := range held {
		if binding.Condition != nil || !slices.Contains(binding.Members, grant.Member) {
			continue
		}
		if binding.Role == grant.Role {
			return true, nil
		}
		if !known {
			continue
		}
		permissions, err := readRolePermissions(ctx, c, roles, binding.Role)
		if err != nil {
			return false, err
		}
		if slices.Contains(permissions, permission) {
			return true, nil
		}
	}
	return false, nil
}

func readRolePermissions(ctx context.Context, c *gcloud.Clients, roles map[string][]string, role string) ([]string, error) {
	if permissions, read := roles[role]; read {
		return permissions, nil
	}
	service, err := c.Accounts()
	if err != nil {
		return nil, err
	}
	read, err := googleretry.Attempted(ctx, service.Roles.Get(role).Context(ctx).Do)
	switch {
	case gcloud.Absent(err) || googleretry.AnsweredCode(err) == http.StatusForbidden:
		roles[role] = nil
	case err != nil:
		return nil, fmt.Errorf("read the permissions role %s holds: %w", role, err)
	default:
		roles[role] = read.IncludedPermissions
	}
	return roles[role], nil
}
