package account

import (
	"context"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsiam "github.com/aws/aws-sdk-go-v2/service/iam"
	iamtypes "github.com/aws/aws-sdk-go-v2/service/iam/types"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/provider"
)

const fakeIdentityProviderARNPrefix = "arn:aws:iam::111122223333:oidc-provider/"

type fakeIdentityProvider struct {
	clientIDs []string
	tags      map[string]string
}

type fakeIdentityProviders struct {
	mu        sync.Mutex
	providers map[string]*fakeIdentityProvider
	calls     []string
	stacks    *fakeCFN
	before    []string
}

func newFakeIdentityProviders() *fakeIdentityProviders {
	return &fakeIdentityProviders{providers: map[string]*fakeIdentityProvider{}}
}

func (f *fakeIdentityProviders) madeElsewhere(host string, clientIDs ...string) {
	f.providers[fakeIdentityProviderARNPrefix+host] = &fakeIdentityProvider{clientIDs: clientIDs, tags: map[string]string{}}
}

func (f *fakeIdentityProviders) GetOpenIDConnectProvider(_ context.Context, in *awsiam.GetOpenIDConnectProviderInput, _ ...func(*awsiam.Options)) (*awsiam.GetOpenIDConnectProviderOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	found, ok := f.providers[aws.ToString(in.OpenIDConnectProviderArn)]
	if !ok {
		return nil, &iamtypes.NoSuchEntityException{Message: aws.String("no such provider")}
	}
	out := &awsiam.GetOpenIDConnectProviderOutput{ClientIDList: slices.Clone(found.clientIDs)}
	for key, value := range found.tags {
		out.Tags = append(out.Tags, iamtypes.Tag{Key: aws.String(key), Value: aws.String(value)})
	}
	return out, nil
}

func (f *fakeIdentityProviders) CreateOpenIDConnectProvider(_ context.Context, in *awsiam.CreateOpenIDConnectProviderInput, _ ...func(*awsiam.Options)) (*awsiam.CreateOpenIDConnectProviderOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	host := strings.TrimPrefix(aws.ToString(in.Url), "https://")
	named := fakeIdentityProviderARNPrefix + host
	if _, ok := f.providers[named]; ok {
		return nil, &iamtypes.EntityAlreadyExistsException{Message: aws.String("exists")}
	}
	if f.stacks != nil && f.stacks.template(defaultNamespace.PartStackName(provider.ConsoleTrustPart, environment.TierProduction)) == "" {
		f.before = append(f.before, host)
	}
	tags := map[string]string{}
	for _, tag := range in.Tags {
		tags[aws.ToString(tag.Key)] = aws.ToString(tag.Value)
	}
	f.providers[named] = &fakeIdentityProvider{clientIDs: slices.Clone(in.ClientIDList), tags: tags}
	f.calls = append(f.calls, "create "+host)
	return &awsiam.CreateOpenIDConnectProviderOutput{OpenIDConnectProviderArn: aws.String(named)}, nil
}

func (f *fakeIdentityProviders) AddClientIDToOpenIDConnectProvider(_ context.Context, in *awsiam.AddClientIDToOpenIDConnectProviderInput, _ ...func(*awsiam.Options)) (*awsiam.AddClientIDToOpenIDConnectProviderOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	named := aws.ToString(in.OpenIDConnectProviderArn)
	found, ok := f.providers[named]
	if !ok {
		return nil, &iamtypes.NoSuchEntityException{Message: aws.String("no such provider")}
	}
	found.clientIDs = append(found.clientIDs, aws.ToString(in.ClientID))
	f.calls = append(f.calls, "add "+aws.ToString(in.ClientID)+" to "+strings.TrimPrefix(named, fakeIdentityProviderARNPrefix))
	return &awsiam.AddClientIDToOpenIDConnectProviderOutput{}, nil
}

func (f *fakeIdentityProviders) RemoveClientIDFromOpenIDConnectProvider(_ context.Context, in *awsiam.RemoveClientIDFromOpenIDConnectProviderInput, _ ...func(*awsiam.Options)) (*awsiam.RemoveClientIDFromOpenIDConnectProviderOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	named := aws.ToString(in.OpenIDConnectProviderArn)
	if found, ok := f.providers[named]; ok {
		found.clientIDs = slices.DeleteFunc(found.clientIDs, func(id string) bool { return id == aws.ToString(in.ClientID) })
	}
	f.calls = append(f.calls, "remove "+aws.ToString(in.ClientID)+" from "+strings.TrimPrefix(named, fakeIdentityProviderARNPrefix))
	return &awsiam.RemoveClientIDFromOpenIDConnectProviderOutput{}, nil
}

func (f *fakeIdentityProviders) DeleteOpenIDConnectProvider(_ context.Context, in *awsiam.DeleteOpenIDConnectProviderInput, _ ...func(*awsiam.Options)) (*awsiam.DeleteOpenIDConnectProviderOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	named := aws.ToString(in.OpenIDConnectProviderArn)
	delete(f.providers, named)
	f.calls = append(f.calls, "delete "+strings.TrimPrefix(named, fakeIdentityProviderARNPrefix))
	return &awsiam.DeleteOpenIDConnectProviderOutput{}, nil
}

func (f *fakeIdentityProviders) has(host string) (*fakeIdentityProvider, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	found, ok := f.providers[fakeIdentityProviderARNPrefix+host]
	return found, ok
}

func TestABootstrapReusesAnIdentityProviderItFindsAndAddsTheAudienceItLacks(t *testing.T) {
	stacks, ssmc, iamc := newFakeCFN(), newFakeSSM(), &fakeIAM{}
	apis := apisOf(stacks, ssmc, iamc, preloadedStore())
	providers := apis.IdentityProviders.(*fakeIdentityProviders)
	providers.madeElsewhere(fixtureIssuerHost, "someone-else")

	if err := Run(context.Background(), apis, defaultNamespace, environment.TierProduction, everything(), nil); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if want := []string{"add " + fixtureAudience + " to " + fixtureIssuerHost}; !reflect.DeepEqual(providers.calls, want) {
		t.Errorf("identity provider calls = %v, want %v", providers.calls, want)
	}
}

func TestPlanShowsTheIdentityProvidersABootstrapWouldMakeOrChange(t *testing.T) {
	for _, tc := range []struct {
		name  string
		found []string
		want  provider.Change
	}{
		{name: "missing", want: provider.Change{Kind: identityProviderKind, Name: fixtureIssuerHost, Action: provider.ActionCreate}},
		{name: "lacking the audience", found: []string{"other"}, want: provider.Change{Kind: identityProviderKind, Name: fixtureIssuerHost, Action: provider.ActionUpdate, Reason: "adds audience " + fixtureAudience}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stacks, ssmc, iamc := newFakeCFN(), newFakeSSM(), &fakeIAM{}
			apis := apisOf(stacks, ssmc, iamc, preloadedStore())
			providers := apis.IdentityProviders.(*fakeIdentityProviders)
			core := everything()
			core.Parts, core.Trust = nil, nil
			if err := Run(context.Background(), apis, defaultNamespace, environment.TierProduction, core, nil); err != nil {
				t.Fatalf("Run: %v", err)
			}
			if tc.found != nil {
				providers.madeElsewhere(fixtureIssuerHost, tc.found...)
			}
			changes, err := plannedTrustedIssuer(context.Background(), ParamAPIs{Stacks: stacks, IdentityProviders: providers}, defaultNamespace, environment.TierProduction, everything())
			if err != nil {
				t.Fatalf("plannedTrustedIssuer: %v", err)
			}
			if want := []provider.Change{tc.want}; !reflect.DeepEqual(changes, want) {
				t.Errorf("planned %+v, want %+v", changes, want)
			}
		})
	}
}

func trustedInBothTiers(t *testing.T) (*fakeCFN, APIs, *fakeIdentityProviders) {
	t.Helper()
	stacks, ssmc, iamc := newFakeCFN(), newFakeSSM(), &fakeIAM{}
	apis := apisOf(stacks, ssmc, iamc, preloadedStore())
	providers := apis.IdentityProviders.(*fakeIdentityProviders)
	for _, tier := range bothTiers {
		if err := Run(context.Background(), apis, defaultNamespace, tier, everything(), nil); err != nil {
			t.Fatalf("Run %s: %v", tier, err)
		}
	}
	return stacks, apis, providers
}

func TestRemovingTheTrustFromBothTiersDeletesTheIdentityProviderThisNamespaceMadeWhicheverTierGoesFirst(t *testing.T) {
	for _, order := range [][]environment.Tier{
		{environment.TierPreview, environment.TierProduction},
		{environment.TierProduction, environment.TierPreview},
	} {
		t.Run(string(order[0])+" first", func(t *testing.T) {
			stacks, apis, providers := trustedInBothTiers(t)
			drop := Request{Parts: []string{CloudflareEdgePart, provider.VariablesKeyPart}, Remove: []string{provider.ConsoleTrustPart}}

			planned, err := plannedIssuerRelease(context.Background(), ParamAPIs{Stacks: stacks, IdentityProviders: providers}, defaultNamespace, order[0])
			if err != nil || len(planned) != 0 {
				t.Errorf("planned release from %s = %v, %v, want none while %s trusts the same providers", order[0], planned, err, order[1])
			}
			if err := Run(context.Background(), apis, defaultNamespace, order[0], drop, nil); err != nil {
				t.Fatalf("drop from %s: %v", order[0], err)
			}
			if _, ok := providers.has(fixtureIssuerHost); !ok {
				t.Fatalf("dropping %s's trust deleted the provider %s still trusts", order[0], order[1])
			}

			planned, err = plannedIssuerRelease(context.Background(), ParamAPIs{Stacks: stacks, IdentityProviders: providers}, defaultNamespace, order[1])
			if want := []provider.Change{{Kind: identityProviderKind, Name: fixtureIssuerHost, Action: provider.ActionDelete}}; err != nil || !reflect.DeepEqual(planned, want) {
				t.Errorf("planned release from %s = %+v, %v, want %+v", order[1], planned, err, want)
			}
			if err := Run(context.Background(), apis, defaultNamespace, order[1], drop, nil); err != nil {
				t.Fatalf("drop from %s: %v", order[1], err)
			}
			if _, ok := providers.has(fixtureIssuerHost); ok {
				t.Error("the console's identity provider survived the last trust in it")
			}
		})
	}
}

func TestRemovingTheTrustOrTearingTheTierDownKeepsAnIdentityProviderOcelDidNotMake(t *testing.T) {
	for _, tc := range []struct {
		name   string
		remove func(*testing.T, *fakeCFN, *fakeSSM, APIs, *fakeIdentityProviders) error
	}{
		{name: "removing the trust", remove: func(_ *testing.T, _ *fakeCFN, _ *fakeSSM, apis APIs, _ *fakeIdentityProviders) error {
			drop := Request{Remove: []string{provider.ConsoleTrustPart}}
			return Run(context.Background(), apis, defaultNamespace, environment.TierProduction, drop, nil)
		}},
		{name: "tearing the tier down", remove: func(_ *testing.T, stacks *fakeCFN, ssmc *fakeSSM, _ APIs, providers *fakeIdentityProviders) error {
			return Teardown(context.Background(), TeardownAPIs{
				CFN: stacks, SSM: ssmc, IAM: &teardownIAM{keys: map[string][]string{}}, Layers: newFakeLayers(), Records: newFakeRecords().in,
				Buckets: &teardownS3{pages: map[string][]objectPage{}}, IdentityProviders: providers,
			}, defaultNamespace, environment.TierProduction, nil)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stacks, ssmc, iamc := newFakeCFN(), newFakeSSM(), &fakeIAM{}
			apis := apisOf(stacks, ssmc, iamc, preloadedStore())
			providers := apis.IdentityProviders.(*fakeIdentityProviders)
			providers.madeElsewhere(fixtureIssuerHost, fixtureAudience)
			trusted := everything()
			trusted.Parts = []string{provider.ConsoleTrustPart}
			if err := Run(context.Background(), apis, defaultNamespace, environment.TierProduction, trusted, nil); err != nil {
				t.Fatalf("Run: %v", err)
			}

			planned, err := plannedIssuerRelease(context.Background(), ParamAPIs{Stacks: stacks, IdentityProviders: providers}, defaultNamespace, environment.TierProduction)
			if err != nil || len(planned) != 0 {
				t.Errorf("planned release = %+v, %v, want none for a provider Ocel did not make", planned, err)
			}
			if err := tc.remove(t, stacks, ssmc, apis, providers); err != nil {
				t.Fatalf("%s: %v", tc.name, err)
			}
			if made, ok := providers.has(fixtureIssuerHost); !ok || !reflect.DeepEqual(made.clientIDs, []string{fixtureAudience}) {
				t.Errorf("%s left the provider Ocel did not make as %+v, want it untouched", tc.name, made)
			}
			for _, call := range providers.calls {
				if strings.HasPrefix(call, "delete ") || strings.HasPrefix(call, "remove ") {
					t.Errorf("identity provider call %q touched a provider Ocel did not make", call)
				}
			}
		})
	}
}

func TestATrustThatMovesReleasesWhatTheTierTrustedBefore(t *testing.T) {
	moved := func(change func(*provider.WebIdentityTrust)) Request {
		req := everything()
		req.Trust = fixtureTrust()
		change(req.Trust)
		return req
	}
	for _, tc := range []struct {
		name    string
		req     Request
		planned provider.Change
		left    func(*fakeIdentityProviders) string
	}{
		{
			name:    "to another issuer",
			req:     moved(func(trust *provider.WebIdentityTrust) { trust.Issuer = "https://other.example.com/oidc" }),
			planned: provider.Change{Kind: identityProviderKind, Name: fixtureIssuerHost, Action: provider.ActionDelete},
			left: func(providers *fakeIdentityProviders) string {
				if _, ok := providers.has(fixtureIssuerHost); ok {
					return "the provider of the issuer no longer trusted survived"
				}
				if _, ok := providers.has("other.example.com/oidc"); !ok {
					return "no provider was made for the issuer now trusted"
				}
				return ""
			},
		},
		{
			name:    "to another audience",
			req:     moved(func(trust *provider.WebIdentityTrust) { trust.Audience = "https://moved.example.com" }),
			planned: provider.Change{Kind: identityProviderKind, Name: fixtureIssuerHost, Action: provider.ActionUpdate, Reason: "drops audience " + fixtureAudience},
			left: func(providers *fakeIdentityProviders) string {
				made, _ := providers.has(fixtureIssuerHost)
				if made == nil || !reflect.DeepEqual(made.clientIDs, []string{"https://moved.example.com"}) {
					return "the provider does not admit the moved audience alone"
				}
				return ""
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stacks, ssmc, iamc := newFakeCFN(), newFakeSSM(), &fakeIAM{}
			apis := apisOf(stacks, ssmc, iamc, preloadedStore())
			providers := apis.IdentityProviders.(*fakeIdentityProviders)
			if err := Run(context.Background(), apis, defaultNamespace, environment.TierProduction, everything(), nil); err != nil {
				t.Fatalf("Run: %v", err)
			}

			planned, err := plannedTrustedIssuer(context.Background(), ParamAPIs{Stacks: stacks, IdentityProviders: providers}, defaultNamespace, environment.TierProduction, tc.req)
			if err != nil {
				t.Fatalf("plannedTrustedIssuer: %v", err)
			}
			if !slices.Contains(planned, tc.planned) {
				t.Errorf("planned %+v, want %+v among them", planned, tc.planned)
			}
			if err := Run(context.Background(), apis, defaultNamespace, environment.TierProduction, tc.req, nil); err != nil {
				t.Fatalf("Run with the moved trust: %v", err)
			}
			if problem := tc.left(providers); problem != "" {
				t.Error(problem)
			}
		})
	}
}

func TestATrustThatMovesKeepsWhatTheOtherTierStillTrusts(t *testing.T) {
	_, apis, providers := trustedInBothTiers(t)
	req := everything()
	req.Trust = fixtureTrust()
	req.Trust.Issuer = "https://other.example.com/oidc"
	if err := Run(context.Background(), apis, defaultNamespace, environment.TierProduction, req, nil); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if _, ok := providers.has(fixtureIssuerHost); !ok {
		t.Error("moving production's trust deleted the provider preview still trusts")
	}
}

func TestTearingATierDownReleasesTheIdentityProvidersItsTrustMade(t *testing.T) {
	stacks, ssmc, iamc := newFakeCFN(), newFakeSSM(), &fakeIAM{}
	apis := apisOf(stacks, ssmc, iamc, preloadedStore())
	providers := apis.IdentityProviders.(*fakeIdentityProviders)
	trusted := everything()
	trusted.Parts = []string{provider.ConsoleTrustPart}
	if err := Run(context.Background(), apis, defaultNamespace, environment.TierProduction, trusted, nil); err != nil {
		t.Fatalf("Run: %v", err)
	}

	planned, err := PlanParameterRemoval(context.Background(), ParamAPIs{SSM: ssmc, IAM: iamc, Stacks: stacks, IdentityProviders: providers}, defaultNamespace, environment.TierProduction)
	if err != nil {
		t.Fatalf("PlanParameterRemoval: %v", err)
	}
	if !slices.Contains(planned.Changes, provider.Change{Kind: identityProviderKind, Name: fixtureIssuerHost, Action: provider.ActionDelete}) {
		t.Errorf("the removal plan %+v does not show identity provider %s going", planned.Changes, fixtureIssuerHost)
	}

	if err := Teardown(context.Background(), TeardownAPIs{
		CFN: stacks, SSM: ssmc, IAM: &teardownIAM{keys: map[string][]string{}}, Layers: newFakeLayers(), Records: newFakeRecords().in,
		Buckets: &teardownS3{pages: map[string][]objectPage{}}, IdentityProviders: providers,
	}, defaultNamespace, environment.TierProduction, nil); err != nil {
		t.Fatalf("Teardown: %v", err)
	}
	if _, ok := providers.has(fixtureIssuerHost); ok {
		t.Errorf("identity provider %s survived the teardown of the only tier trusting it", fixtureIssuerHost)
	}
}
