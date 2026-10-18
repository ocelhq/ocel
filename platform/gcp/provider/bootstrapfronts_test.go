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
	"github.com/ocelhq/ocel/platform/gcp/provider/edges/alb"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/gcloud"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/names"
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

func surveyed(parts ...string) survey {
	return survey{
		Names:   names.New("ocel", "acme-prod"),
		Tier:    environment.TierProduction,
		Project: "acme-prod",
		Present: true,
		Stamp:   stamp{State: stateComplete, Parts: parts},
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

	described, err := b.described(context.Background(), surveyed(cloudflareOriginPart))
	if err != nil {
		t.Fatalf("described = %v", err)
	}
	stack, reported := partStack(described, cloudflareOriginPart)
	if !reported {
		t.Fatalf("Describe reports %+v, want a stack for the %q the stamp records", described.Stacks, cloudflareOriginPart)
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

	described, err := b.described(context.Background(), surveyed(cloudflareOriginPart))
	if err != nil {
		t.Fatalf("described = %v", err)
	}
	if stack, _ := partStack(described, cloudflareOriginPart); !stack.Present {
		t.Error("a part whose front reported its address and its maps is not reported installed, so every bootstrap raises it again")
	}
}

func TestABootstrapThatNamedNoEdgePartTakesNoFrontDown(t *testing.T) {
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
	if err := b.tearFronts(context.Background(), environment.TierProduction, []string{cloudflareOriginPart}); err != nil {
		t.Fatalf("tearFronts = %v", err)
	}
	if !slices.Contains(registry.front.torn, environment.TierProduction) {
		t.Errorf("the removal tore down %v, want the production front: a forwarding rule left in place keeps billing", registry.front.torn)
	}
}

func TestRemovingThePartTakesTheLoadBalancerDownRatherThanJustForgettingIt(t *testing.T) {
	t.Parallel()

	b, registry := fronting(t)
	req := provider.BootstrapRequest{Tier: environment.TierProduction, Remove: []string{cloudflareOriginPart}}
	if err := b.dropFronts(context.Background(), surveyed(cloudflareOriginPart), req, nil); err != nil {
		t.Fatalf("dropFronts = %v", err)
	}
	if !slices.Contains(registry.front.torn, environment.TierProduction) {
		t.Errorf("removing %q tore down %v, and un-stamping a part whose address, forwarding rule and maps are still provisioned bills "+
			"for a front nothing will ever take down again", cloudflareOriginPart, registry.front.torn)
	}
}

func TestAPartWhoseFrontRefusedToComeDownStaysStamped(t *testing.T) {
	t.Parallel()

	b, registry := fronting(t)
	registry.front.refusal = refusal.Refuse(refusal.CodeInvalid, "shop.example.com is still bound")
	req := provider.BootstrapRequest{Tier: environment.TierProduction, Remove: []string{cloudflareOriginPart}}

	if err := b.dropFronts(context.Background(), surveyed(cloudflareOriginPart), req, nil); err == nil {
		t.Fatal("dropFronts = nil though the front refused, and the apply would go on to un-stamp a part that is still installed")
	}
	if installed := installedParts(b.Parts(), []string{cloudflareOriginPart}, req); slices.Contains(installed, cloudflareOriginPart) {
		t.Errorf("the stamp would still name %v after a removal, which is only correct because the apply stops on the refusal above", installed)
	}
}

func TestRemovingThePartIsRefusedWhileAHostnameIsStillBoundToItsFront(t *testing.T) {
	t.Parallel()

	b, registry := fronting(t)
	registry.front.bound = []string{"shop.example.com"}
	req := provider.BootstrapRequest{Tier: environment.TierProduction, Remove: []string{cloudflareOriginPart}}

	var refused refusal.Refusal
	err := b.dropFronts(context.Background(), surveyed(cloudflareOriginPart), req, nil)
	if !errors.As(err, &refused) || !strings.Contains(refused.Message, "shop.example.com") {
		t.Fatalf("dropFronts with a hostname still bound = %v, want a refusal naming it", err)
	}
	if len(registry.front.torn) != 0 {
		t.Errorf("the refused removal tore down %v: a certificate map with entries cannot be deleted, so the destroy fails partway",
			registry.front.torn)
	}
}

func TestAPartNothingInstalledIsNotTornDownOnRemoval(t *testing.T) {
	t.Parallel()

	b, registry := fronting(t)
	req := provider.BootstrapRequest{Tier: environment.TierProduction, Remove: []string{cloudflareOriginPart}}
	if err := b.dropFronts(context.Background(), surveyed(), req, nil); err != nil {
		t.Fatalf("dropFronts = %v", err)
	}
	if len(registry.opened) != 0 {
		t.Errorf("removing a part the stamp never recorded opened %v, and there is no state sealed under a passphrase to read", registry.opened)
	}
}

func TestTheFrontsABootstrapRaisesComeFromWhatItsPartsDeclareTheyNeed(t *testing.T) {
	t.Parallel()

	b, registry := fronting(t)
	req := provider.BootstrapRequest{Tier: environment.TierProduction, Parts: []string{cloudflareOriginPart}}
	if err := b.raiseFronts(context.Background(), req, nil); err != nil {
		t.Fatalf("raiseFronts = %v", err)
	}
	if !slices.Equal(registry.opened, []edge.Kind{"cloudflare"}) {
		t.Errorf("the bootstrap opened %v, want the edge the part's Needs name: a second edge with a front of its own must not need a branch here",
			registry.opened)
	}
}

func TestAFrontThatReportsNothingIsInstalledAndOwnsNoHostname(t *testing.T) {
	t.Parallel()

	b, registry := fronting(t)
	registry.front.silent = true
	registry.front.bound = []string{"shop.example.com"}

	installed, err := b.frontInstalled(context.Background(), environment.TierProduction, cloudflareOriginPart)
	if err != nil {
		t.Fatalf("frontInstalled = %v", err)
	}
	if !installed {
		t.Error("a front that reports nothing about its bootstrap is taken for gone, so the stamp alone can no longer say the part is installed")
	}
	if err := b.frontsFree(context.Background(), environment.TierProduction, []string{cloudflareOriginPart}); err != nil {
		t.Errorf("frontsFree = %v, want nothing owned by a front that names no bound hostname", err)
	}
}

func TestInstallingAndDroppingAFrontSaysWhichPartAndEdgeForWhichTier(t *testing.T) {
	t.Parallel()

	b, _ := fronting(t)
	progress := &fake.Log{}
	raising := provider.BootstrapRequest{Tier: environment.TierProduction, Parts: []string{cloudflareOriginPart}}
	if err := b.raiseFronts(context.Background(), raising, progress); err != nil {
		t.Fatalf("raiseFronts = %v", err)
	}
	dropping := provider.BootstrapRequest{Tier: environment.TierPreview, Remove: []string{cloudflareOriginPart}}
	if err := b.dropFronts(context.Background(), surveyed(cloudflareOriginPart), dropping, progress); err != nil {
		t.Fatalf("dropFronts = %v", err)
	}

	want := []string{
		"INFO Adding the cloudflare-origin part of the production bootstrap: " + b.Parts()[0].Summary,
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
		take func(b bootstrap) error
	}{
		{"dropFronts", func(b bootstrap) error {
			req := provider.BootstrapRequest{Tier: environment.TierProduction, Remove: []string{cloudflareOriginPart}}
			return b.dropFronts(context.Background(), surveyed(cloudflareOriginPart), req, nil)
		}},
		{"tearFronts", func(b bootstrap) error {
			return b.tearFronts(context.Background(), environment.TierProduction, []string{cloudflareOriginPart})
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			b, registry, h := frontingWithSecrets(t)
			registry.front.out = edge.BootstrapOutput{Offers: fullOffers("c1")}
			raising := provider.BootstrapRequest{Tier: environment.TierProduction, Parts: []string{cloudflareOriginPart}}
			if err := b.raiseFronts(context.Background(), raising, nil); err != nil {
				t.Fatalf("raiseFronts = %v", err)
			}
			kind := registry.front.Kind()
			credentials := h.clients.EdgeCredentialsSecret(environment.TierProduction, kind)
			seed := h.clients.PurgeSeedSecret(environment.TierProduction, kind)
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
	registry.front.out = edge.BootstrapOutput{Offers: fullOffers("c1")}
	if err := b.raiseFronts(context.Background(), provider.BootstrapRequest{Tier: environment.TierProduction, Parts: []string{cloudflareOriginPart}}, nil); err != nil {
		t.Fatal(err)
	}
	registry.front.refusal = refusal.Refuse(refusal.CodeInvalid, "still bound")
	if err := b.tearFronts(context.Background(), environment.TierProduction, []string{cloudflareOriginPart}); err == nil {
		t.Fatal("tearFronts = nil though the front refused")
	}
	if !h.secrets.has(h.clients.PurgeSeedSecret(environment.TierProduction, registry.front.Kind())) {
		t.Error("the seed went before the edge did, though its workers still run")
	}
}

func adoptedAs(t *testing.T, h *offersHarness, kind edge.Kind) (adoptedEdge, bool) {
	t.Helper()
	entry, err := h.records.Read(context.Background(), adoptedEdgeKey(environment.TierProduction, kind))
	if errors.Is(err, records.ErrNotFound) {
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

func TestABootstrapPlanNamesWhatTheEdgesFrontInstallsAndWhatRemovingItTakes(t *testing.T) {
	t.Parallel()

	b, registry := fronting(t)
	registry.front.plan = []edge.PlanChange{{Kind: "Cloudflare::Worker", Name: "ocel-releases-store", Action: edge.PlanCreate}}

	installs, err := b.plannedFronts(context.Background(), environment.TierProduction, []string{cloudflareOriginPart})
	if err != nil {
		t.Fatal(err)
	}
	removes, err := b.removedFronts(context.Background(), environment.TierProduction, []string{cloudflareOriginPart})
	if err != nil {
		t.Fatal(err)
	}

	for what, groups := range map[string][]provider.ChangeGroup{"installs": installs, "removes": removes} {
		if len(groups) != 1 || groups[0].Name != edge.EdgeGroupName(alb.Kind) || len(groups[0].Changes) != 1 || groups[0].Changes[0].Name != "ocel-releases-store" {
			t.Errorf("%s = %+v, want one edge group carrying the front's planned worker", what, groups)
		}
	}
	if removes[0].Action != provider.ActionDelete {
		t.Errorf("the removal group's action = %q, want delete", removes[0].Action)
	}
}

func TestTheLoadBalancerBehindTheALBEdgeIsAPartNoBootstrapRequires(t *testing.T) {
	t.Parallel()

	offered := bootstrap{}.Parts()
	for _, kind := range []edge.Kind{alb.Kind, edge.None} {
		required := bootstrapplan.RequiredParts(offered, kind)
		if len(required) != 0 {
			t.Errorf("a bootstrap fronted by %q requires %v, want nothing: the first deploy behind the alb adds its load balancer", kind, required)
		}
	}
	b := bootstrap{clients: &gcloud.Clients{Names: names.New("ocel", "acme-prod"), Region: "europe-west1"}}
	balancer := b.albPart(environment.TierProduction)
	if !strings.Contains(balancer.summary, "$18") || !balancer.retires {
		t.Errorf("the %s part is %+v, want its summary to say the price and it retiring with its last project", albPart, balancer)
	}
	for _, want := range []string{"gate", "plugin", "repository"} {
		if !strings.Contains(balancer.summary, want) {
			t.Errorf("the %s part reads %q, want it to mention the %s it holds", albPart, balancer.summary, want)
		}
	}
}

func TestTheShieldedLoadBalancerIsAPartOnlyTheCloudflareEdgePullsIn(t *testing.T) {
	t.Parallel()

	offered := bootstrap{}.Parts()
	at := slices.IndexFunc(offered, func(f provider.Part) bool { return f.Name == "cloudflare-origin" })
	if at < 0 {
		t.Fatalf("Parts() = %v, want the %q part: the load balancer Cloudflare forwards to costs every month, and a recurring cost is consented to by being planned", offered, cloudflareOriginPart)
	}
	if !strings.Contains(offered[at].Summary, "$18") {
		t.Errorf("the %q part reads %q, want the price in the plan", cloudflareOriginPart, offered[at].Summary)
	}
	proxied := bootstrapplan.RequiredParts(offered, "cloudflare")
	if !slices.Contains(proxied, cloudflareOriginPart) {
		t.Errorf("a cloudflare bootstrap requires %v, want %q and not the front browsers reach directly", proxied, cloudflareOriginPart)
	}
	fronted := bootstrapplan.RequiredParts(offered, alb.Kind)
	if slices.Contains(fronted, cloudflareOriginPart) {
		t.Errorf("an %q bootstrap requires %v, and a load balancer only Cloudflare reaches serves nothing it binds", alb.Kind, fronted)
	}

	for _, api := range []string{"compute.googleapis.com", "certificatemanager.googleapis.com", "networksecurity.googleapis.com"} {
		if !slices.Contains(apisFor(environment.TierProduction, []string{cloudflareOriginPart}), api) {
			t.Errorf("a %q bootstrap checks %v, want %s among them", cloudflareOriginPart, apisFor(environment.TierProduction, []string{cloudflareOriginPart}), api)
		}
	}
	if !slices.Contains(permissionsFor([]string{cloudflareOriginPart}), "networksecurity.serverTlsPolicies.create") {
		t.Errorf("a %q bootstrap checks %v, want the permission its server tls policy is raised with", cloudflareOriginPart, permissionsFor([]string{cloudflareOriginPart}))
	}
	if !slices.Contains(rolesCovering([]string{cloudflareOriginPart}), "roles/compute.securityAdmin") {
		t.Errorf("the refusal for a %q bootstrap names %v, want the role that covers its server tls policy", cloudflareOriginPart, rolesCovering([]string{cloudflareOriginPart}))
	}
}

func TestTheServicesTheLoadBalancerNeedsAreOnlyDemandedWhenItIsBeingAdded(t *testing.T) {
	t.Parallel()

	b := bootstrap{clients: &gcloud.Clients{Names: names.New("ocel", "acme-prod"), Region: "europe-west1"}}
	balancer := b.albPart(environment.TierPreview)
	for _, api := range []string{"compute.googleapis.com", "certificatemanager.googleapis.com", "networkservices.googleapis.com"} {
		if !slices.Contains(balancer.apis, api) {
			t.Errorf("the %s part checks %v, want %s among them: the gate is a Service Extensions plugin, and the front needs the rest", albPart, balancer.apis, api)
		}
	}
	plain := apisFor(environment.TierProduction, nil)
	for _, api := range []string{"compute.googleapis.com", "certificatemanager.googleapis.com"} {
		if slices.Contains(plain, api) {
			t.Errorf("a bootstrap provisioning no load balancer demands %s, and it would be refused for a service it never calls", api)
		}
	}
	for _, api := range BootstrapAPIs {
		if !slices.Contains(plain, api) {
			t.Errorf("a bootstrap no longer checks %s, and the apply would fail on the first resource that needs it", api)
		}
	}
	if slices.Contains(apisFor(environment.TierPreview, []string{cloudflareOriginPart}), "networkservices.googleapis.com") {
		t.Error("a bootstrap for the load balancer only Cloudflare reaches demands the Service Extensions API, and the worker in front of it gates")
	}
}

func TestABootstrapProvisioningNoLoadBalancerChecksNoLoadBalancerPermission(t *testing.T) {
	t.Parallel()

	for _, permission := range permissionsFor(nil) {
		if strings.HasPrefix(permission, "compute.") || strings.HasPrefix(permission, "certificatemanager.") {
			t.Errorf("a bootstrap provisioning no load balancer checks %s, and a credential would be refused for a service it never calls", permission)
		}
	}
	if slices.ContainsFunc(rolesCovering(nil), func(role string) bool {
		return role == "roles/compute.loadBalancerAdmin" || strings.HasPrefix(role, "roles/certificatemanager.")
	}) {
		t.Errorf("the refusal for a plain bootstrap names %v, and a role for a front nothing provisions is one more than the credential needs", rolesCovering(nil))
	}
}
