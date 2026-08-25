package bootstrap

import (
	"context"
	"slices"
	"strings"
	"testing"

	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

var everyEdgeKind = []edge.Kind{KindCloudflare, KindCloudFront, KindAPIGateway}

func TestTheCoreIsTheSameWhicheverEdgeFrontsIt(t *testing.T) {
	for _, class := range []string{ClassProduction, ClassPreview} {
		t.Run(class, func(t *testing.T) {
			want := TemplateDigest(coreStackTemplate(class))
			core, err := StackNameFor(class)
			if err != nil {
				t.Fatalf("StackNameFor: %v", err)
			}
			for _, kind := range everyEdgeKind {
				cfn, ssmc, iamc := newFakeCFN(), newFakeSSM(), &fakeIAM{}
				apis := apisFronting(cfn, ssmc, iamc, preloadedStore(), &fakeEdge{kind: kind})
				if err := Run(context.Background(), apis, class, Request{}, nil, nil); err != nil {
					t.Fatalf("bootstrapping behind the %s edge: %v", kind, err)
				}
				if got := TemplateDigest(cfn.template(core)); got != want {
					t.Errorf("the %s edge wrote a core whose digest is %q, want %q: the core is what every edge stands beside, not part of one", kind, got, want)
				}
				if got := cfn.stampOf(core).Digest; got != want {
					t.Errorf("the %s edge stamped the core %q, want %q", kind, got, want)
				}
			}
		})
	}
}

func TestReadingABootstrapSeesEveryEdgeThatStands(t *testing.T) {
	stamp := Stamp{Schema: RequiredSchema}
	api := stubDescriber{
		StackName: outputs(map[string]string{
			outputInfraClass:  ClassProduction,
			outputAssetBucket: "assets-1",
		}).stamped(stamp),
		FeatureStackName(FeatureISR, ClassProduction): outputs(map[string]string{
			outputRevalidateQueueURL: "https://sqs.test/revalidate",
		}).stamped(stamp),
		FeatureStackName(FeatureCloudflareEdge, ClassProduction): outputs(nil).stamped(stamp),
		FeatureStackName(FeatureCloudFrontEdge, ClassProduction): outputs(map[string]string{
			OutputEdgeResolverARN: "arn:aws:cloudfront::111122223333:function/ocel-resolver",
			OutputEdgeCachePolicy: "cache-1",
		}).stamped(stamp),
	}

	got, err := CheckDeployed(context.Background(), api)
	if err != nil {
		t.Fatalf("CheckDeployed: %v", err)
	}
	want := []string{FeatureISR, FeatureCloudflareEdge, FeatureCloudFrontEdge}
	if !slices.Equal(got.Features.Names(), want) {
		t.Errorf("Features = %v, want %v: an account may hold a stack for more than one edge at a time", got.Features.Names(), want)
	}
	for key, value := range map[string]string{
		outputAssetBucket:        "assets-1",
		outputRevalidateQueueURL: "https://sqs.test/revalidate",
		OutputEdgeResolverARN:    "arn:aws:cloudfront::111122223333:function/ocel-resolver",
		OutputEdgeCachePolicy:    "cache-1",
	} {
		if got.Outputs[key] != value {
			t.Errorf("Outputs[%s] = %q, want %q: a deploy reads the core and every standing feature stack as one", key, got.Outputs[key], value)
		}
	}
}

func TestBootstrappingOneEdgeLeavesAnotherEdgesStackAlone(t *testing.T) {
	ctx := context.Background()
	cfn, ssmc, iamc := newFakeCFN(), newFakeSSM(), &fakeIAM{}
	store := preloadedStore()

	cloudflare := apisFronting(cfn, ssmc, iamc, store, &fakeEdge{kind: KindCloudflare})
	if err := Run(ctx, cloudflare, ClassProduction, Request{Features: []string{FeatureISR, FeatureCloudflareEdge}}, nil, nil); err != nil {
		t.Fatalf("bootstrapping behind the cloudflare edge: %v", err)
	}

	standing := FeatureStackName(FeatureCloudflareEdge, ClassProduction)
	settled := len(cfn.events)
	restamps := cfn.restamps

	cloudfront := apisFronting(cfn, ssmc, iamc, store, &fakeEdge{kind: KindCloudFront})
	if err := Run(ctx, cloudfront, ClassProduction, Request{Features: []string{FeatureCloudFrontEdge}}, nil, nil); err != nil {
		t.Fatalf("bootstrapping behind the cloudfront edge: %v", err)
	}

	written := FeatureStackName(FeatureCloudFrontEdge, ClassProduction)
	if !slices.Contains(cfn.stacks(), written) {
		t.Fatalf("stacks = %v, want the cloudfront edge among them", cfn.stacks())
	}
	for _, event := range cfn.events[settled:] {
		if strings.HasSuffix(event, standing) {
			t.Errorf("the run %s the edge it was not asked for; switching the front an account is bootstrapped behind takes nothing away from the one it stood behind", event)
		}
	}
	if cfn.restamps != restamps {
		t.Errorf("the run restamped %d stacks, want none of them the edge it was not asked for", cfn.restamps-restamps)
	}
	if !slices.Contains(cfn.stacks(), standing) {
		t.Errorf("stacks = %v, want the cloudflare edge still standing", cfn.stacks())
	}
}

func TestAnEdgeFeatureStandsItsOwnEdgeUpWhateverFrontsTheRun(t *testing.T) {
	ctx := context.Background()
	cfn, ssmc, iamc := newFakeCFN(), newFakeSSM(), &fakeIAM{}
	registry := newFakeEdges(&fakeEdge{kind: KindCloudflare, out: edge.BootstrapOutput{
		Values: map[string]string{"namespaceId": "ns-cloudflare"},
	}})
	apis := apisAcross(cfn, ssmc, iamc, preloadedStore(), &fakeEdge{kind: KindCloudFront}, registry)

	if err := Run(ctx, apis, ClassProduction,
		Request{Features: []string{FeatureISR, FeatureCloudflareEdge, FeatureCloudFrontEdge}}, nil, nil); err != nil {
		t.Fatalf("bootstrapping the cloudflare edge feature behind the cloudfront front: %v", err)
	}

	front := registry.at(KindCloudflare)
	if front.bootstraps != 1 {
		t.Fatalf("the cloudflare edge was bootstrapped %d times, want once: the feature owns the edge, not the front this run picked", front.bootstraps)
	}
	values, err := ReadEdgeValues(ctx, ssmc, ClassProduction, KindCloudflare)
	if err != nil {
		t.Fatalf("ReadEdgeValues: %v", err)
	}
	if values["namespaceId"] != "ns-cloudflare" {
		t.Errorf("the cloudflare edge's values read back %v, want what it handed over: a deploy through it reads zero otherwise", values)
	}
}

func TestTheFrontThisRunPicksIsBootstrappedOnce(t *testing.T) {
	cfn, ssmc, iamc := newFakeCFN(), newFakeSSM(), &fakeIAM{}
	front := &fakeEdge{kind: KindCloudflare}
	apis := apisFronting(cfn, ssmc, iamc, preloadedStore(), front)

	if err := Run(context.Background(), apis, ClassProduction,
		Request{Features: []string{FeatureISR, FeatureCloudflareEdge}}, nil, nil); err != nil {
		t.Fatalf("bootstrapping behind the cloudflare edge: %v", err)
	}
	if front.bootstraps != 1 {
		t.Errorf("the front was bootstrapped %d times, want once: the feature and the front are the same edge here", front.bootstraps)
	}
}

func TestRemovingAnEdgeFeatureTearsItsEdgeDownBeforeSeveringWhatReachesIt(t *testing.T) {
	ctx := context.Background()
	cfn, ssmc, iamc := newFakeCFN(), newFakeSSM(), &fakeIAM{}
	store := preloadedStore()
	registry := newFakeEdges(&fakeEdge{kind: KindCloudflare})
	stood := Request{Features: []string{FeatureISR, FeatureCloudflareEdge, FeatureCloudFrontEdge}}
	apis := apisAcross(cfn, ssmc, iamc, store, &fakeEdge{kind: KindCloudFront}, registry)

	if err := Run(ctx, apis, ClassProduction, stood, nil, nil); err != nil {
		t.Fatalf("standing the cloudflare edge up behind the cloudfront front: %v", err)
	}

	names, err := edgeNamesFor(ClassProduction, KindCloudflare)
	if err != nil {
		t.Fatalf("edgeNamesFor: %v", err)
	}
	front := registry.at(KindCloudflare)
	var heldWhileTearing []string
	front.onTeardown = func() {
		for _, param := range names.edgeParams() {
			if _, held := ssmc.params[param]; held {
				heldWhileTearing = append(heldWhileTearing, param)
			}
		}
	}

	drop := Request{Features: []string{FeatureISR, FeatureCloudFrontEdge}, Remove: []string{FeatureCloudflareEdge}}
	if err := Run(ctx, apis, ClassProduction, drop, nil, nil); err != nil {
		t.Fatalf("removing the cloudflare edge feature behind the cloudfront front: %v", err)
	}
	if front.teardowns != 1 {
		t.Fatalf("the cloudflare edge was torn down %d times, want once: dropping its feature takes its externals with it", front.teardowns)
	}
	if len(heldWhileTearing) == 0 {
		t.Error("the teardown ran with nothing left at SSM, so it had no credentials to reach the edge with")
	}
	for _, param := range names.edgeParams() {
		if _, held := ssmc.params[param]; held {
			t.Errorf("%s outlived the drop, so a later destroy still believes the edge stands", param)
		}
	}
}

func TestEachEdgeKeepsItsOwnParameters(t *testing.T) {
	ctx := context.Background()
	ssmc := newFakeSSM()

	for _, kind := range []edge.Kind{KindCloudflare, KindCloudFront} {
		front := &fakeEdge{kind: kind, out: edge.BootstrapOutput{
			Values: map[string]string{"namespaceId": "ns-" + string(kind)},
			Offers: []edge.Offer{
				{Kind: edge.OfferCacheStore, Values: map[string]string{
					edge.OfferKeyBucket:          "cache-" + string(kind),
					edge.OfferKeyAccessKeyID:     "tok-" + string(kind),
					edge.OfferKeySecretAccessKey: "secret-" + string(kind),
				}},
				{Kind: edge.OfferISRWriter, Values: offeredISRWriter("-"+string(kind), "cred-"+string(kind))},
			},
		}}
		apis := apisFronting(newFakeCFN(), ssmc, &fakeIAM{}, preloadedStore(), front)
		if err := Run(ctx, apis, ClassProduction, Request{}, nil, nil); err != nil {
			t.Fatalf("bootstrapping behind the %s edge: %v", kind, err)
		}
	}

	for _, kind := range []edge.Kind{KindCloudflare, KindCloudFront} {
		prefix, err := EdgeParamPrefix(ClassProduction, kind)
		if err != nil {
			t.Fatalf("EdgeParamPrefix(%q): %v", kind, err)
		}
		names, err := edgeNamesFor(ClassProduction, kind)
		if err != nil {
			t.Fatalf("edgeNamesFor(%q): %v", kind, err)
		}
		for _, param := range []string{names.valuesParam, names.cacheStoreParam, names.isrWriterParam, names.isrWriterSeedParam} {
			if !strings.HasPrefix(param, prefix+"/") {
				t.Errorf("%s stands outside the %s edge's namespace %s", param, kind, prefix)
			}
			if _, held := ssmc.params[param]; !held {
				t.Errorf("the %s edge stored nothing at %s", kind, param)
			}
		}

		values, err := ReadEdgeValues(ctx, ssmc, ClassProduction, kind)
		if err != nil {
			t.Fatalf("ReadEdgeValues(%q): %v", kind, err)
		}
		if want := "ns-" + string(kind); values["namespaceId"] != want {
			t.Errorf("the %s edge reads back namespace %q, want %q: a second edge bootstrapped in this account overwrote it", kind, values["namespaceId"], want)
		}

		store, err := ReadCacheStore(ctx, ssmc, ClassProduction, kind)
		if err != nil {
			t.Fatalf("ReadCacheStore(%q): %v", kind, err)
		}
		if want := "cache-" + string(kind); store.Bucket != want {
			t.Errorf("the %s edge reads back cache store %q, want %q", kind, store.Bucket, want)
		}

		writer, err := ReadISRWriterFor(ctx, ssmc, ClassProduction, kind)
		if err != nil {
			t.Fatalf("ReadISRWriterFor(%q): %v", kind, err)
		}
		if want := "ocel-isr-writer-" + string(kind); writer.ScriptName != want {
			t.Errorf("the %s edge reads back ISR writer %q, want %q", kind, writer.ScriptName, want)
		}
	}

	cloudflare, err := ReadISRWriterSeedFor(ctx, ssmc, ClassProduction, KindCloudflare)
	if err != nil {
		t.Fatalf("ReadISRWriterSeedFor(cloudflare): %v", err)
	}
	cloudfront, err := ReadISRWriterSeedFor(ctx, ssmc, ClassProduction, KindCloudFront)
	if err != nil {
		t.Fatalf("ReadISRWriterSeedFor(cloudfront): %v", err)
	}
	if cloudflare == "" || cloudflare == cloudfront {
		t.Errorf("both edges authenticate their tag writes with %q; each edge's writer has a seed of its own", cloudflare)
	}
}
