package cloudfront

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	cftypes "github.com/aws/aws-sdk-go-v2/service/cloudfront/types"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/edge/edgeconformance"
	"github.com/ocelhq/ocel/pkg/edge/gate"
	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/progress"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/pkg/router"
	"github.com/ocelhq/ocel/platform/aws/provider/cloudfrontpart"
)

var shopGate = gate.Gate{
	Key:           "gate-key",
	PasswordMAC:   "password-mac",
	BypassSecrets: []string{"bypass-one"},
	AllowOptions:  []string{"/api/"},
}

var tiers = []environment.Tier{environment.TierProduction, environment.TierPreview}

func gatedSpec(tier environment.Tier, held *gate.Gate) edge.StackSpec {
	spec := testSpec()
	if tier == environment.TierPreview {
		spec = previewStackSpec()
	}
	spec.Gate = held
	return spec
}

func bootstrappedFor(t *testing.T, w *world, tier environment.Tier) *cloudFront {
	t.Helper()
	if tier == environment.TierPreview {
		return previewBootstrapped(t, w)
	}
	return bootstrapped(t, w)
}

func priorFor(tier environment.Tier) edge.StackState {
	if tier == environment.TierPreview {
		return edge.StackState{GlobalPreview: previewBase}
	}
	return edge.StackState{}
}

func recordedGate(t *testing.T, w *world, tier environment.Tier) (gate.Gate, bool) {
	t.Helper()
	value, recorded := w.store.itemsOf(fakeRoutesARN(tier))["gate/"+conformanceSlug]
	if !recorded {
		return gate.Gate{}, false
	}
	var held gate.Gate
	if err := json.Unmarshal([]byte(value), &held); err != nil {
		t.Fatalf("the %s gate recorded for %s is %q, want the JSON the resolver reads: %v", tier, conformanceSlug, value, err)
	}
	return held, true
}

func reconcileGated(t *testing.T, e *cloudFront, tier environment.Tier, held *gate.Gate) edge.EdgeStack {
	t.Helper()
	stack, err := e.Reconcile(context.Background(), gatedSpec(tier, held), priorFor(tier))
	if err != nil {
		t.Fatalf("Reconcile(%s): %v", tier, err)
	}
	return stack
}

func eachTier(t *testing.T, test func(t *testing.T, tier environment.Tier)) {
	t.Helper()
	for _, tier := range tiers {
		t.Run(string(tier), func(t *testing.T) { test(t, tier) })
	}
}

func TestAReconcileRecordsTheGateInItsTiersRoutesStoreForTheResolverToRead(t *testing.T) {
	eachTier(t, func(t *testing.T, tier environment.Tier) {
		w := newWorld()
		reconcileGated(t, bootstrappedFor(t, w, tier), tier, &shopGate)

		got, recorded := recordedGate(t, w, tier)
		if !recorded {
			t.Fatalf("the %s routes store holds %v, want the gate under gate/%s", tier, w.store.itemsOf(fakeRoutesARN(tier)), conformanceSlug)
		}
		if !reflect.DeepEqual(got, shopGate) {
			t.Errorf("recorded gate = %+v, want %+v", got, shopGate)
		}
	})
}

func TestAProjectsProductionAndPreviewGatesLiveInSeparateStores(t *testing.T) {
	w := newWorld()
	production := bootstrapped(t, w)
	preview := previewBootstrapped(t, w)
	previewGate := gate.Gate{Key: "preview-key", PasswordMAC: "preview-mac", BypassSecrets: []string{}, AllowOptions: []string{}}

	reconcileGated(t, production, environment.TierProduction, &shopGate)
	reconcileGated(t, preview, environment.TierPreview, &previewGate)

	if got, _ := recordedGate(t, w, environment.TierProduction); !reflect.DeepEqual(got, shopGate) {
		t.Errorf("production gate = %+v, want %+v: the preview reconcile reached the store production's resolver reads", got, shopGate)
	}
	if got, _ := recordedGate(t, w, environment.TierPreview); !reflect.DeepEqual(got, previewGate) {
		t.Errorf("preview gate = %+v, want %+v", got, previewGate)
	}

	reconcileGated(t, production, environment.TierProduction, nil)

	if _, recorded := recordedGate(t, w, environment.TierProduction); recorded {
		t.Error("production is still gated after a production reconcile that carries no gate")
	}
	if got, recorded := recordedGate(t, w, environment.TierPreview); !recorded || !reflect.DeepEqual(got, previewGate) {
		t.Errorf("preview gate = %+v, %t after production's protection was turned off, want %+v kept", got, recorded, previewGate)
	}
}

func TestEachTiersDistributionRunsItsOwnResolverOnEveryViewerRequest(t *testing.T) {
	w := newWorld()
	reconciled(t, w)
	if _, err := previewBootstrapped(t, w).ReconcilePreviewWildcard(context.Background(), previewWildcardSpec()); err != nil {
		t.Fatalf("ReconcilePreviewWildcard: %v", err)
	}

	for name, tier := range map[string]environment.Tier{
		productionDistributionName():     environment.TierProduction,
		previewWildcardName(previewBase): environment.TierPreview,
	} {
		distribution := w.front.named(name)
		if distribution == nil {
			t.Fatalf("no distribution named %q", name)
		}
		config := distribution.config
		behaviours := []*cftypes.FunctionAssociations{config.DefaultCacheBehavior.FunctionAssociations}
		lambdas := []*cftypes.LambdaFunctionAssociations{config.DefaultCacheBehavior.LambdaFunctionAssociations}
		if config.CacheBehaviors != nil {
			for _, behaviour := range config.CacheBehaviors.Items {
				behaviours = append(behaviours, behaviour.FunctionAssociations)
				lambdas = append(lambdas, behaviour.LambdaFunctionAssociations)
			}
		}
		for i, associations := range behaviours {
			var viewerRequest []string
			if associations != nil {
				for _, association := range associations.Items {
					if association.EventType == cftypes.EventTypeViewerRequest {
						viewerRequest = append(viewerRequest, aws.ToString(association.FunctionARN))
					}
				}
			}
			if len(viewerRequest) != 1 || viewerRequest[0] != fakeResolverARN(tier) {
				t.Errorf("%s behaviour %d runs %v at viewer request, want only the %s resolver %s: a behaviour without it serves the cache to a visitor the gate never saw", name, i, viewerRequest, tier, fakeResolverARN(tier))
			}
			if lambdas[i] != nil && aws.ToInt32(lambdas[i].Quantity) != 0 {
				t.Errorf("%s behaviour %d runs Lambda@Edge %v, want none: only the resolver decides what a request reaches", name, i, lambdas[i].Items)
			}
		}
		if errs := config.CustomErrorResponses; errs != nil && aws.ToInt32(errs.Quantity) != 0 {
			t.Errorf("%s answers errors with %v, want no custom error pages: CloudFront fetches one past the viewer-request gate", name, errs.Items)
		}
	}
}

func TestAGateIsKeyedByTheProjectSlugUnderAPrefixNoHostnameCanHave(t *testing.T) {
	if got, want := gateKey("shop"), "gate/shop"; got != want {
		t.Errorf("gateKey = %q, want %q: the resolver reads this key", got, want)
	}
}

func TestAGateWithNoListsIsRecordedWithEmptyListsTheResolverCanIterate(t *testing.T) {
	eachTier(t, func(t *testing.T, tier environment.Tier) {
		w := newWorld()
		reconcileGated(t, bootstrappedFor(t, w, tier), tier, &gate.Gate{Key: "k", PasswordMAC: "m"})

		value := w.store.itemsOf(fakeRoutesARN(tier))["gate/"+conformanceSlug]
		if !strings.Contains(value, `"bypassSecrets":[]`) || !strings.Contains(value, `"allowOptions":[]`) {
			t.Errorf("recorded gate = %s, want empty lists rather than null", value)
		}
	})
}

func TestAReconcileWithTheGateAlreadyRecordedWritesNothing(t *testing.T) {
	eachTier(t, func(t *testing.T, tier environment.Tier) {
		w := newWorld()
		e := bootstrappedFor(t, w, tier)
		reconcileGated(t, e, tier, &shopGate)
		before := w.store.count("kvs.UpdateKeys")

		reconcileGated(t, e, tier, &shopGate)

		if got := w.store.count("kvs.UpdateKeys"); got != before {
			t.Errorf("UpdateKeys calls = %d after the second reconcile, want %d: the gate has not changed", got, before)
		}
	})
}

func TestAReconcileReplacesAGateWhoseKeysChanged(t *testing.T) {
	eachTier(t, func(t *testing.T, tier environment.Tier) {
		w := newWorld()
		e := bootstrappedFor(t, w, tier)
		reconcileGated(t, e, tier, &shopGate)
		rotated := shopGate
		rotated.PasswordMAC = "rotated-mac"

		reconcileGated(t, e, tier, &rotated)

		if got, _ := recordedGate(t, w, tier); got.PasswordMAC != "rotated-mac" {
			t.Errorf("recorded password MAC = %q, want the rotated one", got.PasswordMAC)
		}
	})
}

func TestAReconcileWithNoGateTakesDownTheGateARevokedRecordLeftBehind(t *testing.T) {
	eachTier(t, func(t *testing.T, tier environment.Tier) {
		w := newWorld()
		e := bootstrappedFor(t, w, tier)
		reconcileGated(t, e, tier, &shopGate)

		reconcileGated(t, e, tier, nil)

		if _, recorded := recordedGate(t, w, tier); recorded {
			t.Errorf("the gate is still recorded after a reconcile that carries none, so %s stays gated by keys nobody manages", tier)
		}
	})
}

func TestAReconcileWithNoGateAndNoneRecordedWritesNothing(t *testing.T) {
	eachTier(t, func(t *testing.T, tier environment.Tier) {
		w := newWorld()
		reconcileGated(t, bootstrappedFor(t, w, tier), tier, nil)

		if got := w.store.count("kvs.UpdateKeys"); got != 0 {
			t.Errorf("UpdateKeys calls = %d, want none: there is no gate to record or remove", got)
		}
	})
}

func TestAGateTooLargeForAKeyValueStoreValueIsRefusedNamingItsTiersRevoke(t *testing.T) {
	oversized := shopGate
	oversized.BypassSecrets = []string{strings.Repeat("s", 1024)}
	for tier, command := range map[environment.Tier]string{
		environment.TierProduction: "`ocel protection bypass revoke`",
		environment.TierPreview:    "`ocel protection bypass revoke --preview`",
	} {
		t.Run(string(tier), func(t *testing.T) {
			_, err := bootstrappedFor(t, newWorld(), tier).Reconcile(context.Background(), gatedSpec(tier, &oversized), priorFor(tier))

			var refused refusal.Refusal
			if !errors.As(err, &refused) || refused.Code != refusal.CodeInvalid {
				t.Fatalf("Reconcile err = %v, want an invalid refusal", err)
			}
			for _, want := range []string{"1024", string(tier), command} {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("err = %q, want it to name %q", err, want)
				}
			}
		})
	}
}

func TestDestroyingAStackTakesItsGateWithIt(t *testing.T) {
	eachTier(t, func(t *testing.T, tier environment.Tier) {
		w := newWorld()
		stack := reconcileGated(t, bootstrappedFor(t, w, tier), tier, &shopGate)

		if err := stack.Destroy(context.Background()); err != nil {
			t.Fatalf("Destroy: %v", err)
		}

		if _, recorded := recordedGate(t, w, tier); recorded {
			t.Errorf("the gate outlived the %s stack it gated", tier)
		}
	})
}

func TestADestroyThatLeavesARouteBehindKeepsTheGateUntilARetryRemovesIt(t *testing.T) {
	eachTier(t, func(t *testing.T, tier environment.Tier) {
		ctx := context.Background()
		w := newWorld()
		stack := reconcileGated(t, bootstrappedFor(t, w, tier), tier, &shopGate)
		if err := stack.BindDomain(ctx, edge.DomainBinding{Hostname: boundHost}); err != nil {
			t.Fatalf("BindDomain: %v", err)
		}
		w.store.updateErrs = []error{errors.New("the store is closed")}

		if err := stack.Destroy(ctx); err == nil {
			t.Fatal("Destroy = nil, want the failed route removal")
		}
		if _, recorded := recordedGate(t, w, tier); !recorded {
			t.Fatalf("the gate was taken down while the route to %s could still serve, so the resolver admits anyone", boundHost)
		}

		if err := stack.Destroy(ctx); err != nil {
			t.Fatalf("Destroy (retry): %v", err)
		}
		if _, recorded := recordedGate(t, w, tier); recorded {
			t.Errorf("the gate outlived the %s stack a retried destroy took down", tier)
		}
	})
}

func TestAPromotedPreviewRouteNamesTheSlugTheResolverReadsItsGateUnder(t *testing.T) {
	w := newWorld()
	_, stack := previewing(t, w)
	promotePreview(t, stack, previewPointer)

	published := previewRoutes(t, w)[previewHostname()]

	if published.Slug != conformanceSlug {
		t.Errorf("route slug = %q, want %q: the resolver reads gate/<slug>", published.Slug, conformanceSlug)
	}
}

func TestAPromotedProductionRouteNamesTheSlugTheResolverReadsItsGateUnder(t *testing.T) {
	w := newWorld()
	stack := reconcileGated(t, bootstrapped(t, w), environment.TierProduction, &shopGate)
	staged(t, stack, fakeEntryURL, fakeAssetPrefix)
	if err := openRouter(stack).MovePointer(context.Background(), router.PointerMove{Promotion: promotion()}, progress.Discard()); err != nil {
		t.Fatalf("Promote: %v", err)
	}
	bound(t, stack)

	if published := routeOn(t, w, stack, boundHost); published.Slug != conformanceSlug {
		t.Errorf("route slug = %q, want %q: the resolver reads gate/<slug>, and a route without one is never gated", published.Slug, conformanceSlug)
	}
}

func TestAGateWithEightGeneratedBypassSecretsFitsAKeyValueStoreValue(t *testing.T) {
	full := gate.Gate{Key: strings.Repeat("k", 64), PasswordMAC: strings.Repeat("m", 64)}
	for range 8 {
		full.BypassSecrets = append(full.BypassSecrets, strings.Repeat("s", 64))
	}

	encoded, err := encodeGate("shop", environment.TierProduction, &full)

	if err != nil {
		t.Fatalf("encodeGate = %v, want the largest gate a project can hold to fit in %d bytes", err, keyValueStoreValueMaxBytes)
	}
	if len(encoded) > keyValueStoreValueMaxBytes {
		t.Errorf("encoded gate = %d bytes, want at most %d", len(encoded), keyValueStoreValueMaxBytes)
	}
}

func TestPublishingAGateWritesItWithoutReconcilingTheStack(t *testing.T) {
	eachTier(t, func(t *testing.T, tier environment.Tier) {
		w := newWorld()
		e := bootstrappedFor(t, w, tier)
		stack := reconcileGated(t, e, tier, nil)
		publish := e.Hooks().PublishGate
		if publish == nil {
			t.Fatal("Hooks().PublishGate = nil, want the edge to publish a gate to the key value store the resolver reads")
		}
		reshapes := w.front.count("UpdateDistribution")

		if err := publish(context.Background(), edge.GatePublication{State: stack.State(), Gate: &shopGate}); err != nil {
			t.Fatalf("PublishGate = %v", err)
		}

		if got, recorded := recordedGate(t, w, tier); !recorded || !reflect.DeepEqual(got, shopGate) {
			t.Errorf("recorded gate = %+v, %t, want %+v", got, recorded, shopGate)
		}
		if got := w.front.count("UpdateDistribution"); got != reshapes {
			t.Errorf("UpdateDistribution calls = %d after publishing, want %d: a gate reaches the resolver through the store alone", got, reshapes)
		}
	})
}

func TestThePublishedGateAnswersTheConformanceSuite(t *testing.T) {
	eachTier(t, func(t *testing.T, tier environment.Tier) {
		w := newWorld()
		e := bootstrappedFor(t, w, tier)
		state := reconcileGated(t, e, tier, nil).State()

		edgeconformance.RunPublishedGate(t, edgeconformance.PublishedGate{
			Publish: func(ctx context.Context, held *gate.Gate) error {
				return e.Hooks().PublishGate(ctx, edge.GatePublication{State: state, Gate: held})
			},
			Read: func(context.Context) (*gate.Gate, error) {
				if held, recorded := recordedGate(t, w, tier); recorded {
					return &held, nil
				}
				return nil, nil
			},
		})
	})
}

func TestEveryPathThatAddsTheCloudFrontPartTellsTheUser(t *testing.T) {
	t.Parallel()
	want := "added the " + cloudfrontpart.Part + " part"
	heard := func(lines *[]string) func(string) { return func(line string) { *lines = append(*lines, line) } }
	refused := func(t *testing.T) func(string) {
		return func(line string) {
			t.Errorf("the user was warned %q, want adding a part said at info level: nothing is wrong", line)
		}
	}

	t.Run("a reconcile", func(t *testing.T) {
		var lines []string
		spec := testSpec()
		spec.Say = heard(&lines)
		spec.Warn = refused(t)
		if _, err := bootstrapped(t, newWorld()).Reconcile(context.Background(), spec, edge.StackState{}); err != nil {
			t.Fatalf("Reconcile: %v", err)
		}
		if !slices.Contains(lines, want) {
			t.Errorf("the user heard %v, want %q", lines, want)
		}
	})
	t.Run("a gate publication", func(t *testing.T) {
		w := newWorld()
		e := bootstrappedFor(t, w, environment.TierProduction)
		state := reconcileGated(t, e, environment.TierProduction, nil).State()
		var lines []string
		if err := e.Hooks().PublishGate(context.Background(), edge.GatePublication{State: state, Gate: &shopGate, Say: heard(&lines)}); err != nil {
			t.Fatalf("PublishGate: %v", err)
		}
		if !slices.Contains(lines, want) {
			t.Errorf("the user heard %v, want %q", lines, want)
		}
	})
	t.Run("a preview wildcard", func(t *testing.T) {
		var lines []string
		spec := previewWildcardSpec()
		spec.Say = heard(&lines)
		spec.Warn = refused(t)
		if _, err := previewBootstrapped(t, newWorld()).ReconcilePreviewWildcard(context.Background(), spec); err != nil {
			t.Fatalf("ReconcilePreviewWildcard: %v", err)
		}
		if !slices.Contains(lines, want) {
			t.Errorf("the user heard %v, want %q", lines, want)
		}
	})
}

func TestPublishingAGateAddsTheCloudFrontPartTheGateIsReadBy(t *testing.T) {
	eachTier(t, func(t *testing.T, tier environment.Tier) {
		w := newWorld()
		e := bootstrappedFor(t, w, tier)
		state := reconcileGated(t, e, tier, nil).State()
		w.parts.ensured = nil

		if err := e.Hooks().PublishGate(context.Background(), edge.GatePublication{State: state, Gate: &shopGate}); err != nil {
			t.Fatalf("PublishGate: %v", err)
		}

		if len(w.parts.ensured) != 1 || !strings.HasPrefix(w.parts.ensured[0], cloudfrontpart.Part+" for ") {
			t.Errorf("parts ensured = %v, want the %s part ensured before the gate is written into its routes store", w.parts.ensured, cloudfrontpart.Part)
		}
	})
}
