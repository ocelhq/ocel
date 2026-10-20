package account

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/platform/aws/provider/cfn"
)

const (
	kindCloudFront edge.Kind = "cloudfront"
	kindAPIGateway edge.Kind = "api-gateway"
)

var everyEdgeKind = []edge.Kind{KindCloudflare, kindCloudFront, kindAPIGateway}

func TestTheCoreIsTheSameWhicheverEdgeFrontsIt(t *testing.T) {
	for _, tier := range []environment.Tier{environment.TierProduction, environment.TierPreview} {
		t.Run(string(tier), func(t *testing.T) {
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
				want := cfn.TemplateDigest(writtenCoreBody(stacks, defaultNamespace, tier, ""))
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

func TestReadingABootstrapSeesTheEdgePartInstalledBesideTheCore(t *testing.T) {
	stamp := Stamp{}
	api := stubStacksAPI{
		coreStackName: coreOutputs(map[string]string{
			outputInfraTier: string(environment.TierProduction),
		}).stamped(stamp),
		defaultNamespace.PartStackName(CloudflareEdgePart, environment.TierProduction): outputs(nil).stamped(stamp),
	}

	got, err := CheckDeployed(context.Background(), api, defaultNamespace)
	if err != nil {
		t.Fatalf("CheckDeployed: %v", err)
	}
	if want := []string{CloudflareEdgePart}; !slices.Equal(got.Parts.Names(), want) {
		t.Errorf("Parts = %v, want %v", got.Parts.Names(), want)
	}
	if got.Outputs[outputEdgeBucket] != fixtureBucketName(environment.TierProduction, naming.StorageEdge) {
		t.Errorf("Outputs[%s] = %q, want the core's edge bucket", outputEdgeBucket, got.Outputs[outputEdgeBucket])
	}
}

func TestBootstrappingOneEdgeLeavesAnotherEdgesStackAlone(t *testing.T) {
	ctx := context.Background()
	stacks, ssmc, iamc := newFakeCFN(), newFakeSSM(), &fakeIAM{}
	store := preloadedStore()

	cloudflare := apisFronting(stacks, ssmc, iamc, store, &fakeEdge{kind: KindCloudflare})
	if err := Run(ctx, cloudflare, defaultNamespace, environment.TierProduction, Request{Parts: []string{CloudflareEdgePart}}, nil); err != nil {
		t.Fatalf("bootstrapping behind the cloudflare edge: %v", err)
	}

	existing := defaultNamespace.PartStackName(CloudflareEdgePart, environment.TierProduction)
	eventsBefore := len(stacks.events)
	restamps := stacks.restamps

	cloudfront := apisFronting(stacks, ssmc, iamc, store, &fakeEdge{kind: kindCloudFront})
	if err := Run(ctx, cloudfront, defaultNamespace, environment.TierProduction, Request{}, nil); err != nil {
		t.Fatalf("bootstrapping behind the cloudfront edge: %v", err)
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

func TestAnEdgePartInstallsItsOwnEdgeWhateverFrontsTheRun(t *testing.T) {
	ctx := context.Background()
	stacks, ssmc, iamc := newFakeCFN(), newFakeSSM(), &fakeIAM{}
	registry := newFakeEdges(&fakeEdge{kind: KindCloudflare})
	apis := apisAcross(stacks, ssmc, iamc, preloadedStore(), &fakeEdge{kind: kindCloudFront}, registry)

	if err := Run(ctx, apis, defaultNamespace, environment.TierProduction,
		Request{Parts: []string{CloudflareEdgePart}}, nil); err != nil {
		t.Fatalf("bootstrapping the cloudflare edge part behind the cloudfront front: %v", err)
	}

	front := registry.at(KindCloudflare)
	if front.bootstraps != 1 {
		t.Fatalf("the cloudflare edge was bootstrapped %d times, want once: the part owns the edge, not the front this run picked", front.bootstraps)
	}
}

func TestTheFrontThisRunPicksIsBootstrappedOnce(t *testing.T) {
	stacks, ssmc, iamc := newFakeCFN(), newFakeSSM(), &fakeIAM{}
	front := &fakeEdge{kind: KindCloudflare}
	apis := apisFronting(stacks, ssmc, iamc, preloadedStore(), front)

	if err := Run(context.Background(), apis, defaultNamespace, environment.TierProduction,
		Request{Parts: []string{CloudflareEdgePart}}, nil); err != nil {
		t.Fatalf("bootstrapping behind the cloudflare edge: %v", err)
	}
	if front.bootstraps != 1 {
		t.Errorf("the front was bootstrapped %d times, want once: the part and the front are the same edge here", front.bootstraps)
	}
}

func TestRemovingAnEdgePartTearsItsEdgeDownBeforeSeveringWhatReachesIt(t *testing.T) {
	ctx := context.Background()
	stacks, ssmc, iamc := newFakeCFN(), newFakeSSM(), &fakeIAM{}
	store := preloadedStore()
	registry := newFakeEdges(&fakeEdge{kind: KindCloudflare})
	install := Request{Parts: []string{CloudflareEdgePart}}
	apis := apisAcross(stacks, ssmc, iamc, store, &fakeEdge{kind: kindCloudFront}, registry)

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

	drop := Request{Remove: []string{CloudflareEdgePart}}
	if err := Run(ctx, apis, defaultNamespace, environment.TierProduction, drop, nil); err != nil {
		t.Fatalf("removing the cloudflare edge part behind the cloudfront front: %v", err)
	}
	if front.teardowns != 1 {
		t.Fatalf("the cloudflare edge was torn down %d times, want once: dropping its part takes its externals with it", front.teardowns)
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

	for _, kind := range []edge.Kind{KindCloudflare, kindCloudFront} {
		front := &fakeEdge{kind: kind, out: edge.BootstrapOutput{
			ReleasesStore: &edge.ReleasesStore{
				Name:                "ocel-releases-store-" + string(kind),
				Endpoint:            "https://ocel-releases-store-" + string(kind) + ".acct.workers.dev",
				BootstrapCredential: "cred-" + string(kind),
			},
		}}
		apis := apisFronting(newFakeCFN(), ssmc, &fakeIAM{}, preloadedStore(), front)
		if err := Run(ctx, apis, defaultNamespace, environment.TierProduction, Request{}, nil); err != nil {
			t.Fatalf("bootstrapping behind the %s edge: %v", kind, err)
		}
	}

	for _, kind := range []edge.Kind{KindCloudflare, kindCloudFront} {
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
	for _, kind := range []edge.Kind{KindCloudflare, kindCloudFront} {
		param, err := PurgeSeedParamFor(defaultNamespace, environment.TierProduction, kind)
		if err != nil {
			t.Fatalf("PurgeSeedParamFor(%q): %v", kind, err)
		}
		seeds[kind] = ssmc.params[param]
	}
	if cloudflare := seeds[KindCloudflare]; cloudflare == "" || cloudflare == seeds[kindCloudFront] {
		t.Errorf("both edges sign their purges with %q; each edge has a seed of its own", cloudflare)
	}
}
