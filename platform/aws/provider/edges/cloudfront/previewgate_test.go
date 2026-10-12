package cloudfront

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/edge/edgeconformance"
	"github.com/ocelhq/ocel/pkg/edge/previewgate"
	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/refusal"
)

var shopGate = previewgate.Gate{
	Key:           "gate-key",
	PasswordMAC:   "password-mac",
	BypassSecrets: []string{"bypass-one"},
	AllowOptions:  []string{"/api/"},
}

func gatedPreviewSpec(gate *previewgate.Gate) edge.StackSpec {
	spec := previewStackSpec()
	spec.PreviewGate = gate
	return spec
}

func recordedGate(t *testing.T, w *world) (previewgate.Gate, bool) {
	t.Helper()
	value, recorded := w.store.itemsOf(fakeRoutesARN(environment.TierPreview))["gate/"+conformanceSlug]
	if !recorded {
		return previewgate.Gate{}, false
	}
	var gate previewgate.Gate
	if err := json.Unmarshal([]byte(value), &gate); err != nil {
		t.Fatalf("the gate recorded for %s is %q, want the JSON the resolver reads: %v", conformanceSlug, value, err)
	}
	return gate, true
}

func reconcilePreview(t *testing.T, e *cloudFront, spec edge.StackSpec) edge.EdgeStack {
	t.Helper()
	stack, err := e.Reconcile(context.Background(), spec, edge.StackState{GlobalPreview: previewBase})
	if err != nil {
		t.Fatalf("Reconcile(preview): %v", err)
	}
	return stack
}

func TestAPreviewReconcileRecordsTheGateInTheRoutesStoreForTheResolverToRead(t *testing.T) {
	w := newWorld()
	reconcilePreview(t, previewBootstrapped(t, w), gatedPreviewSpec(&shopGate))

	got, recorded := recordedGate(t, w)
	if !recorded {
		t.Fatalf("the preview routes store holds %v, want the gate under gate/%s", w.store.itemsOf(fakeRoutesARN(environment.TierPreview)), conformanceSlug)
	}
	if !reflect.DeepEqual(got, shopGate) {
		t.Errorf("recorded gate = %+v, want %+v", got, shopGate)
	}
}

func TestAGateIsKeyedByTheProjectSlugUnderAPrefixNoHostnameCanHave(t *testing.T) {
	if got, want := previewGateKey("shop"), "gate/shop"; got != want {
		t.Errorf("previewGateKey = %q, want %q: the preview resolver reads this key", got, want)
	}
}

func TestAGateWithNoListsIsRecordedWithEmptyListsTheResolverCanIterate(t *testing.T) {
	w := newWorld()
	reconcilePreview(t, previewBootstrapped(t, w), gatedPreviewSpec(&previewgate.Gate{Key: "k", PasswordMAC: "m"}))

	value := w.store.itemsOf(fakeRoutesARN(environment.TierPreview))["gate/"+conformanceSlug]
	if !strings.Contains(value, `"bypassSecrets":[]`) || !strings.Contains(value, `"allowOptions":[]`) {
		t.Errorf("recorded gate = %s, want empty lists rather than null", value)
	}
}

func TestAPreviewReconcileWithTheGateAlreadyRecordedWritesNothing(t *testing.T) {
	w := newWorld()
	e := previewBootstrapped(t, w)
	reconcilePreview(t, e, gatedPreviewSpec(&shopGate))
	before := w.store.count("kvs.UpdateKeys")

	reconcilePreview(t, e, gatedPreviewSpec(&shopGate))

	if got := w.store.count("kvs.UpdateKeys"); got != before {
		t.Errorf("UpdateKeys calls = %d after the second reconcile, want %d: the gate has not changed", got, before)
	}
}

func TestAPreviewReconcileReplacesAGateWhoseKeysChanged(t *testing.T) {
	w := newWorld()
	e := previewBootstrapped(t, w)
	reconcilePreview(t, e, gatedPreviewSpec(&shopGate))
	rotated := shopGate
	rotated.PasswordMAC = "rotated-mac"

	reconcilePreview(t, e, gatedPreviewSpec(&rotated))

	if got, _ := recordedGate(t, w); got.PasswordMAC != "rotated-mac" {
		t.Errorf("recorded password MAC = %q, want the rotated one", got.PasswordMAC)
	}
}

func TestAPreviewReconcileWithNoGateTakesDownTheGateARevokedRecordLeftBehind(t *testing.T) {
	w := newWorld()
	e := previewBootstrapped(t, w)
	reconcilePreview(t, e, gatedPreviewSpec(&shopGate))

	reconcilePreview(t, e, gatedPreviewSpec(nil))

	if _, recorded := recordedGate(t, w); recorded {
		t.Error("the gate is still recorded after a reconcile that carries none, so the preview stays gated by keys nobody manages")
	}
}

func TestAPreviewReconcileWithNoGateAndNoneRecordedWritesNothing(t *testing.T) {
	w := newWorld()
	reconcilePreview(t, previewBootstrapped(t, w), gatedPreviewSpec(nil))

	if got := w.store.count("kvs.UpdateKeys"); got != 0 {
		t.Errorf("UpdateKeys calls = %d, want none: there is no gate to record or remove", got)
	}
}

func TestAProductionReconcileNeverTouchesTheGateRecords(t *testing.T) {
	w := newWorld()
	reconciled(t, w)

	if got := w.store.count("kvs.GetKey"); got != 0 {
		t.Errorf("GetKey calls = %d, want none: production fronts are never gated", got)
	}
}

func TestAGateTooLargeForAKeyValueStoreValueIsRefusedByName(t *testing.T) {
	oversized := shopGate
	oversized.BypassSecrets = []string{strings.Repeat("s", 1024)}

	_, err := previewBootstrapped(t, newWorld()).Reconcile(context.Background(), gatedPreviewSpec(&oversized), edge.StackState{GlobalPreview: previewBase})

	var refused refusal.Refusal
	if !errors.As(err, &refused) || refused.Code != refusal.CodeInvalid {
		t.Fatalf("Reconcile err = %v, want an invalid refusal", err)
	}
	for _, want := range []string{"1024", "bypass"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("err = %q, want it to name %q", err, want)
		}
	}
}

func TestDestroyingAPreviewStackTakesItsGateWithIt(t *testing.T) {
	w := newWorld()
	e := previewBootstrapped(t, w)
	stack := reconcilePreview(t, e, gatedPreviewSpec(&shopGate))

	if err := stack.Destroy(context.Background()); err != nil {
		t.Fatalf("Destroy: %v", err)
	}

	if _, recorded := recordedGate(t, w); recorded {
		t.Error("the gate outlived the preview stack it gated")
	}
}

func TestADestroyThatLeavesARouteBehindKeepsTheGateUntilARetryRemovesIt(t *testing.T) {
	ctx := context.Background()
	w := newWorld()
	stack := reconcilePreview(t, previewBootstrapped(t, w), gatedPreviewSpec(&shopGate))
	if err := stack.BindDomain(ctx, edge.DomainBinding{Hostname: boundHost}); err != nil {
		t.Fatalf("BindDomain: %v", err)
	}
	w.store.updateErrs = []error{errors.New("the store is closed")}

	if err := stack.Destroy(ctx); err == nil {
		t.Fatal("Destroy = nil, want the failed route removal")
	}
	if _, recorded := recordedGate(t, w); !recorded {
		t.Fatalf("the gate was taken down while the route to %s could still serve, so the resolver admits anyone", boundHost)
	}

	if err := stack.Destroy(ctx); err != nil {
		t.Fatalf("Destroy (retry): %v", err)
	}
	if _, recorded := recordedGate(t, w); recorded {
		t.Error("the gate outlived the preview stack a retried destroy took down")
	}
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

func TestAGateWithEightGeneratedBypassSecretsFitsAKeyValueStoreValue(t *testing.T) {
	full := previewgate.Gate{Key: strings.Repeat("k", 64), PasswordMAC: strings.Repeat("m", 64)}
	for range 8 {
		full.BypassSecrets = append(full.BypassSecrets, strings.Repeat("s", 64))
	}

	encoded, err := encodePreviewGate("shop", &full)

	if err != nil {
		t.Fatalf("encodePreviewGate = %v, want the largest gate a project can hold to fit in %d bytes", err, keyValueStoreValueMaxBytes)
	}
	if len(encoded) > keyValueStoreValueMaxBytes {
		t.Errorf("encoded gate = %d bytes, want at most %d", len(encoded), keyValueStoreValueMaxBytes)
	}
}

func TestPublishingAGateWritesItWithoutReconcilingTheStack(t *testing.T) {
	w := newWorld()
	e := previewBootstrapped(t, w)
	stack := reconcilePreview(t, e, gatedPreviewSpec(nil))
	publish := e.Hooks().PublishPreviewGate
	if publish == nil {
		t.Fatal("Hooks().PublishPreviewGate = nil, want the edge to publish a gate to the key value store the resolver reads")
	}

	if err := publish(context.Background(), edge.PreviewGatePublication{State: stack.State(), Gate: &shopGate}); err != nil {
		t.Fatalf("PublishPreviewGate = %v", err)
	}

	if got, recorded := recordedGate(t, w); !recorded || !reflect.DeepEqual(got, shopGate) {
		t.Errorf("recorded gate = %+v, %t, want %+v", got, recorded, shopGate)
	}
}

func TestThePublishedGateAnswersTheConformanceSuite(t *testing.T) {
	w := newWorld()
	e := previewBootstrapped(t, w)
	state := reconcilePreview(t, e, gatedPreviewSpec(nil)).State()

	edgeconformance.RunPublishedPreviewGate(t, edgeconformance.PublishedPreviewGate{
		Publish: func(ctx context.Context, gate *previewgate.Gate) error {
			return e.Hooks().PublishPreviewGate(ctx, edge.PreviewGatePublication{State: state, Gate: gate})
		},
		Read: func(context.Context) (*previewgate.Gate, error) {
			if gate, recorded := recordedGate(t, w); recorded {
				return &gate, nil
			}
			return nil, nil
		},
	})
}
