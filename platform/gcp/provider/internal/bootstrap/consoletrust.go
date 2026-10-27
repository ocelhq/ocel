package bootstrap

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"google.golang.org/api/iam/v1"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/progress"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/credential"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/gcloud"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/names"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/policy"
)

const (
	KindIdentityPool     Kind = "iam:workloadidentitypool"
	KindIdentityProvider Kind = "iam:workloadidentitypoolprovider"
	KindGrants           Kind = "iam:policybinding"

	WorkloadIdentityUserRole = "roles/iam.workloadIdentityUser"

	consoleTrustSummary = "a workload identity pool that takes the console's tokens, and a service account this tier deploys as, " +
		"holding what a deploy credential of the tier is granted"
	reasonUngrantedDeploy = "it holds other grants than a deploy credential of the tier"
)

var ConsoleTrustAPIs = []string{"iam.googleapis.com", "sts.googleapis.com", "iamcredentials.googleapis.com"}

func consoleTrustPart() provider.Part {
	return provider.Part{Name: provider.ConsoleTrustPart, Summary: consoleTrustSummary}
}

func celQuoted(value string) string {
	return "'" + strings.NewReplacer(`\`, `\\`, `'`, `\'`).Replace(value) + "'"
}

func ConsoleProviderOf(trust provider.WebIdentityTrust) *iam.WorkloadIdentityPoolProvider {
	return &iam.WorkloadIdentityPoolProvider{
		DisplayName: "ocel console",
		Oidc:        &iam.Oidc{IssuerUri: trust.Issuer, AllowedAudiences: []string{trust.Audience}},
		AttributeMapping: map[string]string{
			"google.subject":   "assertion.target + ':' + assertion.tier + ':' + assertion.project",
			"attribute.org":    "assertion.org",
			"attribute.target": "assertion.target",
			"attribute.deploy": "assertion.target + ':' + assertion.tier",
		},
		AttributeCondition: "assertion.org == " + celQuoted(trust.OrganizationID) + " && assertion.target == " + celQuoted(trust.TargetID),
	}
}

type deployGrants struct {
	member   string
	project  []policy.Binding
	tagValue []policy.Binding
	roles    []customRole
}

func newDeployGrants(n names.Names, region string, tier environment.Tier) (deployGrants, error) {
	held := deployGrants{member: gcloud.MemberOf(n.DeployAccountEmail(tier))}
	for _, grant := range credential.New(n, region, tier).Grants() {
		role := grant.Role
		if role == "" {
			held.roles = append(held.roles, customRole{
				ID:          n.DeployRole(grant.Permissions),
				title:       "ocel deploy permissions (" + string(n.Namespace()) + ")",
				description: "holds what a deploy credential is granted that no predefined role holds alone, and nothing else",
				Permissions: grant.Permissions,
				DriftReason: "its permissions drifted",
			})
			role = n.DeployRolePath(grant.Permissions)
		}
		binding := policy.Binding{Role: role, Condition: grant.Condition}
		switch grant.On.Name {
		case "":
			held.project = append(held.project, binding)
		case n.TierTagValuePath(tier):
			held.tagValue = append(held.tagValue, binding)
		default:
			return deployGrants{}, fmt.Errorf("a deploy credential is granted %s, and nothing here grants a service account anything on it", grant.On)
		}
	}
	return held, nil
}

func ConsoleTrustDigest(n names.Names, region string, tier environment.Tier, trust provider.WebIdentityTrust) (string, error) {
	grants, err := newDeployGrants(n, region, tier)
	if err != nil {
		return "", err
	}
	sum := sha256.New()
	for _, binding := range grants.project {
		fmt.Fprintf(sum, "project %s %s\n", binding.Role, expressionOf(binding.Condition))
	}
	for _, binding := range grants.tagValue {
		fmt.Fprintf(sum, "tag value %s %s\n", binding.Role, expressionOf(binding.Condition))
	}
	for _, role := range grants.roles {
		fmt.Fprintf(sum, "role %s %s\n", role.ID, strings.Join(role.Permissions, ","))
	}
	encoded, err := json.Marshal([]*iam.WorkloadIdentityPoolProvider{ConsoleProviderOf(trust)})
	if err != nil {
		return "", err
	}
	sum.Write(encoded)
	return hex.EncodeToString(sum.Sum(nil)), nil
}

func expressionOf(condition *policy.Condition) string {
	if condition == nil {
		return ""
	}
	return condition.Expression
}

func InstalledTrust(read Survey, req provider.BootstrapRequest) *provider.WebIdentityTrust {
	if !slices.Contains(InstalledParts(CredentialParts(), read.Stamp.Parts, req), provider.ConsoleTrustPart) {
		return nil
	}
	if req.Trust != nil {
		return req.Trust
	}
	return read.Stamp.Trust
}

func (b Engine) consoleTrustCurrent(read Survey) (bool, error) {
	if read.Stamp.Trust == nil {
		return false, nil
	}
	digest, err := ConsoleTrustDigest(b.Clients.Names, b.Clients.Region, read.Tier, *read.Stamp.Trust)
	return digest == read.Stamp.TrustDigest, err
}

func trustsTarget(stamp Stamp, target string) bool {
	return slices.Contains(stamp.Parts, provider.ConsoleTrustPart) && stamp.Trust != nil && stamp.Trust.TargetID == target
}

func supersededTarget(read Survey, trust provider.WebIdentityTrust) string {
	previous := read.Stamp.Trust
	if previous == nil || previous.TargetID == trust.TargetID || trustsTarget(read.SiblingStamp, previous.TargetID) {
		return ""
	}
	return previous.TargetID
}

func (b Engine) principalSet(ctx context.Context, tier environment.Tier, target string) (string, error) {
	number, err := b.Clients.ReadProjectNumber(ctx)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("principalSet://iam.googleapis.com/projects/%d/locations/global/workloadIdentityPools/%s/attribute.deploy/%s:%s",
		number, b.Clients.ConsolePool(), target, tier), nil
}

func (b Engine) ConsoleTrustIdentity(ctx context.Context, tier environment.Tier, trust provider.WebIdentityTrust) (*provider.TierIdentity, error) {
	number, err := b.Clients.ReadProjectNumber(ctx)
	if err != nil {
		return nil, err
	}
	return &provider.TierIdentity{
		IdentityProvider: fmt.Sprintf("//iam.googleapis.com/projects/%d/locations/global/workloadIdentityPools/%s/providers/%s",
			number, b.Clients.ConsolePool(), b.Clients.ConsoleProvider(trust.TargetID)),
		Principal: b.Clients.DeployAccountEmail(tier),
	}, nil
}

func (b Engine) consoleTrustInstalled(ctx context.Context, tier environment.Tier) (bool, error) {
	found, err := b.Clients.ReadAccount(ctx, b.Clients.DeployAccount(tier))
	return found != nil, err
}

func identityChange(kind Kind, name string, current, want *iam.WorkloadIdentityPoolProvider) provider.Change {
	change := provider.Change{Kind: string(kind), Name: name, Action: provider.ActionCreate}
	switch {
	case current == nil:
	case identityProviderDrift(current, want) != "":
		change.Action, change.Reason = provider.ActionUpdate, identityProviderDrift(current, want)
	default:
		change.Action, change.Reason = provider.ActionKeep, reasonCurrent
	}
	return change
}

func (b Engine) planConsoleTrust(ctx context.Context, read Survey, trust provider.WebIdentityTrust) ([]provider.Change, error) {
	tier := read.Tier
	if err := b.RefuseServicesOff(ctx, ConsoleTrustAPIs); err != nil {
		return nil, err
	}
	n := b.Clients.Names
	pool, err := b.readPool(ctx)
	if err != nil {
		return nil, err
	}
	poolChange := provider.Change{Kind: string(KindIdentityPool), Name: n.ConsolePool(), Action: provider.ActionCreate}
	switch {
	case pool != nil && pool.State == identityDeleted:
		poolChange.Action, poolChange.Reason = provider.ActionUpdate, reasonIdentityDeleted
	case pool != nil:
		poolChange.Action, poolChange.Reason = provider.ActionKeep, reasonCurrent
	}
	changes := []provider.Change{poolChange}

	console, err := b.readIdentityProvider(ctx, n.ConsoleProvider(trust.TargetID))
	if err != nil {
		return nil, err
	}
	changes = append(changes, identityChange(KindIdentityProvider, n.ConsoleProvider(trust.TargetID), console, ConsoleProviderOf(trust)))
	if superseded := supersededTarget(read, trust); superseded != "" {
		changes = append(changes, provider.Change{Kind: string(KindIdentityProvider), Name: n.ConsoleProvider(superseded), Action: provider.ActionDelete})
	}

	grants, err := newDeployGrants(n, b.Clients.Region, tier)
	if err != nil {
		return nil, err
	}
	for _, role := range grants.roles {
		found, err := b.rolePresence(ctx, role)
		if err != nil {
			return nil, err
		}
		changes = append(changes, presenceChange(KindRole, role.ID, found))
	}
	account, err := b.Clients.ReadAccount(ctx, n.DeployAccount(tier))
	if err != nil {
		return nil, err
	}
	accountChange := provider.Change{Kind: string(KindServiceAccount), Name: n.DeployAccountEmail(tier), Action: provider.ActionCreate}
	grantsChange := provider.Change{Kind: string(KindGrants), Name: n.DeployAccountEmail(tier), Action: provider.ActionCreate}
	if account != nil {
		accountChange.Action, accountChange.Reason = provider.ActionKeep, reasonCurrent
		granted, err := b.deployGranted(ctx, tier, trust.TargetID, grants)
		if err != nil {
			return nil, err
		}
		grantsChange.Action, grantsChange.Reason = provider.ActionKeep, reasonCurrent
		if !granted {
			grantsChange.Action, grantsChange.Reason = provider.ActionUpdate, reasonUngrantedDeploy
		}
	}
	return append(changes, accountChange, grantsChange), nil
}

func presenceChange(kind Kind, name string, found Presence) provider.Change {
	change := provider.Change{Kind: string(kind), Name: name, Action: provider.ActionCreate}
	switch {
	case found.Present && found.Mends != "":
		change.Action, change.Reason = provider.ActionUpdate, found.Mends
	case found.Present:
		change.Action, change.Reason = provider.ActionKeep, reasonCurrent
	}
	return change
}

func (b Engine) tagValuePolicy(ctx context.Context, tier environment.Tier) (policy.Resource, bool, error) {
	if !b.Clients.TierTagged() {
		return policy.Resource{}, false, nil
	}
	value, err := b.Clients.ReadTierTagValue(ctx, tier)
	if err != nil {
		return policy.Resource{}, false, err
	}
	return gcloud.TagValuePolicy(b.Clients, value, b.Clients.TierTagValuePath(tier)), true, nil
}

func (b Engine) deployGranted(ctx context.Context, tier environment.Tier, target string, grants deployGrants) (bool, error) {
	held, err := policy.Read(ctx, gcloud.ProjectPolicy(b.Clients))
	if err != nil || !policy.HoldsOnly(held, grants.member, grants.project) {
		return false, err
	}
	tagged, tagValued, err := b.tagValuePolicy(ctx, tier)
	if err != nil {
		return false, err
	}
	if tagValued {
		if held, err = policy.Read(ctx, tagged); err != nil || !policy.HoldsOnly(held, grants.member, grants.tagValue) {
			return false, err
		}
	}
	principals, err := b.principalSet(ctx, tier, target)
	if err != nil {
		return false, err
	}
	held, err = policy.Read(ctx, gcloud.AccountPolicy(b.Clients, b.Clients.DeployAccount(tier)))
	if policy.IsAbsent(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	_, differs := policy.OnlyMember(WorkloadIdentityUserRole, principals)(held)
	return !differs, nil
}

func (b Engine) RaiseConsoleTrust(ctx context.Context, read Survey, trust provider.WebIdentityTrust, progress progress.Log) error {
	tier := read.Tier
	if err := b.RefuseServicesOff(ctx, ConsoleTrustAPIs); err != nil {
		return err
	}
	n := b.Clients.Names
	if err := b.makePool(ctx); err != nil {
		return err
	}
	if err := b.makeIdentityProvider(ctx, n.ConsoleProvider(trust.TargetID), ConsoleProviderOf(trust)); err != nil {
		return err
	}
	if superseded := supersededTarget(read, trust); superseded != "" {
		if err := b.deleteIdentityProvider(ctx, n.ConsoleProvider(superseded)); err != nil {
			return err
		}
	}
	grants, err := newDeployGrants(n, b.Clients.Region, tier)
	if err != nil {
		return err
	}
	for _, role := range grants.roles {
		if err := b.MakeRole(ctx, role); err != nil {
			return err
		}
	}
	if err := b.Clients.CreateAccount(ctx, tier, n.DeployAccount(tier), "ocel "+string(tier)+" deploys",
		"deploys the "+string(tier)+" tier for the console it trusts, holding what a deploy credential of the tier is granted"); err != nil {
		return err
	}
	if err := b.grantDeploy(ctx, tier, trust.TargetID, grants); err != nil {
		return err
	}
	gcloud.EnsureProgress(progress).Say("Trusted the console to deploy the " + string(tier) + " tier as " + n.DeployAccountEmail(tier))
	return nil
}

func (b Engine) grantDeploy(ctx context.Context, tier environment.Tier, target string, grants deployGrants) error {
	account := b.Clients.DeployAccount(tier)
	if err := gcloud.UntilVisible(ctx, func() error {
		_, err := policy.Edit(ctx, gcloud.ProjectPolicy(b.Clients), policy.GrantOnly(grants.member, grants.project))
		return err
	}); err != nil {
		return fmt.Errorf("grant the %s service account what a deploy credential of the %s tier is granted: %w", account, tier, err)
	}
	tagged, tagValued, err := b.tagValuePolicy(ctx, tier)
	if err != nil {
		return err
	}
	if tagValued {
		if _, err := policy.Edit(ctx, tagged, policy.GrantOnly(grants.member, grants.tagValue)); err != nil {
			return err
		}
	}
	principals, err := b.principalSet(ctx, tier, target)
	if err != nil {
		return err
	}
	return gcloud.UntilVisible(ctx, func() error {
		_, err := policy.Edit(ctx, gcloud.AccountPolicy(b.Clients, account), policy.OnlyMember(WorkloadIdentityUserRole, principals))
		return gcloud.WrapAccountGrantError(err, WorkloadIdentityUserRole+" on the "+account+" service account")
	})
}

func siblingTrusts(read Survey) bool {
	return slices.Contains(read.SiblingStamp.Parts, provider.ConsoleTrustPart)
}

func ownTarget(read Survey) string {
	if read.Stamp.Trust == nil || trustsTarget(read.SiblingStamp, read.Stamp.Trust.TargetID) {
		return ""
	}
	return read.Stamp.Trust.TargetID
}

func (b Engine) plannedConsoleTrustRemoval(read Survey) []provider.Change {
	n := b.Clients.Names
	changes := []provider.Change{
		{Kind: string(KindGrants), Name: n.DeployAccountEmail(read.Tier), Action: provider.ActionDelete},
		{Kind: string(KindServiceAccount), Name: n.DeployAccountEmail(read.Tier), Action: provider.ActionDelete},
	}
	if !siblingTrusts(read) {
		return append(changes, provider.Change{Kind: string(KindIdentityPool), Name: n.ConsolePool(), Action: provider.ActionDelete})
	}
	if target := ownTarget(read); target != "" {
		changes = append(changes, provider.Change{Kind: string(KindIdentityProvider), Name: n.ConsoleProvider(target), Action: provider.ActionDelete})
	}
	return append(changes, provider.Change{Kind: string(KindIdentityPool), Name: n.ConsolePool(), Action: provider.ActionKeep, Reason: sharedWith(read.Tier)})
}

func (b Engine) TakeConsoleTrust(ctx context.Context, read Survey, progress progress.Log) error {
	n := b.Clients.Names
	member := gcloud.MemberOf(n.DeployAccountEmail(read.Tier))
	if _, err := policy.Edit(ctx, gcloud.ProjectPolicy(b.Clients), policy.RevokeAll(member)); err != nil {
		return err
	}
	tagged, tagValued, err := b.tagValuePolicy(ctx, read.Tier)
	if err != nil {
		return err
	}
	if tagValued {
		if _, err := policy.Edit(ctx, tagged, policy.RevokeAll(member)); err != nil {
			return err
		}
	}
	if _, err := b.Clients.DeleteAccount(ctx, read.Tier, n.DeployAccount(read.Tier)); err != nil {
		return err
	}
	if !siblingTrusts(read) {
		if err := b.deletePool(ctx); err != nil {
			return err
		}
	} else if target := ownTarget(read); target != "" {
		if err := b.deleteIdentityProvider(ctx, n.ConsoleProvider(target)); err != nil {
			return err
		}
	}
	gcloud.EnsureProgress(progress).Say("Removed the " + provider.ConsoleTrustPart + " part of the " + string(read.Tier) + " bootstrap")
	return nil
}

func (b Engine) planConsoleTrustGroup(ctx context.Context, read Survey, req provider.BootstrapRequest, groups []provider.ChangeGroup) error {
	at := slices.IndexFunc(groups, func(group provider.ChangeGroup) bool { return group.Part == provider.ConsoleTrustPart })
	switch {
	case at < 0:
		return nil
	case groups[at].Action == provider.ActionDelete:
		groups[at].Changes = b.plannedConsoleTrustRemoval(read)
	case InstalledTrust(read, req) != nil:
		changes, err := b.planConsoleTrust(ctx, read, *InstalledTrust(read, req))
		if err != nil {
			return err
		}
		groups[at].Changes = changes
		groups[at].Action, groups[at].Reason = provider.RollUp(changes)
	}
	return nil
}

func (b Engine) plannedConsoleTrustTeardown(read Survey) []provider.ChangeGroup {
	if !slices.Contains(read.Stamp.Parts, provider.ConsoleTrustPart) {
		return nil
	}
	return []provider.ChangeGroup{{
		Kind:    provider.StackGroupKind,
		Name:    read.Project + "/" + string(read.Tier) + "/" + provider.ConsoleTrustPart,
		Part:    provider.ConsoleTrustPart,
		Action:  provider.ActionDelete,
		Changes: b.plannedConsoleTrustRemoval(read),
	}}
}
