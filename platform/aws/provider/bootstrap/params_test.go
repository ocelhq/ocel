package bootstrap

import (
	"context"
	"testing"

	"github.com/ocelhq/ocel/pkg/providerkit"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

func cloudflareAdoption() edge.Adoption {
	return edge.Adoption{
		Values: map[string]string{"cacheBucket": "ocel-edge-cache"},
		Offers: []edge.OfferKind{edge.OfferCacheStore, edge.OfferDeploymentsStore, edge.OfferISRWriter},
	}
}

func fronting() Request { return Request{Features: []string{FeatureCloudflareEdge}} }

func standingParams(t *testing.T) (*fakeSSM, *fakeIAM) {
	t.Helper()

	ctx := context.Background()
	ssmc, iamc := newFakeSSM(), &fakeIAM{}
	if _, err := ensureOriginSecret(ctx, ssmc, ClassProduction); err != nil {
		t.Fatalf("ensureOriginSecret: %v", err)
	}
	if _, err := ensurePassphrase(ctx, ssmc); err != nil {
		t.Fatalf("ensurePassphrase: %v", err)
	}
	if _, err := ensureEdgeCredentials(ctx, iamc, ssmc, ClassProduction, KindCloudflare); err != nil {
		t.Fatalf("ensureEdgeCredentials: %v", err)
	}
	if err := adoptCacheStore(ctx, ssmc, ClassProduction, KindCloudflare, offeredStore()); err != nil {
		t.Fatalf("adoptCacheStore: %v", err)
	}
	if err := adoptDeploymentsStore(ctx, ssmc, ClassProduction, KindCloudflare, offeredDeploymentsStore()); err != nil {
		t.Fatalf("adoptDeploymentsStore: %v", err)
	}
	if err := adoptISRWriter(ctx, ssmc, ClassProduction, KindCloudflare, offeredISRWriter("", "cred-prod")); err != nil {
		t.Fatalf("adoptISRWriter: %v", err)
	}
	if _, err := ensureISRWriterSeed(ctx, ssmc, ClassProduction, KindCloudflare); err != nil {
		t.Fatalf("ensureISRWriterSeed: %v", err)
	}
	if err := writeEdgeValues(ctx, ssmc, ClassProduction, KindCloudflare, cloudflareAdoption().Values); err != nil {
		t.Fatalf("writeEdgeValues: %v", err)
	}
	return ssmc, iamc
}

func plannedParams(t *testing.T, ssmc *fakeSSM, iamc *fakeIAM, req Request) providerkit.ChangeGroup {
	t.Helper()

	group, err := PlanParameters(context.Background(), ParamAPIs{SSM: ssmc, IAM: iamc},
		ClassProduction, KindCloudflare, cloudflareAdoption(), req)
	if err != nil {
		t.Fatalf("PlanParameters: %v", err)
	}
	return group
}

func actions(group providerkit.ChangeGroup) map[string]providerkit.ChangeAction {
	by := make(map[string]providerkit.ChangeAction, len(group.Changes))
	for _, change := range group.Changes {
		by[change.Name] = change.Action
	}
	return by
}

func TestPlanParametersOnAFreshAccountCreatesEveryStandingParameter(t *testing.T) {
	group := plannedParams(t, newFakeSSM(), &fakeIAM{}, fronting())

	if group.Kind != providerkit.ParameterGroupKind || group.Name != ParamGroupName || group.Feature != "" {
		t.Errorf("group = %+v, want the unfeatured parameters group", group)
	}
	if group.Action != providerkit.ActionCreate {
		t.Errorf("group action = %q, want create where nothing stands", group.Action)
	}
	names := cloudflareNames(ClassProduction)
	want := []string{
		OriginSecretParamName,
		PassphraseParamName,
		names.valuesParam,
		names.cacheStoreParam,
		names.deploymentsStoreParam,
		names.isrWriterParam,
		names.isrWriterSeedParam,
		names.credentialsParam,
		names.user,
	}
	got := actions(group)
	if len(got) != len(want) {
		t.Fatalf("plan rows = %+v, want one per standing resource: %v", group.Changes, want)
	}
	for _, name := range want {
		if got[name] != providerkit.ActionCreate {
			t.Errorf("%s = %q, want create", name, got[name])
		}
	}
	for _, change := range group.Changes {
		wantKind := kindParameter
		if change.Name == names.user {
			wantKind = kindAccessKey
		}
		if change.Kind != wantKind {
			t.Errorf("%s is a %q, want %q", change.Name, change.Kind, wantKind)
		}
	}
}

func TestPlanParametersOnAConvergedAccountKeepsEverything(t *testing.T) {
	ssmc, iamc := standingParams(t)
	writes := ssmc.puts

	group := plannedParams(t, ssmc, iamc, fronting())

	if group.Action != providerkit.ActionKeep || group.Reason != paramCurrent {
		t.Errorf("group = %q (%q), want a keep that says it is already current", group.Action, group.Reason)
	}
	for _, change := range group.Changes {
		if change.Action != providerkit.ActionKeep {
			t.Errorf("%s = %q, want keep: the apply would rewrite nothing", change.Name, change.Action)
		}
	}
	if ssmc.puts != writes {
		t.Errorf("planning wrote %d parameters, want a plan that only reads", ssmc.puts-writes)
	}
}

func TestPlanParametersUpdatesOnlyTheValuesTheEdgeWouldRewrite(t *testing.T) {
	ssmc, iamc := standingParams(t)
	if err := writeEdgeValues(context.Background(), ssmc, ClassProduction, KindCloudflare, map[string]string{"cacheBucket": "stale"}); err != nil {
		t.Fatalf("writeEdgeValues: %v", err)
	}

	group := plannedParams(t, ssmc, iamc, fronting())

	if group.Action != providerkit.ActionUpdate {
		t.Errorf("group action = %q, want update where one parameter drifted", group.Action)
	}
	names := cloudflareNames(ClassProduction)
	for _, change := range group.Changes {
		want := providerkit.ActionKeep
		if change.Name == names.valuesParam {
			want = providerkit.ActionUpdate
		}
		if change.Action != want {
			t.Errorf("%s = %q, want %q", change.Name, change.Action, want)
		}
	}
	if got := actions(group)[names.valuesParam]; got != providerkit.ActionUpdate {
		t.Fatalf("%s = %q, want the drifted values updated", names.valuesParam, got)
	}
}

func TestPlanParametersLeavesOutWhatAnEdgeThatAdoptsNothingNeverWrites(t *testing.T) {
	group, err := PlanParameters(context.Background(), ParamAPIs{SSM: newFakeSSM(), IAM: &fakeIAM{}},
		ClassProduction, "cloudfront", edge.Adoption{}, Request{Features: []string{FeatureCloudFrontEdge}})
	if err != nil {
		t.Fatalf("PlanParameters: %v", err)
	}
	got := actions(group)
	if len(got) != 2 {
		t.Fatalf("plan rows = %+v, want the origin secret and the passphrase alone", group.Changes)
	}
	for _, name := range []string{OriginSecretParamName, PassphraseParamName} {
		if got[name] != providerkit.ActionCreate {
			t.Errorf("%s = %q, want create", name, got[name])
		}
	}
}

func TestPlanParametersShowsWhatSeveringTheEdgeTakesWithIt(t *testing.T) {
	ssmc, iamc := standingParams(t)

	group := plannedParams(t, ssmc, iamc, Request{Remove: []string{FeatureCloudflareEdge}})

	names := cloudflareNames(ClassProduction)
	got := actions(group)
	for _, name := range append(names.edgeParams(), names.user) {
		if got[name] != providerkit.ActionDelete {
			t.Errorf("%s = %q, want delete: severing the edge takes it", name, got[name])
		}
	}
	for _, name := range []string{OriginSecretParamName, PassphraseParamName} {
		if got[name] != providerkit.ActionKeep {
			t.Errorf("%s = %q, want keep: severing the edge leaves the core parameters", name, got[name])
		}
	}
	if len(got) != len(names.edgeParams())+3 {
		t.Errorf("plan rows = %+v, want the core parameters, the access key and the edge parameters", group.Changes)
	}
}

func TestPlanParametersRemintsTheKeyTheAccountNoLongerHolds(t *testing.T) {
	ssmc, iamc := standingParams(t)
	iamc.keys = nil

	group := plannedParams(t, ssmc, iamc, fronting())

	names := cloudflareNames(ClassProduction)
	for _, change := range group.Changes {
		switch change.Name {
		case names.credentialsParam:
			if change.Action != providerkit.ActionUpdate || change.Reason != keyGone {
				t.Errorf("%s = %q (%q), want an update that says why", change.Name, change.Action, change.Reason)
			}
		case names.user:
			if change.Action != providerkit.ActionCreate {
				t.Errorf("%s = %q, want a fresh key minted", change.Name, change.Action)
			}
		}
	}
}
