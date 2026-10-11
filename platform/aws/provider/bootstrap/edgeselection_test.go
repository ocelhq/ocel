package bootstrap

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/platform/aws/provider/cfn"
)

var everyEdgeKind = []edge.Kind{KindCloudflare, KindCloudFront, KindAPIGateway}

func TestTheCoreIsTheSameWhicheverEdgeFrontsIt(t *testing.T) {
	for _, tier := range []environment.Tier{environment.TierProduction, environment.TierPreview} {
		t.Run(string(tier), func(t *testing.T) {
			want := cfn.TemplateDigest(coreStackTemplate(defaultNamespace, tier, ""))
			core, err := defaultNamespace.StackNameFor(tier)
			if err != nil {
				t.Fatalf("StackNameFor: %v", err)
			}
			for _, kind := range everyEdgeKind {
				stacks, ssmc, iamc := newFakeCFN(), newFakeSSM(), &fakeIAM{}
				apis := apisFronting(stacks, ssmc, iamc, preloadedStore(), &fakeEdge{kind: kind})
				if err := Run(context.Background(), apis, defaultNamespace, tier, Request{}, nil); err != nil {
					t.Fatalf("bootstrapping behind the %s edge: %v", kind, err)
				}
				if got := cfn.TemplateDigest(stacks.template(core)); got != want {
					t.Errorf("the %s edge wrote a core whose digest is %q, want %q: the core is what every edge is installed beside, not part of one", kind, got, want)
				}
				if got := stacks.stampOf(core).Digest; got != want {
					t.Errorf("the %s edge stamped the core %q, want %q", kind, got, want)
				}
			}
		})
	}
}

func TestReadingABootstrapSeesEveryInstalledEdge(t *testing.T) {
	stamp := Stamp{}
	api := stubStacksAPI{
		coreStackName: outputs(map[string]string{
			outputInfraTier:   string(environment.TierProduction),
			outputAssetBucket: "assets-1",
		}).stamped(stamp),
		defaultNamespace.FeatureStackName(FeatureISR, environment.TierProduction):            outputs(nil).stamped(stamp),
		defaultNamespace.FeatureStackName(FeatureCloudflareEdge, environment.TierProduction): outputs(nil).stamped(stamp),
		defaultNamespace.FeatureStackName(FeatureCloudFrontEdge, environment.TierProduction): outputs(map[string]string{
			OutputEdgeResolverARN: "arn:aws:cloudfront::111122223333:function/ocel-resolver",
			OutputEdgeCachePolicy: "cache-1",
		}).stamped(stamp),
	}

	got, err := CheckDeployed(context.Background(), api, defaultNamespace)
	if err != nil {
		t.Fatalf("CheckDeployed: %v", err)
	}
	want := []string{FeatureISR, FeatureCloudflareEdge, FeatureCloudFrontEdge}
	if !slices.Equal(got.Features.Names(), want) {
		t.Errorf("Features = %v, want %v: an account may have a stack for more than one edge at a time", got.Features.Names(), want)
	}
	for key, value := range map[string]string{
		outputAssetBucket:     "assets-1",
		OutputEdgeResolverARN: "arn:aws:cloudfront::111122223333:function/ocel-resolver",
		OutputEdgeCachePolicy: "cache-1",
	} {
		if got.Outputs[key] != value {
			t.Errorf("Outputs[%s] = %q, want %q: a deploy reads the core and every installed feature stack as one", key, got.Outputs[key], value)
		}
	}
}

func TestBootstrappingOneEdgeLeavesAnotherEdgesStackAlone(t *testing.T) {
	ctx := context.Background()
	stacks, ssmc, iamc := newFakeCFN(), newFakeSSM(), &fakeIAM{}
	store := preloadedStore()

	cloudflare := apisFronting(stacks, ssmc, iamc, store, &fakeEdge{kind: KindCloudflare})
	if err := Run(ctx, cloudflare, defaultNamespace, environment.TierProduction, Request{Features: []string{FeatureISR, FeatureCloudflareEdge}}, nil); err != nil {
		t.Fatalf("bootstrapping behind the cloudflare edge: %v", err)
	}

	existing := defaultNamespace.FeatureStackName(FeatureCloudflareEdge, environment.TierProduction)
	eventsBefore := len(stacks.events)
	restamps := stacks.restamps

	cloudfront := apisFronting(stacks, ssmc, iamc, store, &fakeEdge{kind: KindCloudFront})
	if err := Run(ctx, cloudfront, defaultNamespace, environment.TierProduction, Request{Features: []string{FeatureCloudFrontEdge}}, nil); err != nil {
		t.Fatalf("bootstrapping behind the cloudfront edge: %v", err)
	}

	written := defaultNamespace.FeatureStackName(FeatureCloudFrontEdge, environment.TierProduction)
	if !slices.Contains(stacks.stacks(), written) {
		t.Fatalf("stacks = %v, want the cloudfront edge among them", stacks.stacks())
	}
	for _, event := range stacks.events[eventsBefore:] {
		if strings.HasSuffix(event, existing) {
			t.Errorf("the run %s the edge it was not asked for; switching the front an account is bootstrapped behind takes nothing away from the one it was bootstrapped behind before", event)
		}
	}
	if stacks.restamps != restamps {
		t.Errorf("the run restamped %d stacks, want none of them the edge it was not asked for", stacks.restamps-restamps)
	}
	if !slices.Contains(stacks.stacks(), existing) {
		t.Errorf("stacks = %v, want the cloudflare edge still installed", stacks.stacks())
	}
}

func TestAnEdgeFeatureInstallsItsOwnEdgeWhateverFrontsTheRun(t *testing.T) {
	ctx := context.Background()
	stacks, ssmc, iamc := newFakeCFN(), newFakeSSM(), &fakeIAM{}
	registry := newFakeEdges(&fakeEdge{kind: KindCloudflare})
	apis := apisAcross(stacks, ssmc, iamc, preloadedStore(), &fakeEdge{kind: KindCloudFront}, registry)

	if err := Run(ctx, apis, defaultNamespace, environment.TierProduction,
		Request{Features: []string{FeatureISR, FeatureCloudflareEdge, FeatureCloudFrontEdge}}, nil); err != nil {
		t.Fatalf("bootstrapping the cloudflare edge feature behind the cloudfront front: %v", err)
	}

	front := registry.at(KindCloudflare)
	if front.bootstraps != 1 {
		t.Fatalf("the cloudflare edge was bootstrapped %d times, want once: the feature owns the edge, not the front this run picked", front.bootstraps)
	}
}

func TestTheFrontThisRunPicksIsBootstrappedOnce(t *testing.T) {
	stacks, ssmc, iamc := newFakeCFN(), newFakeSSM(), &fakeIAM{}
	front := &fakeEdge{kind: KindCloudflare}
	apis := apisFronting(stacks, ssmc, iamc, preloadedStore(), front)

	if err := Run(context.Background(), apis, defaultNamespace, environment.TierProduction,
		Request{Features: []string{FeatureISR, FeatureCloudflareEdge}}, nil); err != nil {
		t.Fatalf("bootstrapping behind the cloudflare edge: %v", err)
	}
	if front.bootstraps != 1 {
		t.Errorf("the front was bootstrapped %d times, want once: the feature and the front are the same edge here", front.bootstraps)
	}
}

func TestRemovingAnEdgeFeatureTearsItsEdgeDownBeforeSeveringWhatReachesIt(t *testing.T) {
	ctx := context.Background()
	stacks, ssmc, iamc := newFakeCFN(), newFakeSSM(), &fakeIAM{}
	store := preloadedStore()
	registry := newFakeEdges(&fakeEdge{kind: KindCloudflare})
	install := Request{Features: []string{FeatureISR, FeatureCloudflareEdge, FeatureCloudFrontEdge}}
	apis := apisAcross(stacks, ssmc, iamc, store, &fakeEdge{kind: KindCloudFront}, registry)

	if err := Run(ctx, apis, defaultNamespace, environment.TierProduction, install, nil); err != nil {
		t.Fatalf("installing the cloudflare edge behind the cloudfront front: %v", err)
	}

	names, err := edgeNamesFor(defaultNamespace, environment.TierProduction, KindCloudflare)
	if err != nil {
		t.Fatalf("edgeNamesFor: %v", err)
	}
	front := registry.at(KindCloudflare)
	var presentWhileTearing []string
	front.onTeardown = func() {
		for _, param := range names.edgeParams() {
			if _, present := ssmc.params[param]; present {
				presentWhileTearing = append(presentWhileTearing, param)
			}
		}
	}

	drop := Request{Features: []string{FeatureISR, FeatureCloudFrontEdge}, Remove: []string{FeatureCloudflareEdge}}
	if err := Run(ctx, apis, defaultNamespace, environment.TierProduction, drop, nil); err != nil {
		t.Fatalf("removing the cloudflare edge feature behind the cloudfront front: %v", err)
	}
	if front.teardowns != 1 {
		t.Fatalf("the cloudflare edge was torn down %d times, want once: dropping its feature takes its externals with it", front.teardowns)
	}
	if len(presentWhileTearing) == 0 {
		t.Error("the teardown ran with nothing left at SSM, so it had no credentials to reach the edge with")
	}
	for _, param := range names.edgeParams() {
		if _, present := ssmc.params[param]; present {
			t.Errorf("%s outlived the drop, so a later destroy still believes the edge is installed", param)
		}
	}
}

func TestEachEdgeKeepsItsOwnParameters(t *testing.T) {
	ctx := context.Background()
	ssmc := newFakeSSM()

	for _, kind := range []edge.Kind{KindCloudflare, KindCloudFront} {
		front := &fakeEdge{kind: kind, out: edge.BootstrapOutput{
			Offers: []edge.Offer{
				{Kind: edge.OfferReleasesStore, Values: map[string]string{
					edge.OfferKeyStoreEndpoint:            "https://ocel-releases-store-" + string(kind) + ".acct.workers.dev",
					edge.OfferKeyStoreScriptName:          "ocel-releases-store-" + string(kind),
					edge.OfferKeyStoreBootstrapCredential: "cred-" + string(kind),
				}},
			},
		}}
		apis := apisFronting(newFakeCFN(), ssmc, &fakeIAM{}, preloadedStore(), front)
		if err := Run(ctx, apis, defaultNamespace, environment.TierProduction, Request{}, nil); err != nil {
			t.Fatalf("bootstrapping behind the %s edge: %v", kind, err)
		}
	}

	for _, kind := range []edge.Kind{KindCloudflare, KindCloudFront} {
		prefix, err := defaultNamespace.EdgeParamPrefix(environment.TierProduction, kind)
		if err != nil {
			t.Fatalf("defaultNamespace.EdgeParamPrefix(%q): %v", kind, err)
		}
		names, err := edgeNamesFor(defaultNamespace, environment.TierProduction, kind)
		if err != nil {
			t.Fatalf("edgeNamesFor(defaultNamespace, %q): %v", kind, err)
		}
		for _, param := range []string{names.releasesStoreParam, names.purgeSeedParam} {
			if !strings.HasPrefix(param, prefix+"/") {
				t.Errorf("%s lies outside the %s edge's namespace %s", param, kind, prefix)
			}
			if _, present := ssmc.params[param]; !present {
				t.Errorf("the %s edge stored nothing at %s", kind, param)
			}
		}

		store, err := ReadReleasesStoreFor(ctx, ssmc, defaultNamespace, environment.TierProduction, kind)
		if err != nil {
			t.Fatalf("ReadReleasesStoreFor(%q): %v", kind, err)
		}
		if want := "ocel-releases-store-" + string(kind); store.ScriptName != want {
			t.Errorf("the %s edge reads back releases store %q, want %q", kind, store.ScriptName, want)
		}
	}

	seeds := map[edge.Kind]string{}
	for _, kind := range []edge.Kind{KindCloudflare, KindCloudFront} {
		param, err := PurgeSeedParamFor(defaultNamespace, environment.TierProduction, kind)
		if err != nil {
			t.Fatalf("PurgeSeedParamFor(%q): %v", kind, err)
		}
		seeds[kind] = ssmc.params[param]
	}
	if cloudflare := seeds[KindCloudflare]; cloudflare == "" || cloudflare == seeds[KindCloudFront] {
		t.Errorf("both edges sign their purges with %q; each edge has a seed of its own", cloudflare)
	}
}
