package account

import (
	"context"
	"encoding/json"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/provider"
)

type consoleTrustTemplateJSON struct {
	Resources map[string]struct {
		Type       string         `json:"Type"`
		Properties map[string]any `json:"Properties"`
	} `json:"Resources"`
}

func consoleTrustResources(t *testing.T, tier environment.Tier) consoleTrustTemplateJSON {
	t.Helper()
	var parsed consoleTrustTemplateJSON
	if err := json.Unmarshal([]byte(partTemplate(provider.ConsoleTrustPart, tier)), &parsed); err != nil {
		t.Fatalf("parse the console trust template: %v", err)
	}
	return parsed
}

func asJSON(t *testing.T, value any) string {
	t.Helper()
	out, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return string(out)
}

func TestTheDeployRoleAdmitsTheConsolesTokensForItsSubjectAndGitHubForTheTiersRef(t *testing.T) {
	for tier, gitHubSubject := range map[environment.Tier]string{
		environment.TierProduction: "repo:acme/shop:ref:refs/heads/main",
		environment.TierPreview:    "repo:acme/shop:pull_request",
	} {
		t.Run(string(tier), func(t *testing.T) {
			role := consoleTrustResources(t, tier).Resources["DeployRole"]
			if role.Type != "AWS::IAM::Role" {
				t.Fatalf("DeployRole is a %q, want an AWS::IAM::Role", role.Type)
			}
			if got := role.Properties["MaxSessionDuration"]; got != float64(14400) {
				t.Errorf("MaxSessionDuration = %v, want 14400", got)
			}
			if got := role.Properties["RoleName"]; got != defaultNamespace.DeployRoleName(tier) {
				t.Errorf("RoleName = %v, want %s", got, defaultNamespace.DeployRoleName(tier))
			}
			want := []map[string]any{
				{
					"Effect":    "Allow",
					"Action":    "sts:AssumeRoleWithWebIdentity",
					"Principal": map[string]any{"Federated": map[string]any{"Fn::Sub": "arn:${AWS::Partition}:iam::${AWS::AccountId}:oidc-provider/" + fixtureIssuerHost}},
					"Condition": map[string]any{
						"StringEquals": map[string]any{fixtureIssuerHost + ":aud": fixtureAudience},
						"StringLike":   map[string]any{fixtureIssuerHost + ":sub": "org:o:target:t:tier:" + string(tier) + ":project:*"},
					},
				},
				{
					"Effect":    "Allow",
					"Action":    "sts:AssumeRoleWithWebIdentity",
					"Principal": map[string]any{"Federated": map[string]any{"Fn::Sub": "arn:${AWS::Partition}:iam::${AWS::AccountId}:oidc-provider/token.actions.githubusercontent.com"}},
					"Condition": map[string]any{"StringEquals": map[string]any{
						"token.actions.githubusercontent.com:aud": "sts.amazonaws.com",
						"token.actions.githubusercontent.com:sub": gitHubSubject,
					}},
				},
			}
			document := role.Properties["AssumeRolePolicyDocument"].(map[string]any)
			if got := asJSON(t, document["Statement"]); got != asJSON(t, want) {
				t.Errorf("the trust policy is\n%s\nwant\n%s", got, asJSON(t, want))
			}
		})
	}
}

func TestTheDeployRoleTrustsNoGitHubWorkflowUnlessTheTrustNamesARepository(t *testing.T) {
	trust := fixtureTrust()
	trust.GitHub = nil
	stack, err := consoleTrustTemplate(partInputs{ns: defaultNamespace, tier: environment.TierProduction, trust: trust, deploy: fixtureDeploy(environment.TierProduction)})
	if err != nil {
		t.Fatalf("consoleTrustTemplate: %v", err)
	}
	if strings.Contains(stack.body, gitHubIssuerHost) {
		t.Errorf("a trust naming no repository rendered a role that trusts GitHub:\n%s", stack.body)
	}
}

func TestTheDeployRoleHoldsExactlyThePoliciesTheDeployCredentialPrints(t *testing.T) {
	for _, tier := range bothTiers {
		for _, broughtKey := range []string{"", "arn:aws:kms:us-east-1:111122223333:key/brought"} {
			t.Run(string(tier)+"/"+broughtKey, func(t *testing.T) {
				printed, err := DeployCredentialPermissions(defaultNamespace, tier, broughtKey)
				if err != nil {
					t.Fatalf("DeployCredentialPermissions: %v", err)
				}
				var policies []struct {
					PolicyDocument map[string]any `json:"PolicyDocument"`
				}
				if err := json.Unmarshal([]byte(printed), &policies); err != nil {
					t.Fatalf("parse the printed deploy credential: %v", err)
				}
				deploy, err := CredentialPolicies(defaultNamespace, edge.PurposeDeploy, tier, broughtKey)
				if err != nil {
					t.Fatalf("CredentialPolicies: %v", err)
				}
				stack, err := consoleTrustTemplate(partInputs{ns: defaultNamespace, tier: tier, trust: fixtureTrust(), deploy: deploy})
				if err != nil {
					t.Fatalf("consoleTrustTemplate: %v", err)
				}
				var parsed consoleTrustTemplateJSON
				if err := json.Unmarshal([]byte(stack.body), &parsed); err != nil {
					t.Fatalf("parse the console trust template: %v", err)
				}
				var attached []any
				for i, policy := range policies {
					id := "DeployPolicy" + strconv.Itoa(i+1)
					held := parsed.Resources[id]
					if got, want := asJSON(t, held.Properties["PolicyDocument"]), asJSON(t, policy.PolicyDocument); got != want {
						t.Errorf("%s holds\n%s\nwant what the deploy credential prints\n%s", id, got, want)
					}
					if want := defaultNamespace.deployPolicyStem(tier) + "-" + strconv.Itoa(i+1); held.Properties["ManagedPolicyName"] != want {
						t.Errorf("%s is named %v, want %s", id, held.Properties["ManagedPolicyName"], want)
					}
					attached = append(attached, map[string]any{"Ref": id})
				}
				if got := asJSON(t, parsed.Resources["DeployRole"].Properties["ManagedPolicyArns"]); got != asJSON(t, attached) {
					t.Errorf("the role attaches %s, want every deploy policy %s", got, asJSON(t, attached))
				}
				if len(parsed.Resources) != len(policies)+1 {
					t.Errorf("the template holds %d resources, want the role and its %d policies alone", len(parsed.Resources), len(policies))
				}
			})
		}
	}
}

func TestABootstrapTrustingAConsoleMakesItsIdentityProvidersBeforeTheRoleAndSaysTheTiersIdentity(t *testing.T) {
	stacks, ssmc, iamc := newFakeCFN(), newFakeSSM(), &fakeIAM{}
	apis := apisOf(stacks, ssmc, iamc, preloadedStore())
	providers := apis.IdentityProviders.(*fakeIdentityProviders)
	providers.stacks = stacks

	if err := Run(context.Background(), apis, defaultNamespace, environment.TierProduction, everything(), nil); err != nil {
		t.Fatalf("Run: %v", err)
	}

	if want := []string{fixtureIssuerHost, gitHubIssuerHost}; !reflect.DeepEqual(providers.before, want) {
		t.Errorf("made %v before the role's stack, want %v", providers.before, want)
	}
	for host, audience := range map[string]string{fixtureIssuerHost: fixtureAudience, gitHubIssuerHost: gitHubAudience} {
		made, ok := providers.has(host)
		if !ok {
			t.Fatalf("no identity provider for %s", host)
		}
		if !reflect.DeepEqual(made.clientIDs, []string{audience}) {
			t.Errorf("%s admits %v, want %s alone", host, made.clientIDs, audience)
		}
		if want := map[string]string{naming.ManagedByTagKey: string(defaultNamespace), "ocel:component": provider.ConsoleTrustPart}; !reflect.DeepEqual(made.tags, want) {
			t.Errorf("%s is tagged %v, want %v: made by this namespace for either tier's console trust", host, made.tags, want)
		}
	}

	read, err := Read(context.Background(), stacks, defaultNamespace, environment.TierProduction, fixtureDeploy(environment.TierProduction))
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	want := &provider.TierIdentity{
		IdentityProvider: fakeIdentityProviderARNPrefix + fixtureIssuerHost,
		Principal:        "arn:aws:test:us-east-1:111122223333:" + defaultNamespace.PartStackName(provider.ConsoleTrustPart, environment.TierProduction) + "/" + outputDeployRoleARN,
	}
	if got := read.Deployed.Identity(); got == nil || *got != *want {
		t.Errorf("Identity() = %+v, want %+v", got, want)
	}
	if got := read.Deployed.Trust(); got == nil || !reflect.DeepEqual(*got, *fixtureTrust()) {
		t.Errorf("Trust() = %+v, want the trust the part was applied with %+v", got, fixtureTrust())
	}
	for _, stack := range read.Deployed.Stacks {
		if stack.Part == provider.ConsoleTrustPart && !stack.Current() {
			t.Errorf("the console trust stack reads as stale right after it was written: %+v", stack)
		}
	}
	if stamp := stacks.stampOf(defaultNamespace.PartStackName(provider.ConsoleTrustPart, environment.TierProduction)); stamp.WrittenBy != everything().Writer.String() {
		t.Errorf("the console trust stack is stamped as written by %q, want %q", stamp.WrittenBy, everything().Writer.String())
	}
}

func TestABootstrapWithNoTrustLeavesTheInstalledTrustAsItIs(t *testing.T) {
	stacks, ssmc, iamc := newFakeCFN(), newFakeSSM(), &fakeIAM{}
	apis := apisOf(stacks, ssmc, iamc, preloadedStore())
	providers := apis.IdentityProviders.(*fakeIdentityProviders)
	if err := Run(context.Background(), apis, defaultNamespace, environment.TierProduction, everything(), nil); err != nil {
		t.Fatalf("Run: %v", err)
	}
	trustStack := defaultNamespace.PartStackName(provider.ConsoleTrustPart, environment.TierProduction)
	installed, calls := stacks.template(trustStack), len(providers.calls)

	untrusted := everything()
	untrusted.Trust = nil
	if err := Run(context.Background(), apis, defaultNamespace, environment.TierProduction, untrusted, nil); err != nil {
		t.Fatalf("Run with no trust: %v", err)
	}
	if stacks.template(trustStack) != installed {
		t.Errorf("a bootstrap with no trust rewrote the console trust stack")
	}
	if len(providers.calls) != calls {
		t.Errorf("a bootstrap with no trust called identity providers: %v", providers.calls[calls:])
	}

	read, err := Read(context.Background(), stacks, defaultNamespace, environment.TierProduction, fixtureDeploy(environment.TierProduction))
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	changes, err := plannedTrustedIssuers(context.Background(), ParamAPIs{Stacks: stacks, IdentityProviders: providers}, defaultNamespace, environment.TierProduction, untrusted)
	if err != nil || len(changes) != 0 {
		t.Errorf("plannedTrustedIssuers = %v, %v, want no change", changes, err)
	}
	for _, stack := range read.Deployed.Stacks {
		if stack.Part == provider.ConsoleTrustPart && !stack.Current() {
			t.Errorf("with no trust handed in, the installed console trust reads as stale: %+v", stack)
		}
	}
}

func TestTheBootstrapCredentialMakesTheConsoleTrustOfItsOwnTierAlone(t *testing.T) {
	for _, tier := range bothTiers {
		t.Run(string(tier), func(t *testing.T) {
			bootstrap, _ := renderedCredentialsOf(t, tier)
			path := defaultNamespace.bootstrapRolePathFor(tier)
			role := "arn:aws:iam::111122223333:role" + path + defaultNamespace.DeployRoleName(tier)
			policy := "arn:aws:iam::111122223333:policy" + path + defaultNamespace.deployPolicyStem(tier) + "-1"
			issuer := fakeIdentityProviderARNPrefix + fixtureIssuerHost
			tagging := requestContext{"aws:RequestTag/" + naming.EnvTierTagKey: string(tier)}
			madeForTrust := requestContext{"aws:RequestTag/ocel:component": provider.ConsoleTrustPart}
			heldForTrust := requestContext{"aws:ResourceTag/ocel:component": provider.ConsoleTrustPart}
			for _, c := range []struct {
				action, arn string
				request     requestContext
			}{
				{"iam:CreateRole", role, tagging},
				{"iam:TagRole", role, taggedWithTier(tier, tagging)},
				{"iam:GetRole", role, nil},
				{"iam:UpdateRole", role, taggedWithTier(tier, nil)},
				{"iam:UpdateAssumeRolePolicy", role, taggedWithTier(tier, nil)},
				{"iam:DeleteRole", role, taggedWithTier(tier, nil)},
				{"iam:AttachRolePolicy", role, taggedWithTier(tier, requestContext{"iam:PolicyARN": policy})},
				{"iam:DetachRolePolicy", role, taggedWithTier(tier, requestContext{"iam:PolicyARN": policy})},
				{"iam:CreatePolicy", policy, nil},
				{"iam:CreatePolicyVersion", policy, nil},
				{"iam:DeletePolicyVersion", policy, nil},
				{"iam:DeletePolicy", policy, nil},
				{"iam:GetOpenIDConnectProvider", issuer, nil},
				{"iam:CreateOpenIDConnectProvider", issuer, madeForTrust},
				{"iam:TagOpenIDConnectProvider", issuer, madeForTrust},
				{"iam:AddClientIDToOpenIDConnectProvider", issuer, heldForTrust},
				{"iam:RemoveClientIDFromOpenIDConnectProvider", issuer, heldForTrust},
				{"iam:DeleteOpenIDConnectProvider", issuer, heldForTrust},
			} {
				if !allows(t, bootstrap, c.action, c.arn, c.request) {
					t.Errorf("the %s bootstrap credential cannot %s on %s", tier, c.action, c.arn)
				}
			}
			for _, action := range []string{"iam:AddClientIDToOpenIDConnectProvider", "iam:RemoveClientIDFromOpenIDConnectProvider", "iam:DeleteOpenIDConnectProvider", "iam:TagOpenIDConnectProvider"} {
				if allows(t, bootstrap, action, issuer, taggedWithTier(tier, nil)) {
					t.Errorf("the %s bootstrap credential can %s on an identity provider no console trust made", tier, action)
				}
			}
			if allows(t, bootstrap, "iam:AttachRolePolicy", role, taggedWithTier(tier, requestContext{"iam:PolicyARN": "arn:aws:iam::aws:policy/AdministratorAccess"})) {
				t.Errorf("the %s bootstrap credential can attach AdministratorAccess to the deploy role", tier)
			}
		})
	}
}

func TestReadingAConsoleTrustThatRecordsNoTrustFails(t *testing.T) {
	stacks, ssmc, iamc := newFakeCFN(), newFakeSSM(), &fakeIAM{}
	if err := Run(context.Background(), apisOf(stacks, ssmc, iamc, preloadedStore()), defaultNamespace, environment.TierProduction, everything(), nil); err != nil {
		t.Fatalf("Run: %v", err)
	}
	stacks.mu.Lock()
	delete(stacks.outputs[defaultNamespace.PartStackName(provider.ConsoleTrustPart, environment.TierProduction)], outputTrustedIssuer)
	stacks.mu.Unlock()

	if _, err := Read(context.Background(), stacks, defaultNamespace, environment.TierProduction, fixtureDeploy(environment.TierProduction)); err == nil || !strings.Contains(err.Error(), provider.ConsoleTrustPart) {
		t.Errorf("Read() error = %v, want it to name the %s part whose template cannot be rendered", err, provider.ConsoleTrustPart)
	}
}
