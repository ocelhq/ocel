package gcp

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/keyvalue"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/fake"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/platform/gcp/provider/edges/alb"
)

type frontRegistry struct {
	opened []edge.Kind
	front  *countingFront
}

func (r *frontRegistry) Open(kind edge.Kind, _ provider.Options) (edge.Edge, error) {
	r.opened = append(r.opened, kind)
	return r.front, nil
}

type countingFront struct {
	*alb.Edge
	raised    []environment.Tier
	torn      []environment.Tier
	installed bool
	bound     []string
	refusal   error
	silent    bool
	parts     []edge.BootstrapPart
	out       edge.BootstrapOutput
	describe  error
}

func (f *countingFront) Hooks() edge.Hooks {
	if f.silent {
		return edge.Hooks{}
	}
	return edge.Hooks{
		CheckBootstrapInstalled: func(context.Context, environment.Tier) (bool, error) { return f.installed, nil },
		ListBoundHostnames:      func(context.Context, environment.Tier) ([]string, error) { return f.bound, nil },
		DescribeBootstrap: func(context.Context, environment.Tier) ([]edge.BootstrapPart, error) {
			return f.parts, f.describe
		},
	}
}

func (f *countingFront) Bootstrap(_ context.Context, tier environment.Tier) (edge.BootstrapOutput, error) {
	f.raised = append(f.raised, tier)
	return f.out, nil
}

func (f *countingFront) Teardown(_ context.Context, tier environment.Tier) error {
	if f.refusal != nil {
		return f.refusal
	}
	f.torn = append(f.torn, tier)
	return nil
}

func fronting(t *testing.T) (bootstrap, *frontRegistry) {
	t.Helper()
	b, registry, _ := frontingWithSecrets(t)
	return b, registry
}

func frontingWithSecrets(t *testing.T) (bootstrap, *frontRegistry, *offersHarness) {
	t.Helper()
	registry := &frontRegistry{front: &countingFront{}}
	h := newOffersHarness(t)
	return bootstrap{fronts: registry, clients: h.clients, records: h.records}, registry, h
}

func surveyed(features ...string) survey {
	return survey{
		Names:   Names{namespace: "ocel", project: "acme-prod"},
		Tier:    environment.TierProduction,
		Project: "acme-prod",
		Present: true,
		Stamp:   stamp{State: stateComplete, Features: features},
	}
}

func featureStack(described provider.BootstrapDescription, name string) (provider.BootstrapStack, bool) {
	for _, stack := range described.Stacks {
		if stack.Feature == name {
			return stack, true
		}
	}
	return provider.BootstrapStack{}, false
}

func TestAFeatureWhoseFrontNeverCameUpIsReportedAbsentSoTheGateRaisesItAgain(t *testing.T) {
	t.Parallel()

	b, registry := fronting(t)
	registry.front.installed = false

	described, err := b.described(context.Background(), surveyed(albFeature))
	if err != nil {
		t.Fatalf("described = %v", err)
	}
	stack, reported := featureStack(described, albFeature)
	if !reported {
		t.Fatalf("Describe reports %+v, want a stack for the %q the stamp records", described.Stacks, albFeature)
	}
	if stack.Present {
		t.Error("a feature the stamp records is reported installed whatever the front says, so a raise that failed after the stamp was written " +
			"reads as healthy and is never tried again")
	}
}

func TestAFeatureWhoseFrontIsInstalledIsReportedPresent(t *testing.T) {
	t.Parallel()

	b, registry := fronting(t)
	registry.front.installed = true

	described, err := b.described(context.Background(), surveyed(albFeature))
	if err != nil {
		t.Fatalf("described = %v", err)
	}
	if stack, _ := featureStack(described, albFeature); !stack.Present {
		t.Error("a feature whose front reported its address and its maps is not reported installed, so every bootstrap raises it again")
	}
}

func TestABootstrapThatNamedNoEdgeFeatureTakesNoFrontDown(t *testing.T) {
	t.Parallel()

	b, registry := fronting(t)
	if err := b.tearFronts(context.Background(), environment.TierProduction, nil); err != nil {
		t.Fatalf("tearFronts = %v", err)
	}
	if len(registry.opened) != 0 {
		t.Errorf("the removal opened %v, and a tier that never provisioned a load balancer has no state sealed under a passphrase to read, "+
			"let alone a stack to destroy", registry.opened)
	}
}

func TestABootstrapThatProvisionedTheLoadBalancerTakesItDownAgain(t *testing.T) {
	t.Parallel()

	b, registry := fronting(t)
	if err := b.tearFronts(context.Background(), environment.TierProduction, []string{albFeature}); err != nil {
		t.Fatalf("tearFronts = %v", err)
	}
	if !slices.Contains(registry.front.torn, environment.TierProduction) {
		t.Errorf("the removal tore down %v, want the production front: a forwarding rule left in place keeps billing", registry.front.torn)
	}
}

func TestRemovingTheFeatureTakesTheLoadBalancerDownRatherThanJustForgettingIt(t *testing.T) {
	t.Parallel()

	b, registry := fronting(t)
	req := provider.BootstrapRequest{Tier: environment.TierProduction, Remove: []string{albFeature}}
	if err := b.dropFronts(context.Background(), surveyed(albFeature), req, nil); err != nil {
		t.Fatalf("dropFronts = %v", err)
	}
	if !slices.Contains(registry.front.torn, environment.TierProduction) {
		t.Errorf("removing %q tore down %v, and un-stamping a feature whose address, forwarding rule and maps are still provisioned bills "+
			"for a front nothing will ever take down again", albFeature, registry.front.torn)
	}
}

func TestAFeatureWhoseFrontRefusedToComeDownStaysStamped(t *testing.T) {
	t.Parallel()

	b, registry := fronting(t)
	registry.front.refusal = refusal.Refuse(refusal.CodeInvalid, "shop.example.com is still bound")
	req := provider.BootstrapRequest{Tier: environment.TierProduction, Remove: []string{albFeature}}

	if err := b.dropFronts(context.Background(), surveyed(albFeature), req, nil); err == nil {
		t.Fatal("dropFronts = nil though the front refused, and the apply would go on to un-stamp a feature that is still installed")
	}
	if installed := installedFeatures(b.Catalogue(), []string{albFeature}, req); slices.Contains(installed, albFeature) {
		t.Errorf("the stamp would still name %v after a removal, which is only correct because the apply stops on the refusal above", installed)
	}
}

func TestRemovingTheFeatureIsRefusedWhileAHostnameIsStillBoundToItsFront(t *testing.T) {
	t.Parallel()

	b, registry := fronting(t)
	registry.front.bound = []string{"shop.example.com"}
	req := provider.BootstrapRequest{Tier: environment.TierProduction, Remove: []string{albFeature}}

	var refused refusal.Refusal
	err := b.dropFronts(context.Background(), surveyed(albFeature), req, nil)
	if !errors.As(err, &refused) || !strings.Contains(refused.Message, "shop.example.com") {
		t.Fatalf("dropFronts with a hostname still bound = %v, want a refusal naming it", err)
	}
	if len(registry.front.torn) != 0 {
		t.Errorf("the refused removal tore down %v: a certificate map with entries cannot be deleted, so the destroy fails partway",
			registry.front.torn)
	}
}

func TestAFeatureNothingInstalledIsNotTornDownOnRemoval(t *testing.T) {
	t.Parallel()

	b, registry := fronting(t)
	req := provider.BootstrapRequest{Tier: environment.TierProduction, Remove: []string{albFeature}}
	if err := b.dropFronts(context.Background(), surveyed(), req, nil); err != nil {
		t.Fatalf("dropFronts = %v", err)
	}
	if len(registry.opened) != 0 {
		t.Errorf("removing a feature the stamp never recorded opened %v, and there is no state sealed under a passphrase to read", registry.opened)
	}
}

func TestTheFrontsABootstrapRaisesComeFromWhatItsFeaturesDeclareTheyNeed(t *testing.T) {
	t.Parallel()

	b, registry := fronting(t)
	req := provider.BootstrapRequest{Tier: environment.TierProduction, Features: []string{albFeature}}
	if err := b.raiseFronts(context.Background(), req, nil); err != nil {
		t.Fatalf("raiseFronts = %v", err)
	}
	if !slices.Equal(registry.opened, []edge.Kind{alb.Kind}) {
		t.Errorf("the bootstrap opened %v, want the edge the feature's Needs name: a second edge with a front of its own must not need a branch here",
			registry.opened)
	}
}

func TestAFrontThatReportsNothingIsInstalledAndOwnsNoHostname(t *testing.T) {
	t.Parallel()

	b, registry := fronting(t)
	registry.front.silent = true
	registry.front.bound = []string{"shop.example.com"}

	installed, err := b.frontInstalled(context.Background(), environment.TierProduction, albFeature)
	if err != nil {
		t.Fatalf("frontInstalled = %v", err)
	}
	if !installed {
		t.Error("a front that reports nothing about its bootstrap is taken for gone, so the stamp alone can no longer say the feature is installed")
	}
	if err := b.frontsFree(context.Background(), environment.TierProduction, []string{albFeature}); err != nil {
		t.Errorf("frontsFree = %v, want nothing owned by a front that names no bound hostname", err)
	}
}

func TestInstallingAndDroppingAFrontSaysWhichFeatureAndEdgeForWhichTier(t *testing.T) {
	t.Parallel()

	b, _ := fronting(t)
	progress := &fake.Log{}
	raising := provider.BootstrapRequest{Tier: environment.TierProduction, Features: []string{albFeature}}
	if err := b.raiseFronts(context.Background(), raising, progress); err != nil {
		t.Fatalf("raiseFronts = %v", err)
	}
	dropping := provider.BootstrapRequest{Tier: environment.TierPreview, Remove: []string{albFeature}}
	if err := b.dropFronts(context.Background(), surveyed(albFeature), dropping, progress); err != nil {
		t.Fatalf("dropFronts = %v", err)
	}

	want := []string{
		"INFO Installing feature alb-edge for production: " + b.Catalogue()[0].Summary,
		"INFO Taking down the alb edge's front for preview: this bootstrap no longer requests feature alb-edge",
	}
	if got := progress.Lines(); !slices.Equal(got, want) {
		t.Errorf("the bootstrap said %q, want %q", got, want)
	}
}

func TestDroppingTheEdgeFeatureForgetsWhatItsBootstrapAdopted(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name string
		take func(b bootstrap) error
	}{
		{"dropFronts", func(b bootstrap) error {
			req := provider.BootstrapRequest{Tier: environment.TierProduction, Remove: []string{albFeature}}
			return b.dropFronts(context.Background(), surveyed(albFeature), req, nil)
		}},
		{"tearFronts", func(b bootstrap) error {
			return b.tearFronts(context.Background(), environment.TierProduction, []string{albFeature})
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			b, registry, h := frontingWithSecrets(t)
			registry.front.out = edge.BootstrapOutput{Offers: fullOffers("c1", "c2"), Values: map[string]string{"cacheBucket": "b"}}
			raising := provider.BootstrapRequest{Tier: environment.TierProduction, Features: []string{albFeature}}
			if err := b.raiseFronts(context.Background(), raising, nil); err != nil {
				t.Fatalf("raiseFronts = %v", err)
			}
			kind := registry.front.Kind()
			credentials := h.clients.EdgeCredentialsSecret(environment.TierProduction, kind)
			seed := h.clients.ISRWriterSeedSecret(environment.TierProduction, kind)
			if _, found := adoptedAs(t, h, kind); !found || !h.secrets.has(credentials) || !h.secrets.has(seed) {
				t.Fatal("the raise adopted nothing")
			}

			if err := tc.take(b); err != nil {
				t.Fatalf("%s = %v", tc.name, err)
			}
			if _, found := adoptedAs(t, h, kind); found {
				t.Error("the edge's record outlived its teardown")
			}
			if h.secrets.has(credentials) || h.secrets.has(seed) {
				t.Error("a secret of the edge outlived its teardown")
			}
		})
	}
}

func TestAFrontThatRefusedToComeDownKeepsWhatItsBootstrapAdopted(t *testing.T) {
	t.Parallel()

	b, registry, h := frontingWithSecrets(t)
	registry.front.out = edge.BootstrapOutput{Offers: fullOffers("c1", "c2")}
	if err := b.raiseFronts(context.Background(), provider.BootstrapRequest{Tier: environment.TierProduction, Features: []string{albFeature}}, nil); err != nil {
		t.Fatal(err)
	}
	registry.front.refusal = refusal.Refuse(refusal.CodeInvalid, "still bound")
	if err := b.tearFronts(context.Background(), environment.TierProduction, []string{albFeature}); err == nil {
		t.Fatal("tearFronts = nil though the front refused")
	}
	if !h.secrets.has(h.clients.ISRWriterSeedSecret(environment.TierProduction, registry.front.Kind())) {
		t.Error("the seed went before the edge did, though its workers still run")
	}
}

func adoptedAs(t *testing.T, h *offersHarness, kind edge.Kind) (adoptedEdge, bool) {
	t.Helper()
	entry, err := h.records.Read(context.Background(), adoptedEdgeKey(environment.TierProduction, kind))
	if errors.Is(err, keyvalue.ErrNotFound) {
		return adoptedEdge{}, false
	}
	if err != nil {
		t.Fatal(err)
	}
	var adopted adoptedEdge
	if err := json.Unmarshal(entry.Value, &adopted); err != nil {
		t.Fatal(err)
	}
	return adopted, true
}
