package gcp

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/bootstrapplan"
	"github.com/ocelhq/ocel/pkg/provider/fake"
	"github.com/ocelhq/ocel/pkg/records"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/pkg/stackrecords"
	"github.com/ocelhq/ocel/platform/gcp/provider/edges/alb"
	"github.com/ocelhq/ocel/platform/gcp/provider/edges/albpart"
	"github.com/ocelhq/ocel/platform/gcp/provider/edges/albresources"
	"github.com/ocelhq/ocel/platform/gcp/provider/edges/cloudflareorigin"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/bootstrap"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/gcloud"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/googletest"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/names"
	"github.com/ocelhq/ocel/platform/gcp/provider/ports"
)

type frontRegistry struct {
	mu     sync.Mutex
	opened []edge.Kind
	front  *countingFront
}

func (r *frontRegistry) Open(kind edge.Kind, _ provider.Options) (edge.Edge, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.opened = append(r.opened, kind)
	return r.front, nil
}

type countingFront struct {
	*alb.Edge
	mu        sync.Mutex
	tearing   func(ctx context.Context) error
	raised    []environment.Tier
	torn      []environment.Tier
	installed bool
	bound     []string
	refusal   error
	silent    bool
	parts     []edge.BootstrapPart
	plan      []edge.PlanChange
	out       edge.BootstrapOutput
	describe  error
	runsCode  bool
}

func (f *countingFront) Facts() edge.Facts { return edge.Facts{RunsCode: f.runsCode} }

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
		PlanBootstrap: func(context.Context, environment.Tier) ([]edge.PlanChange, error) {
			return f.plan, nil
		},
		PlanRemoveBootstrap: func(context.Context, environment.Tier) ([]edge.PlanChange, error) {
			return f.plan, nil
		},
	}
}

func (f *countingFront) Bootstrap(_ context.Context, tier environment.Tier) (edge.BootstrapOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.raised = append(f.raised, tier)
	return f.out, nil
}

func (f *countingFront) Teardown(ctx context.Context, tier environment.Tier) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.tearing != nil {
		return f.tearing(ctx)
	}
	if f.refusal != nil {
		return f.refusal
	}
	f.torn = append(f.torn, tier)
	return nil
}

func fronting(t *testing.T) (bootstrap.Engine, *frontRegistry) {
	t.Helper()
	b, registry, _ := frontingWithSecrets(t)
	return b, registry
}

func frontingWithSecrets(t *testing.T) (bootstrap.Engine, *frontRegistry, *offersHarness) {
	t.Helper()
	registry := &frontRegistry{front: &countingFront{}}
	h := newOffersHarness(t)
	return composed(bootstrap.Engine{Edges: registry, Clients: h.clients, Records: h.records}), registry, h
}

func surveyed(parts ...string) bootstrap.Survey {
	return bootstrap.Survey{
		Names:   names.New("ocel", "acme-prod"),
		Tier:    environment.TierProduction,
		Project: "acme-prod",
		Present: true,
		Stamp:   bootstrap.Stamp{State: bootstrap.StateComplete, Parts: parts},
	}
}

func partStack(described provider.BootstrapDescription, name string) (provider.BootstrapStack, bool) {
	for _, stack := range described.Stacks {
		if stack.Part == name {
			return stack, true
		}
	}
	return provider.BootstrapStack{}, false
}

func TestAPartWhoseFrontNeverCameUpIsReportedAbsentSoTheGateRaisesItAgain(t *testing.T) {
	t.Parallel()

	b, registry := fronting(t)
	registry.front.installed = false

	described, err := b.Described(context.Background(), surveyed(bootstrap.CloudflareOriginPart))
	if err != nil {
		t.Fatalf("described = %v", err)
	}
	stack, reported := partStack(described, bootstrap.CloudflareOriginPart)
	if !reported {
		t.Fatalf("Describe reports %+v, want a stack for the %q the stamp records", described.Stacks, bootstrap.CloudflareOriginPart)
	}
	if stack.Present {
		t.Error("a part the stamp records is reported installed whatever the front says, so a raise that failed after the stamp was written " +
			"reads as healthy and is never tried again")
	}
}

func TestAPartWhoseFrontIsInstalledIsReportedPresent(t *testing.T) {
	t.Parallel()

	b, registry := fronting(t)
	registry.front.installed = true

	described, err := b.Described(context.Background(), surveyed(bootstrap.CloudflareOriginPart))
	if err != nil {
		t.Fatalf("described = %v", err)
	}
	if stack, _ := partStack(described, bootstrap.CloudflareOriginPart); !stack.Present {
		t.Error("a part whose front reported its address and its maps is not reported installed, so every bootstrap raises it again")
	}
}

func TestABootstrapThatNamedNoEdgePartTakesNoFrontDown(t *testing.T) {
	t.Parallel()

	b, registry := fronting(t)
	if err := b.TearFronts(context.Background(), environment.TierProduction, nil); err != nil {
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
	if err := b.TearFronts(context.Background(), environment.TierProduction, []string{bootstrap.CloudflareOriginPart}); err != nil {
		t.Fatalf("tearFronts = %v", err)
	}
	if !slices.Contains(registry.front.torn, environment.TierProduction) {
		t.Errorf("the removal tore down %v, want the production front: a forwarding rule left in place keeps billing", registry.front.torn)
	}
}

func TestRemovingThePartTakesTheLoadBalancerDownRatherThanJustForgettingIt(t *testing.T) {
	t.Parallel()

	b, registry := fronting(t)
	req := provider.BootstrapRequest{Tier: environment.TierProduction, Remove: []string{bootstrap.CloudflareOriginPart}}
	if err := b.DropFronts(context.Background(), surveyed(bootstrap.CloudflareOriginPart), req, nil); err != nil {
		t.Fatalf("dropFronts = %v", err)
	}
	if !slices.Contains(registry.front.torn, environment.TierProduction) {
		t.Errorf("removing %q tore down %v, and un-stamping a part whose address, forwarding rule and maps are still provisioned bills "+
			"for a front nothing will ever take down again", bootstrap.CloudflareOriginPart, registry.front.torn)
	}
}

func TestAPartWhoseFrontRefusedToComeDownStaysStamped(t *testing.T) {
	t.Parallel()

	b, registry := fronting(t)
	registry.front.refusal = refusal.Refuse(refusal.CodeInvalid, "shop.example.com is still bound")
	req := provider.BootstrapRequest{Tier: environment.TierProduction, Remove: []string{bootstrap.CloudflareOriginPart}}

	if err := b.DropFronts(context.Background(), surveyed(bootstrap.CloudflareOriginPart), req, nil); err == nil {
		t.Fatal("dropFronts = nil though the front refused, and the apply would go on to un-stamp a part that is still installed")
	}
	if installed := bootstrap.InstalledParts(b.Parts(), []string{bootstrap.CloudflareOriginPart}, req); slices.Contains(installed, bootstrap.CloudflareOriginPart) {
		t.Errorf("the stamp would still name %v after a removal, which is only correct because the apply stops on the refusal above", installed)
	}
}

func TestRemovingThePartIsRefusedWhileAHostnameIsStillBoundToItsFront(t *testing.T) {
	t.Parallel()

	b, registry := fronting(t)
	registry.front.bound = []string{"shop.example.com"}
	req := provider.BootstrapRequest{Tier: environment.TierProduction, Remove: []string{bootstrap.CloudflareOriginPart}}

	var refused refusal.Refusal
	err := b.DropFronts(context.Background(), surveyed(bootstrap.CloudflareOriginPart), req, nil)
	if !errors.As(err, &refused) || !strings.Contains(refused.Message, "shop.example.com") {
		t.Fatalf("dropFronts with a hostname still bound = %v, want a refusal naming it", err)
	}
	if len(registry.front.torn) != 0 {
		t.Errorf("the refused removal tore down %v: a certificate map with entries cannot be deleted, so the destroy fails partway",
			registry.front.torn)
	}
}

func serveBehind(t *testing.T, store records.Store, tier environment.Tier, slug string, kind edge.Kind) {
	t.Helper()
	state, err := json.Marshal(stackrecords.EdgeState{Kind: kind})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Write(t.Context(), records.Entry{Key: stackrecords.EdgeStackKey(tier, slug), Value: state}); err != nil {
		t.Fatal(err)
	}
}

func TestRemovingThePartIsRefusedByItsPlanAndItsApplyWhileAProjectIsServedBehindItsEdge(t *testing.T) {
	t.Parallel()

	b, registry := fronting(t)
	serveBehind(t, b.Records, environment.TierProduction, "shop", albresources.Kind)
	serveBehind(t, b.Records, environment.TierProduction, "blog", "cloudfront")
	serveBehind(t, b.Records, environment.TierPreview, "docs", albresources.Kind)
	req := provider.BootstrapRequest{Tier: environment.TierProduction, Remove: []string{bootstrap.CloudflareOriginPart}}

	for what, err := range map[string]error{
		"plan":  b.FrontsFree(t.Context(), environment.TierProduction, []string{bootstrap.CloudflareOriginPart}),
		"apply": b.DropFronts(t.Context(), surveyed(bootstrap.CloudflareOriginPart), req, nil),
	} {
		var refused refusal.Refusal
		if !errors.As(err, &refused) || !strings.Contains(refused.Message, "shop") || !strings.Contains(refused.Message, "ocel projects destroy") {
			t.Errorf("%s while shop is served behind the edge = %v, want a refusal naming shop and `ocel projects destroy`: "+
				"removing the part deletes the origin secret every release demands, and each answers 403", what, err)
			continue
		}
		if strings.Contains(refused.Message, "blog") || strings.Contains(refused.Message, "docs") {
			t.Errorf("%s refused naming %q, want only the projects this edge serves on this tier", what, refused.Message)
		}
	}
	if len(registry.front.torn) != 0 {
		t.Errorf("the refused removal tore down %v", registry.front.torn)
	}
}

func TestAPartNothingInstalledIsNotTornDownOnRemoval(t *testing.T) {
	t.Parallel()

	b, registry := fronting(t)
	req := provider.BootstrapRequest{Tier: environment.TierProduction, Remove: []string{bootstrap.CloudflareOriginPart}}
	if err := b.DropFronts(context.Background(), surveyed(), req, nil); err != nil {
		t.Fatalf("dropFronts = %v", err)
	}
	if len(registry.opened) != 0 {
		t.Errorf("removing a part the stamp never recorded opened %v, and there is no state sealed under a passphrase to read", registry.opened)
	}
}

func TestTheFrontsABootstrapRaisesComeFromWhatItsPartsDeclareTheyNeed(t *testing.T) {
	t.Parallel()

	b, registry := fronting(t)
	req := provider.BootstrapRequest{Tier: environment.TierProduction, Parts: []string{bootstrap.CloudflareOriginPart}}
	if err := b.RaiseFronts(context.Background(), req, nil); err != nil {
		t.Fatalf("raiseFronts = %v", err)
	}
	if !slices.Equal(registry.opened, []edge.Kind{"cloudflare"}) {
		t.Errorf("the bootstrap opened %v, want the edge the part's Needs name: a second edge with a front of its own must not need a branch here",
			registry.opened)
	}
}

func TestAPartBehindAnEdgeThatRunsCodeIsReportedAbsentWithoutItsOriginSecret(t *testing.T) {
	t.Parallel()

	b, registry, _ := frontingWithSecrets(t)
	registry.front.installed, registry.front.runsCode = true, true

	described, err := b.Described(context.Background(), surveyed(bootstrap.CloudflareOriginPart))
	if err != nil {
		t.Fatalf("described = %v", err)
	}
	if stack, _ := partStack(described, bootstrap.CloudflareOriginPart); stack.Present {
		t.Error("a part the stamp records is reported installed with no origin secret in Secret Manager, " +
			"though every deploy behind the edge is refused until a bootstrap stores one")
	}
}

func TestAPartBehindAnEdgeThatRunsCodeIsReportedPresentWithItsOriginSecret(t *testing.T) {
	t.Parallel()

	b, registry, h := frontingWithSecrets(t)
	registry.front.installed, registry.front.runsCode = true, true
	if err := cloudflareorigin.EnsureOriginSecret(t.Context(), h.clients, environment.TierProduction, registry.front.Kind(), originSecretMintedAt, h.log); err != nil {
		t.Fatalf("EnsureOriginSecret() = %v", err)
	}

	described, err := b.Described(context.Background(), surveyed(bootstrap.CloudflareOriginPart))
	if err != nil {
		t.Fatalf("described = %v", err)
	}
	if stack, _ := partStack(described, bootstrap.CloudflareOriginPart); !stack.Present {
		t.Error("a part whose front is installed and whose origin secret is stored is not reported installed, so every bootstrap raises it again")
	}
}

func TestAPartWhoseOriginSecretIsUnreadableIsReportedBroken(t *testing.T) {
	t.Parallel()

	b, registry, h := frontingWithSecrets(t)
	registry.front.installed, registry.front.runsCode = true, true
	service, err := h.clients.Secrets()
	if err != nil {
		t.Fatal(err)
	}
	name := h.clients.OriginSecret(environment.TierProduction, registry.front.Kind())
	if err := h.clients.EnsureSecret(t.Context(), service, environment.TierProduction, name); err != nil {
		t.Fatal(err)
	}
	if _, err := h.clients.AddSecretVersion(t.Context(), service, name, []byte("not an origin secret")); err != nil {
		t.Fatal(err)
	}

	if _, err := b.Described(context.Background(), surveyed(bootstrap.CloudflareOriginPart)); refusalCode(err) != refusal.CodeInvalid {
		t.Errorf("described = %v, want the refusal naming the origin secret's secret to delete", err)
	}
}

func TestAFrontThatReportsNothingIsInstalledAndOwnsNoHostname(t *testing.T) {
	t.Parallel()

	b, registry := fronting(t)
	registry.front.silent = true
	registry.front.bound = []string{"shop.example.com"}

	installed, err := b.FrontInstalled(context.Background(), environment.TierProduction, bootstrap.CloudflareOriginPart)
	if err != nil {
		t.Fatalf("frontInstalled = %v", err)
	}
	if !installed {
		t.Error("a front that reports nothing about its bootstrap is taken for gone, so the stamp alone can no longer say the part is installed")
	}
	if err := b.FrontsFree(context.Background(), environment.TierProduction, []string{bootstrap.CloudflareOriginPart}); err != nil {
		t.Errorf("frontsFree = %v, want nothing owned by a front that names no bound hostname", err)
	}
}

func TestInstallingAndDroppingAFrontSaysWhichPartAndEdgeForWhichTier(t *testing.T) {
	t.Parallel()

	b, _ := fronting(t)
	progress := &fake.Log{}
	raising := provider.BootstrapRequest{Tier: environment.TierProduction, Parts: []string{bootstrap.CloudflareOriginPart}}
	if err := b.RaiseFronts(context.Background(), raising, progress); err != nil {
		t.Fatalf("raiseFronts = %v", err)
	}
	dropping := provider.BootstrapRequest{Tier: environment.TierPreview, Remove: []string{bootstrap.CloudflareOriginPart}}
	if err := b.DropFronts(context.Background(), surveyed(bootstrap.CloudflareOriginPart), dropping, progress); err != nil {
		t.Fatalf("dropFronts = %v", err)
	}

	want := []string{
		"INFO Adding the cloudflare-origin part of the production bootstrap",
		"INFO Taking down the alb edge's front for preview: the cloudflare-origin part is being removed",
	}
	if got := progress.Lines(); !slices.Equal(got, want) {
		t.Errorf("the bootstrap said %q, want %q", got, want)
	}
}

func TestDroppingTheEdgePartForgetsWhatItsBootstrapAdopted(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name string
		take func(b bootstrap.Engine) error
	}{
		{"dropFronts", func(b bootstrap.Engine) error {
			req := provider.BootstrapRequest{Tier: environment.TierProduction, Remove: []string{bootstrap.CloudflareOriginPart}}
			return b.DropFronts(context.Background(), surveyed(bootstrap.CloudflareOriginPart), req, nil)
		}},
		{"tearFronts", func(b bootstrap.Engine) error {
			return b.TearFronts(context.Background(), environment.TierProduction, []string{bootstrap.CloudflareOriginPart})
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			b, registry, h := frontingWithSecrets(t)
			registry.front.out = fullOffers("c1")
			raising := provider.BootstrapRequest{Tier: environment.TierProduction, Parts: []string{bootstrap.CloudflareOriginPart}}
			if err := b.RaiseFronts(context.Background(), raising, nil); err != nil {
				t.Fatalf("raiseFronts = %v", err)
			}
			kind := registry.front.Kind()
			credentials := h.clients.EdgeCredentialsSecret(environment.TierProduction, kind)
			seed := h.clients.PurgeSeedSecret(environment.TierProduction, kind)
			if _, found := adoptedAs(t, h, kind); !found || !h.secrets.Has(credentials) || !h.secrets.Has(seed) {
				t.Fatal("the raise adopted nothing")
			}

			if err := tc.take(b); err != nil {
				t.Fatalf("%s = %v", tc.name, err)
			}
			if _, found := adoptedAs(t, h, kind); found {
				t.Error("the edge's record outlived its teardown")
			}
			if h.secrets.Has(credentials) || h.secrets.Has(seed) {
				t.Error("a secret of the edge outlived its teardown")
			}
		})
	}
}

func TestAFrontThatRefusedToComeDownKeepsWhatItsBootstrapAdopted(t *testing.T) {
	t.Parallel()

	b, registry, h := frontingWithSecrets(t)
	registry.front.out = fullOffers("c1")
	if err := b.RaiseFronts(context.Background(), provider.BootstrapRequest{Tier: environment.TierProduction, Parts: []string{bootstrap.CloudflareOriginPart}}, nil); err != nil {
		t.Fatal(err)
	}
	registry.front.refusal = refusal.Refuse(refusal.CodeInvalid, "still bound")
	if err := b.TearFronts(context.Background(), environment.TierProduction, []string{bootstrap.CloudflareOriginPart}); err == nil {
		t.Fatal("tearFronts = nil though the front refused")
	}
	if !h.secrets.Has(h.clients.PurgeSeedSecret(environment.TierProduction, registry.front.Kind())) {
		t.Error("the seed went before the edge did, though its workers still run")
	}
}

func adoptedAs(t *testing.T, h *offersHarness, kind edge.Kind) (cloudflareorigin.AdoptedEdge, bool) {
	t.Helper()
	entry, err := h.records.Read(context.Background(), cloudflareorigin.AdoptedEdgeKey(environment.TierProduction, kind))
	if errors.Is(err, records.ErrNotFound) {
		return cloudflareorigin.AdoptedEdge{}, false
	}
	if err != nil {
		t.Fatal(err)
	}
	var adopted cloudflareorigin.AdoptedEdge
	if err := json.Unmarshal(entry.Value, &adopted); err != nil {
		t.Fatal(err)
	}
	return adopted, true
}

func TestABootstrapPlanNamesWhatTheEdgesFrontInstallsAndWhatRemovingItTakes(t *testing.T) {
	t.Parallel()

	b, registry := fronting(t)
	registry.front.plan = []edge.PlanChange{{Kind: "Cloudflare::Worker", Name: "ocel-releases-store", Action: edge.PlanCreate}}

	installs, err := b.PlannedFronts(context.Background(), environment.TierProduction, []string{bootstrap.CloudflareOriginPart})
	if err != nil {
		t.Fatal(err)
	}
	removes, err := b.RemovedFronts(context.Background(), environment.TierProduction, []string{bootstrap.CloudflareOriginPart})
	if err != nil {
		t.Fatal(err)
	}

	for what, groups := range map[string][]provider.ChangeGroup{"installs": installs, "removes": removes} {
		if len(groups) != 1 || groups[0].Name != edge.EdgeGroupName(albresources.Kind) || len(groups[0].Changes) != 1 || groups[0].Changes[0].Name != "ocel-releases-store" {
			t.Errorf("%s = %+v, want one edge group carrying the front's planned worker", what, groups)
		}
	}
	if removes[0].Action != provider.ActionDelete {
		t.Errorf("the removal group's action = %q, want delete", removes[0].Action)
	}
}

func TestABootstrapInstallingAFrontPlansTheEdgeBucketAndKeysTheFrontReadsAsPartsAddedWithIt(t *testing.T) {
	t.Parallel()

	b, _ := fronting(t)
	planned, err := b.PlanFrontedParts(context.Background(), provider.BootstrapRequest{Tier: environment.TierProduction, Parts: []string{bootstrap.CloudflareOriginPart}})
	if err != nil {
		t.Fatal(err)
	}
	var parts []string
	for _, group := range planned {
		parts = append(parts, group.Part)
	}
	if !slices.Equal(parts, []string{bootstrap.KeysPart, bootstrap.EdgeBucketPart}) {
		t.Errorf("a bootstrap installing %s plans the parts %v, want the keys and the edge bucket its front reads", bootstrap.CloudflareOriginPart, parts)
	}
	if none, err := b.PlanFrontedParts(context.Background(), provider.BootstrapRequest{Tier: environment.TierProduction}); err != nil || len(none) != 0 {
		t.Errorf("a bootstrap installing no front plans %+v, %v, want no part: the first deploy that needs one adds it", none, err)
	}
}

type noRecordsDatabase struct{ records.Store }

func (noRecordsDatabase) Read(context.Context, records.Key) (records.Entry, error) {
	return records.Entry{}, errors.Join(refusal.Refuse(refusal.CodeNotReady, "no records database"), ports.ErrNoRecordsDatabase)
}

func TestAFirstBootstrapPlansTheFrontsPartsBeforeItsRecordsDatabaseExists(t *testing.T) {
	t.Parallel()

	b, _ := fronting(t)
	b.Records = noRecordsDatabase{b.Records}
	planned, err := b.PlanFrontedParts(context.Background(), provider.BootstrapRequest{Tier: environment.TierProduction, Parts: []string{bootstrap.CloudflareOriginPart}})
	if err != nil {
		t.Fatalf("PlanFrontedParts() = %v, want the parts planned as first use: the bootstrap creates the records database", err)
	}
	for _, group := range planned {
		if group.Action != provider.ActionCreate || group.Reason != bootstrap.ReasonFirstUse {
			t.Errorf("part %s plans %s (%s), want a create on first use", group.Part, group.Action, group.Reason)
		}
	}
	if len(planned) != 2 {
		t.Errorf("planned %d parts, want the keys and the edge bucket", len(planned))
	}
}

func TestTheLoadBalancerBehindTheALBEdgeIsAPartNoBootstrapRequires(t *testing.T) {
	t.Parallel()

	offered := composed(bootstrap.Engine{}).Parts()
	for _, kind := range []edge.Kind{albresources.Kind, edge.None} {
		required := bootstrapplan.RequiredParts(offered, kind)
		if len(required) != 0 {
			t.Errorf("a bootstrap fronted by %q requires %v, want nothing: the first deploy behind the alb adds its load balancer", kind, required)
		}
	}
	b := composed(bootstrap.Engine{Clients: &gcloud.Clients{Names: names.New("ocel", "acme-prod"), Region: "europe-west1"}})
	balancer := albpart.NewPart(b, environment.TierProduction)
	if !strings.Contains(balancer.Summary, "$18") || !balancer.Retires {
		t.Errorf("the %s part is %+v, want its summary to say the price and it retiring with its last project", albpart.PartName, balancer)
	}
	for _, want := range []string{"gate", "plugin", "repository"} {
		if !strings.Contains(balancer.Summary, want) {
			t.Errorf("the %s part reads %q, want it to mention the %s it holds", albpart.PartName, balancer.Summary, want)
		}
	}
}

func TestTheCloudflareOriginPartHoldsNoLoadBalancerAndOnlyTheCloudflareEdgePullsItIn(t *testing.T) {
	t.Parallel()

	b := composed(bootstrap.Engine{})
	offered := b.Parts()
	at := slices.IndexFunc(offered, func(f provider.Part) bool { return f.Name == bootstrap.CloudflareOriginPart })
	if at < 0 {
		t.Fatalf("Parts() = %v, want the %q part", offered, bootstrap.CloudflareOriginPart)
	}
	summary := offered[at].Summary
	for _, said := range []string{"load balancer", "$18"} {
		if strings.Contains(summary, said) {
			t.Errorf("the %q part reads %q, and names %q: the worker reaches each release on its revision-tag URL", bootstrap.CloudflareOriginPart, summary, said)
		}
	}
	for _, want := range []string{"origin secret", "HMAC key"} {
		if !strings.Contains(summary, want) {
			t.Errorf("the %q part reads %q, want it to name the %s it holds", bootstrap.CloudflareOriginPart, summary, want)
		}
	}
	if proxied := bootstrapplan.RequiredParts(offered, "cloudflare"); !slices.Contains(proxied, bootstrap.CloudflareOriginPart) {
		t.Errorf("a cloudflare bootstrap requires %v, want %q", proxied, bootstrap.CloudflareOriginPart)
	}
	if fronted := bootstrapplan.RequiredParts(offered, albresources.Kind); slices.Contains(fronted, bootstrap.CloudflareOriginPart) {
		t.Errorf("an %q bootstrap requires %v, and nothing it binds reads the Cloudflare edge's origin secret", albresources.Kind, fronted)
	}
	for _, permission := range bootstrap.PermissionsFor([]string{bootstrap.CloudflareOriginPart}) {
		for _, service := range []string{"compute.", "certificatemanager.", "networksecurity."} {
			if strings.HasPrefix(permission, service) {
				t.Errorf("a %q bootstrap checks %s, and a credential would be refused for a load balancer it never raises", bootstrap.CloudflareOriginPart, permission)
			}
		}
	}

	read := surveyed(bootstrap.CloudflareOriginPart)
	stack := provider.BootstrapStack{Name: read.Project + "/" + string(read.Tier) + "/" + bootstrap.CloudflareOriginPart, Part: bootstrap.CloudflareOriginPart, Present: true}
	for _, part := range bootstrap.SurveyedParts(b, read, []provider.BootstrapStack{stack}) {
		if part.Name == bootstrap.CloudflareOriginPart && part.Inventory != nil {
			t.Errorf("the described %q part is inventoried as %+v, want no priced load balancer", bootstrap.CloudflareOriginPart, part.Inventory)
		}
	}
}

func TestTheServicesTheLoadBalancerNeedsAreOnlyDemandedWhenItIsBeingAdded(t *testing.T) {
	t.Parallel()

	b := composed(bootstrap.Engine{Clients: &gcloud.Clients{Names: names.New("ocel", "acme-prod"), Region: "europe-west1"}})
	balancer := albpart.NewPart(b, environment.TierPreview)
	for _, api := range []string{"compute.googleapis.com", "certificatemanager.googleapis.com", "networkservices.googleapis.com"} {
		if !slices.Contains(balancer.APIs, api) {
			t.Errorf("the %s part checks %v, want %s among them: the gate is a Service Extensions plugin, and the front needs the rest", albpart.PartName, balancer.APIs, api)
		}
	}
	for _, api := range []string{"compute.googleapis.com", "certificatemanager.googleapis.com"} {
		if slices.Contains(bootstrap.BootstrapAPIs, api) {
			t.Errorf("a bootstrap provisioning no load balancer demands %s, and it would be refused for a service it never calls", api)
		}
	}
}

func TestABootstrapProvisioningNoLoadBalancerChecksNoLoadBalancerPermission(t *testing.T) {
	t.Parallel()

	for _, permission := range bootstrap.PermissionsFor(nil) {
		if strings.HasPrefix(permission, "compute.") || strings.HasPrefix(permission, "certificatemanager.") {
			t.Errorf("a bootstrap provisioning no load balancer checks %s, and a credential would be refused for a service it never calls", permission)
		}
	}
	plain := bootstrap.GrantsFor(nil).Roles()
	if slices.ContainsFunc(plain, func(role string) bool {
		return role == "roles/compute.loadBalancerAdmin" || strings.HasPrefix(role, "roles/certificatemanager.")
	}) {
		t.Errorf("the refusal for a plain bootstrap names %v, and a role for a front nothing provisions is one more than the credential needs", plain)
	}
}

func TestTheEdgeBucketAndKeysAreNotRemovedWhileAnInstalledFrontReadsThem(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	tier := environment.TierProduction
	b := composed(bootstrap.Engine{Clients: &gcloud.Clients{Names: names.New("ocel", "acme-prod"), Region: "europe-west1"}, Records: fake.NewKeyValues()})
	for _, part := range []string{bootstrap.KeysPart, bootstrap.EdgeBucketPart} {
		if err := stackrecords.MarkBootstrapPartReady(ctx, b.Records, tier, part, stackrecords.BootstrapPart{}); err != nil {
			t.Fatal(err)
		}
	}

	for _, part := range []string{bootstrap.KeysPart, bootstrap.EdgeBucketPart} {
		_, err := b.PlanRemovedParts(ctx, tier, []string{part}, true, []string{bootstrap.CloudflareOriginPart})
		var refused refusal.Refusal
		if !errors.As(err, &refused) || !strings.Contains(refused.Message, "--remove "+bootstrap.CloudflareOriginPart) {
			t.Errorf("PlanRemovedParts(%s) while the %s front is installed = %v, want a refusal naming the removal that takes the front with it", part, bootstrap.CloudflareOriginPart, err)
		}
		if groups, err := b.PlanRemovedParts(ctx, tier, []string{part}, false, nil); err != nil || len(groups) != 1 {
			t.Errorf("PlanRemovedParts(%s) with no front installed = %+v, %v, want it planned", part, groups, err)
		}
	}
}

func TestABootstrapRemovingTheEdgeBucketOrKeysUnderAnInstalledFrontIsRefusedByItsPlanAndItsApply(t *testing.T) {
	t.Parallel()
	tier := environment.TierProduction
	for _, part := range []string{bootstrap.KeysPart, bootstrap.EdgeBucketPart} {
		for step, run := range map[string]func(context.Context, bootstrap.Engine, provider.BootstrapRequest) error{
			"Plan": func(ctx context.Context, b bootstrap.Engine, req provider.BootstrapRequest) error {
				_, err := b.Plan(ctx, req)
				return err
			},
			"Apply": func(ctx context.Context, b bootstrap.Engine, req provider.BootstrapRequest) error {
				return b.Apply(ctx, req, nil)
			},
		} {
			t.Run(part+"/"+step, func(t *testing.T) {
				t.Parallel()
				b := bootstrappingOver(t, googletest.GrantedIAM(), bootstrap.CloudflareOriginPart)
				if err := stackrecords.MarkBootstrapPartReady(t.Context(), b.Records, tier, part, stackrecords.BootstrapPart{}); err != nil {
					t.Fatal(err)
				}

				err := run(t.Context(), b, provider.BootstrapRequest{Tier: tier, Parts: []string{bootstrap.CloudflareOriginPart}, Remove: []string{part}})

				var refused refusal.Refusal
				if !errors.As(err, &refused) || !strings.Contains(refused.Message, "--remove "+bootstrap.CloudflareOriginPart) {
					t.Errorf("%s() removing %s while the %s front is installed = %v, want a refusal naming the removal that takes the front with it", step, part, bootstrap.CloudflareOriginPart, err)
				}
			})
		}
	}
}
