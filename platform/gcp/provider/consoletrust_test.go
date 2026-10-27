package gcp

import (
	"context"
	"encoding/json"
	"io"
	"maps"
	"mime"
	"mime/multipart"
	"net/http"
	"regexp"
	"slices"
	"strings"
	"sync"
	"testing"

	"google.golang.org/api/cloudresourcemanager/v1"
	"google.golang.org/api/iam/v1"
	storagejson "google.golang.org/api/storage/v1"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/progress"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/fake"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/bootstrap"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/credential"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/gcloud"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/googletest"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/policy"
)

const (
	consolePool     = "ocel-console"
	productionAgent = "ocel-prod-deploy@acme-prod.iam.gserviceaccount.com"
	uuidTarget      = "0199d1e2-0000-7000-8000-00000000000b"
)

var identityProviderID = regexp.MustCompile(`^[a-z0-9-]{4,32}$`)

func consoleTrust() provider.WebIdentityTrust {
	return provider.WebIdentityTrust{
		Issuer:         "https://console.example.com/oidc",
		Audience:       "https://console.example.com",
		OrganizationID: "o-1",
		TargetID:       "t-1",
	}
}

type trustHarness struct {
	b      bootstrap.Engine
	iam    *googletest.IAM
	pools  *googletest.IdentityPools
	tags   *googletest.TierTags
	mu     *sync.Mutex
	stamps map[string]bootstrap.Stamp
}

func trusting(t *testing.T) trustHarness {
	t.Helper()
	h := trustHarness{iam: googletest.GrantedIAM(), pools: googletest.NewIdentityPools(), tags: googletest.NewTieredTags(t),
		mu: &sync.Mutex{}, stamps: map[string]bootstrap.Stamp{}}
	h.iam.Accounts = map[string]bool{}
	h.iam.AccountPolicies = map[string]*iam.Policy{}
	h.iam.Roles = map[string][]string{}
	rest := h.iam.Rest(t)
	clients := h.iam.Serve(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case h.pools.Serves(r):
			h.pools.Serve(w, r)
		case r.Method == http.MethodPost && r.URL.Path == "/v1/projects/"+googletest.Project+":testIamPermissions":
			if asked := googletest.ReadBody[cloudresourcemanager.TestIamPermissionsRequest](w, r); asked != nil {
				googletest.WriteBody(w, &cloudresourcemanager.TestIamPermissionsResponse{Permissions: asked.Permissions})
			}
		case r.Method == http.MethodPost && strings.HasPrefix(r.URL.Path, "/upload/storage/v1/b/"):
			h.storeStamp(t, r)
			googletest.WriteBody(w, &storagejson.Object{Name: bootstrap.StampObject, Generation: 1})
		case r.Method == http.MethodPost && r.URL.Path == "/v1/projects/"+googletest.Project+"/roles":
			asked := googletest.ReadBody[iam.CreateRoleRequest](w, r)
			if asked == nil {
				return
			}
			name := "projects/" + googletest.Project + "/roles/" + asked.RoleId
			h.iam.Mu.Lock()
			h.iam.Roles[name] = asked.Role.IncludedPermissions
			h.iam.Mu.Unlock()
			googletest.WriteBody(w, &iam.Role{Name: name, IncludedPermissions: asked.Role.IncludedPermissions, Stage: "GA"})
		default:
			rest(w, r)
		}
	})
	h.tags.Tagged(clients)
	h.b = composed(bootstrap.Engine{
		Clients:  clients,
		Records:  fake.NewKeyValues(),
		Surveyed: &bootstrap.Surveys{},
		Edges:    &frontRegistry{front: &countingFront{installed: true}},
	})
	h.b.SweepAccounts = func(context.Context, bootstrap.Engine, provider.BootstrapRequest, progress.Log) error { return nil }
	return h
}

func (h trustHarness) storeStamp(t *testing.T, r *http.Request) {
	t.Helper()
	_, params, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil {
		t.Errorf("the stamp upload carried no content type: %v", err)
		return
	}
	parts := multipart.NewReader(r.Body, params["boundary"])
	if _, err := parts.NextPart(); err != nil {
		t.Errorf("the stamp upload carried no metadata: %v", err)
		return
	}
	media, err := parts.NextPart()
	if err != nil {
		t.Errorf("the stamp upload carried no body: %v", err)
		return
	}
	body, err := io.ReadAll(media)
	if err != nil {
		t.Error(err)
		return
	}
	var written bootstrap.Stamp
	if err := json.Unmarshal(body, &written); err != nil {
		t.Errorf("the stamp upload = %s, want a stamp: %v", body, err)
		return
	}
	bucket := strings.Split(strings.TrimPrefix(r.URL.Path, "/upload/storage/v1/b/"), "/")[0]
	h.mu.Lock()
	defer h.mu.Unlock()
	h.stamps[bucket] = written
}

func (h trustHarness) stamp(tier environment.Tier) (bootstrap.Stamp, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	written, present := h.stamps[googletest.TierBuckets(tier)[naming.StorageState]]
	return written, present
}

func (h trustHarness) surveyed(tier environment.Tier) bootstrap.Survey {
	read := surveyed()
	read.Tier, read.Region, read.Emulated, read.Held = tier, h.b.Clients.Region, true, map[string]bool{}
	if written, present := h.stamp(tier); present {
		read.Stamp = written
	}
	read.SiblingStamp, read.Sibling = h.stamp(tier.Sibling())
	for _, item := range bootstrap.Resources(read.Names, read.Tier, read.Emulated) {
		read.Held[item.ID()] = true
	}
	return read
}

func (h trustHarness) apply(t *testing.T, req provider.BootstrapRequest) {
	t.Helper()
	h.b.Surveyed.Remember(h.surveyed(req.Tier))
	if err := h.b.Apply(t.Context(), req, nil); err != nil {
		t.Fatalf("Apply(%+v) = %v", req, err)
	}
}

func (h trustHarness) describe(t *testing.T, tier environment.Tier) provider.BootstrapDescription {
	t.Helper()
	described, err := h.b.Described(t.Context(), h.surveyed(tier))
	if err != nil {
		t.Fatalf("Described(%s) = %v", tier, err)
	}
	return described
}

func (h trustHarness) plan(t *testing.T, req provider.BootstrapRequest) provider.ChangeGroup {
	t.Helper()
	h.b.Surveyed.Remember(h.surveyed(req.Tier))
	plan, err := h.b.Plan(t.Context(), req)
	if err != nil {
		t.Fatalf("Plan(%+v) = %v", req, err)
	}
	at := slices.IndexFunc(plan.Groups, func(group provider.ChangeGroup) bool { return group.Part == provider.ConsoleTrustPart })
	if at < 0 {
		t.Fatalf("Plan(%+v) = %+v, want a group for the %s part", req, plan.Groups, provider.ConsoleTrustPart)
	}
	return plan.Groups[at]
}

func trustRequest(tier environment.Tier, trust provider.WebIdentityTrust) provider.BootstrapRequest {
	return provider.BootstrapRequest{Tier: tier, Parts: []string{provider.ConsoleTrustPart}, Trust: &trust}
}

func rerunRequest(tier environment.Tier) provider.BootstrapRequest {
	return provider.BootstrapRequest{Tier: tier, Parts: []string{provider.ConsoleTrustPart}}
}

func (h trustHarness) bindingsOf(member string) []policy.Binding {
	h.iam.Mu.Lock()
	defer h.iam.Mu.Unlock()
	var held []policy.Binding
	for _, binding := range h.iam.Project.Bindings {
		if !slices.Contains(binding.Members, member) {
			continue
		}
		held = append(held, policy.Binding{Role: binding.Role, Members: []string{member}, Condition: conditionOf(binding.Condition)})
	}
	return held
}

func (h trustHarness) revokeFromProject(role, member string) {
	h.iam.Mu.Lock()
	defer h.iam.Mu.Unlock()
	for _, binding := range h.iam.Project.Bindings {
		if binding.Role == role {
			binding.Members = slices.DeleteFunc(binding.Members, func(held string) bool { return held == member })
		}
	}
}

func conditionOf(expr *cloudresourcemanager.Expr) *policy.Condition {
	if expr == nil {
		return nil
	}
	return &policy.Condition{Title: expr.Title, Expression: expr.Expression}
}

func consoleTrustStack(t *testing.T, described provider.BootstrapDescription) provider.BootstrapStack {
	t.Helper()
	stack, found := partStack(described, provider.ConsoleTrustPart)
	if !found {
		t.Fatalf("Described().Stacks = %+v, want one for %s", described.Stacks, provider.ConsoleTrustPart)
	}
	return stack
}

func TestATrustedConsoleIsAProviderOfItsTargetInTheProjectsPoolThatTakesOnlyThatTargetsTokens(t *testing.T) {
	t.Parallel()
	h := trusting(t)

	h.apply(t, trustRequest(environment.TierProduction, consoleTrust()))

	if pool := h.pools.Pool(consolePool); pool == nil || pool.State != "ACTIVE" {
		t.Fatalf("pool %s = %+v, want it made", consolePool, pool)
	}
	console := h.pools.Provider(consolePool, h.b.Clients.ConsoleProvider("t-1"))
	if console == nil {
		t.Fatal("the pool holds no provider for the target")
	}
	if console.Oidc == nil || console.Oidc.IssuerUri != "https://console.example.com/oidc" || !slices.Equal(console.Oidc.AllowedAudiences, []string{"https://console.example.com"}) {
		t.Errorf("console provider oidc = %+v, want the console's issuer and its origin as the one audience", console.Oidc)
	}
	wantMapping := map[string]string{
		"google.subject":   "assertion.target + ':' + assertion.tier + ':' + assertion.project",
		"attribute.org":    "assertion.org",
		"attribute.target": "assertion.target",
		"attribute.tier":   "assertion.tier",
	}
	if !maps.Equal(console.AttributeMapping, wantMapping) {
		t.Errorf("console provider mapping = %v, want %v", console.AttributeMapping, wantMapping)
	}
	if want := "assertion.org == 'o-1' && assertion.target == 't-1'"; console.AttributeCondition != want {
		t.Errorf("console provider condition = %q, want %q", console.AttributeCondition, want)
	}
}

func TestATargetsProviderIDAndTheSubjectItMapsFitGooglesLimitsForUUIDTargetsAndTheLongestProject(t *testing.T) {
	t.Parallel()
	h := trusting(t)

	id := h.b.Clients.ConsoleProvider(uuidTarget)
	if !identityProviderID.MatchString(id) || strings.HasPrefix(id, "gcp-") {
		t.Errorf("ConsoleProvider(%s) = %q, want 4-32 lowercase letters, digits or hyphens, not starting gcp-", uuidTarget, id)
	}
	if other := h.b.Clients.ConsoleProvider("0199d1e2-0000-7000-8000-00000000000c"); other == id {
		t.Errorf("two targets share the provider %q, want each its own", id)
	}
	subject := uuidTarget + ":" + string(environment.TierProduction) + ":" + strings.Repeat("p", 63)
	if len(subject) > 127 {
		t.Errorf("the longest mapped subject is %d bytes, and Google allows 127", len(subject))
	}
}

func TestTwoTargetsTrustedByTheTwoTiersOfAProjectEachKeepTheirOwnProvider(t *testing.T) {
	t.Parallel()
	h := trusting(t)
	other := consoleTrust()
	other.TargetID = "t-2"

	h.apply(t, trustRequest(environment.TierProduction, consoleTrust()))
	h.apply(t, trustRequest(environment.TierPreview, other))

	for target, want := range map[string]string{"t-1": "assertion.target == 't-1'", "t-2": "assertion.target == 't-2'"} {
		held := h.pools.Provider(consolePool, h.b.Clients.ConsoleProvider(target))
		if held == nil || held.State != "ACTIVE" || !strings.Contains(held.AttributeCondition, want) {
			t.Errorf("the provider of %s = %+v, want it active with its own condition", target, held)
		}
	}
}

func TestAConsoleTrustQuotesWhatItComparesSoAnIDCannotWidenTheCondition(t *testing.T) {
	t.Parallel()
	trust := consoleTrust()
	trust.OrganizationID = `o' || true || '`

	got := bootstrap.ConsoleProviderOf(trust).AttributeCondition

	if want := `assertion.org == 'o\' || true || \'' && assertion.target == 't-1'`; got != want {
		t.Errorf("condition = %q, want %q", got, want)
	}
}

func TestTheDeployAccountOfATierHoldsExactlyWhatADeployCredentialOfThatTierIsGranted(t *testing.T) {
	t.Parallel()
	h := trusting(t)
	tier := environment.TierProduction

	h.apply(t, trustRequest(tier, consoleTrust()))

	member := "serviceAccount:" + productionAgent
	project := h.bindingsOf(member)
	value := h.tags.ValueOf(tier)
	h.tags.Mu.Lock()
	var tagValue []policy.Binding
	for _, binding := range h.tags.ValuePolicies[value].Bindings {
		if slices.Contains(binding.Members, member) {
			tagValue = append(tagValue, policy.Binding{Role: binding.Role, Members: []string{member}})
		}
	}
	h.tags.Mu.Unlock()

	grants := credential.New(h.b.Clients.Names, h.b.Clients.Region, tier).Grants()
	var wantProject, wantTagValue []policy.Binding
	for _, grant := range grants {
		role := grant.Role
		if role == "" {
			role = h.b.Clients.DeployRolePath(grant.Permissions)
			if held := h.iam.Roles[role]; !slices.Equal(held, grant.Permissions) {
				t.Errorf("the custom role %s holds %v, want exactly %v", role, held, grant.Permissions)
			}
		}
		if grant.On.Name != "" {
			wantTagValue = append(wantTagValue, policy.Binding{Role: role})
			continue
		}
		wantProject = append(wantProject, policy.Binding{Role: role, Condition: grant.Condition})
	}
	if !policy.HoldsOnly(project, member, wantProject) || len(project) != len(wantProject) {
		t.Errorf("the deploy account holds %+v on the project, want exactly the deploy grants %+v", project, wantProject)
	}
	if !policy.HoldsOnly(tagValue, member, wantTagValue) || len(tagValue) != len(wantTagValue) {
		t.Errorf("the deploy account holds %+v on the tier's tag value, want exactly %+v", tagValue, wantTagValue)
	}
}

func TestTheConsolesTokensOfATierMayActAsThatTiersDeployAccountAndNoOther(t *testing.T) {
	t.Parallel()
	h := trusting(t)

	h.apply(t, trustRequest(environment.TierProduction, consoleTrust()))

	h.iam.Mu.Lock()
	held := h.iam.AccountPolicies["/v1/"+gcloud.AccountPath(h.b.Clients, "ocel-prod-deploy")]
	h.iam.Mu.Unlock()
	want := "principalSet://iam.googleapis.com/projects/123456789/locations/global/workloadIdentityPools/ocel-console/attribute.tier/production"
	if held == nil || !slices.ContainsFunc(held.Bindings, func(binding *iam.Binding) bool {
		return binding.Role == bootstrap.WorkloadIdentityUserRole && slices.Equal(binding.Members, []string{want})
	}) {
		t.Errorf("the deploy account's policy = %+v, want %s holding %s", held, want, bootstrap.WorkloadIdentityUserRole)
	}
}

func TestATrustedTierDescribesItsTargetsProviderTheAccountAndTheTrustItWasLastAppliedWith(t *testing.T) {
	t.Parallel()
	h := trusting(t)
	tier := environment.TierProduction
	trust := consoleTrust()
	h.apply(t, trustRequest(tier, trust))

	described := h.describe(t, tier)

	want := provider.TierIdentity{
		IdentityProvider: "//iam.googleapis.com/projects/123456789/locations/global/workloadIdentityPools/ocel-console/providers/" + h.b.Clients.ConsoleProvider("t-1"),
		Principal:        productionAgent,
	}
	if described.Identity == nil || *described.Identity != want {
		t.Errorf("Described().Identity = %+v, want %+v", described.Identity, want)
	}
	if got := described.Trust; got == nil || got.TargetID != "t-1" || *got != trust {
		t.Errorf("Described().Trust = %+v, want %+v", got, trust)
	}
	if stack := consoleTrustStack(t, described); !stack.Present || !stack.DigestCurrent {
		t.Errorf("the %s stack = %+v, want it present and current", provider.ConsoleTrustPart, stack)
	}
	if untrusted := h.describe(t, environment.TierPreview); untrusted.Identity != nil || untrusted.Trust != nil {
		t.Errorf("Described(preview) = %+v, %+v, want no identity or trust: the preview tier trusts no console", untrusted.Identity, untrusted.Trust)
	}
}

func TestATrustAppliedByAnotherBuildIsDescribedAsStaleSoADeployIsRefusedUntilItIsBootstrappedAgain(t *testing.T) {
	t.Parallel()
	h := trusting(t)
	tier := environment.TierProduction
	h.apply(t, trustRequest(tier, consoleTrust()))
	read := h.surveyed(tier)
	read.Stamp.TrustDigest = "what another build granted"

	described, err := h.b.Described(t.Context(), read)
	if err != nil {
		t.Fatalf("Described() = %v", err)
	}

	if stack := consoleTrustStack(t, described); !stack.Present || stack.DigestCurrent {
		t.Errorf("the %s stack = %+v, want it present and stale", provider.ConsoleTrustPart, stack)
	}
}

func TestABootstrapCarryingNoTrustRestoresTheDeployGrantsFromTheTrustItRecorded(t *testing.T) {
	t.Parallel()
	h := trusting(t)
	tier := environment.TierProduction
	member := "serviceAccount:" + productionAgent
	h.apply(t, trustRequest(tier, consoleTrust()))
	h.revokeFromProject(credential.RunRole, member)

	if group := h.plan(t, rerunRequest(tier)); group.Action != provider.ActionUpdate {
		t.Errorf("Plan() with no trust over a drifted account = %+v, want its grants updated", group)
	}
	h.apply(t, rerunRequest(tier))

	deploy := credential.New(h.b.Clients.Names, h.b.Clients.Region, tier)
	if !policy.Holds(h.bindingsOf(member), credential.RunRole, member, deploy.TierCondition()) {
		t.Errorf("the deploy account holds %+v after a bootstrap with no trust, want %s granted again", h.bindingsOf(member), credential.RunRole)
	}
	if written, _ := h.stamp(tier); written.Trust == nil || written.Trust.TargetID != "t-1" {
		t.Errorf("the stamp records trust %+v, want the trust it was applied with kept", written.Trust)
	}
}

func TestABootstrapCarryingNoTrustOverACurrentConsoleTrustChangesNothing(t *testing.T) {
	t.Parallel()
	h := trusting(t)
	tier := environment.TierProduction
	h.apply(t, trustRequest(tier, consoleTrust()))
	h.iam.Mu.Lock()
	created, projectWrites := len(h.iam.Created), h.iam.ProjectWrites
	h.iam.Mu.Unlock()

	group := h.plan(t, rerunRequest(tier))
	if group.Action != provider.ActionKeep {
		t.Errorf("Plan() with no trust over a current part = %+v, want it kept", group)
	}
	h.apply(t, rerunRequest(tier))

	h.iam.Mu.Lock()
	defer h.iam.Mu.Unlock()
	if len(h.iam.Created) != created || h.iam.ProjectWrites != projectWrites || len(h.pools.Patched) != 0 {
		t.Errorf("a bootstrap with no trust created %d accounts, wrote the project policy %d times and patched %v, want nothing touched",
			len(h.iam.Created)-created, h.iam.ProjectWrites-projectWrites, h.pools.Patched)
	}
}

func TestABootstrapPlanShowsWhatTrustingTheConsoleMakesAndThenThatItIsCurrent(t *testing.T) {
	t.Parallel()
	h := trusting(t)
	tier := environment.TierProduction
	req := trustRequest(tier, consoleTrust())

	first := h.plan(t, req)
	if first.Action != provider.ActionCreate || len(first.Changes) == 0 {
		t.Errorf("Plan() before the trust = %+v, want the part created with each thing it makes", first)
	}
	for _, change := range first.Changes {
		if change.Action != provider.ActionCreate {
			t.Errorf("Plan() before the trust shows %s %s as %s, want a create", change.Kind, change.Name, change.Action)
		}
	}

	h.apply(t, req)
	if again := h.plan(t, req); again.Action != provider.ActionKeep {
		t.Errorf("Plan() after the trust = %+v, want it kept as current", again)
	}

	moved := consoleTrust()
	moved.TargetID = "t-2"
	changed := h.plan(t, trustRequest(tier, moved))
	if changed.Action != provider.ActionUpdate || !slices.ContainsFunc(changed.Changes, func(change provider.Change) bool {
		return change.Name == h.b.Clients.ConsoleProvider("t-1") && change.Action == provider.ActionDelete
	}) {
		t.Errorf("Plan() trusting another target = %+v, want the new target's provider made and the old one's deleted", changed)
	}
}

func TestRemovingTheConsoleTrustOfATierKeepsThePoolAndTheTargetTheOtherTierStillTrusts(t *testing.T) {
	t.Parallel()
	h := trusting(t)
	h.apply(t, trustRequest(environment.TierPreview, consoleTrust()))
	h.apply(t, trustRequest(environment.TierProduction, consoleTrust()))

	h.apply(t, provider.BootstrapRequest{Tier: environment.TierProduction, Remove: []string{provider.ConsoleTrustPart}})

	if pool := h.pools.Pool(consolePool); pool.State != "ACTIVE" {
		t.Errorf("pool state = %s, want it kept for the preview tier that still trusts the console", pool.State)
	}
	if held := h.pools.Provider(consolePool, h.b.Clients.ConsoleProvider("t-1")); held.State != "ACTIVE" {
		t.Errorf("the target's provider is %s, want it kept for the preview tier that trusts the same target", held.State)
	}
	h.iam.Mu.Lock()
	gone := !h.iam.Accounts["ocel-prod-deploy"]
	h.iam.Mu.Unlock()
	if !gone {
		t.Error("the production deploy account is still there, want it deleted with the part")
	}
	if held := h.bindingsOf("serviceAccount:" + productionAgent); len(held) != 0 {
		t.Errorf("the project still grants the deleted deploy account %v, want nothing", held)
	}
	if described := h.describe(t, environment.TierProduction); described.Identity != nil || described.Trust != nil {
		t.Errorf("Described() after the removal = %+v, %+v, want no identity or trust", described.Identity, described.Trust)
	}
}

func TestRemovingTheConsoleTrustOfATierDeletesItsTargetsProviderWhenTheOtherTierTrustsAnotherTarget(t *testing.T) {
	t.Parallel()
	h := trusting(t)
	other := consoleTrust()
	other.TargetID = "t-2"
	h.apply(t, trustRequest(environment.TierPreview, other))
	h.apply(t, trustRequest(environment.TierProduction, consoleTrust()))

	h.apply(t, provider.BootstrapRequest{Tier: environment.TierProduction, Remove: []string{provider.ConsoleTrustPart}})

	if held := h.pools.Provider(consolePool, h.b.Clients.ConsoleProvider("t-1")); held.State != "DELETED" {
		t.Errorf("t-1's provider is %s, want it deleted: no tier trusts t-1", held.State)
	}
	if held := h.pools.Provider(consolePool, h.b.Clients.ConsoleProvider("t-2")); held.State != "ACTIVE" {
		t.Errorf("t-2's provider is %s, want it kept for the preview tier", held.State)
	}
}

func TestRemovingTheConsoleTrustOfTheLastTrustingTierDeletesThePool(t *testing.T) {
	t.Parallel()
	h := trusting(t)
	h.apply(t, trustRequest(environment.TierProduction, consoleTrust()))

	h.apply(t, provider.BootstrapRequest{Tier: environment.TierProduction, Remove: []string{provider.ConsoleTrustPart}})

	if pool := h.pools.Pool(consolePool); pool.State != "DELETED" {
		t.Errorf("pool state = %s, want it deleted: no tier trusts the console", pool.State)
	}
}

func TestABootstrapCredentialMayMakeAndRemoveTheConsolesTrust(t *testing.T) {
	t.Parallel()

	for _, role := range bootstrap.GrantsFor([]string{provider.ConsoleTrustPart}).Roles() {
		if !slices.Contains(bootstrap.BootstrapRoles(), role) {
			t.Errorf("trusting the console needs %s, and a bootstrap credential is granted only %v", role, bootstrap.BootstrapRoles())
		}
	}
	for _, permission := range []string{"iam.workloadIdentityPools.create", "iam.workloadIdentityPoolProviders.update", "resourcemanager.tagValues.setIamPolicy"} {
		if !slices.Contains(bootstrap.PermissionsFor([]string{provider.ConsoleTrustPart}), permission) {
			t.Errorf("a bootstrap trusting the console does not check %s before it starts", permission)
		}
	}
}
