package deploy

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/ocelhq/ocel/cloud/edge"
	deploymentsv1 "github.com/ocelhq/ocel/pkg/proto/deployments/v1"
)

// setStoreWorkerBundle writes a deployments-store worker bundle and exports
// the manifest pointing Cloudflare at it, standing in for the npm launcher
// (mirrors edgeworker_test.go's setWorkerBundle for the generic worker).
func setStoreWorkerBundle(t *testing.T) {
	t.Helper()
	bundle := filepath.Join(t.TempDir(), "index.js")
	if err := os.WriteFile(bundle, []byte("export default {}"), 0o644); err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(edge.StoreBundleManifest{edge.KindCloudflare: bundle})
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv(edge.EnvStoreWorkerBundles, string(raw))
}

// TestRootStackSpecs_ThreadsEdgeValues guards ocelhq-f0e: the generic worker's
// OCEL_CACHE_STORE R2 binding degrades to its no-store fallback in every
// production deploy unless the bootstrap values carrying the cache bucket
// name reach RootStackSpec, exactly like they already reach AppDeployment.Values
// for a preview deploy (edgeworker.go's Values: cfg.EdgeValues).
func TestRootStackSpecs_ThreadsEdgeValues(t *testing.T) {
	setWorkerBundle(t)
	setStoreWorkerBundle(t)
	cfg := Config{Edge: &recordingEdge{}, EdgeValues: map[string]string{"cacheBucket": "ocel-proj-cache"}}

	t.Run("no worker-fronted apps", func(t *testing.T) {
		manifest := &deploymentsv1.Manifest{ProjectId: "proj"}
		specs, err := rootStackSpecs(cfg, manifest, "v1")
		if err != nil {
			t.Fatalf("rootStackSpecs: %v", err)
		}
		if len(specs) != 1 {
			t.Fatalf("specs = %d, want 1", len(specs))
		}
		if specs[0].Values["cacheBucket"] != "ocel-proj-cache" {
			t.Errorf("specs[0].Values = %v, want cacheBucket=ocel-proj-cache", specs[0].Values)
		}
	})

	t.Run("with a worker-fronted app", func(t *testing.T) {
		manifest := &deploymentsv1.Manifest{
			ProjectId: "proj",
			Apps:      []*deploymentsv1.ManifestApp{{Name: "web", Framework: "next"}},
			Functions: []*deploymentsv1.ManifestFunction{{LogicalName: "web_index", Framework: "next", App: "web", RouteId: "/"}},
		}
		specs, err := rootStackSpecs(cfg, manifest, "v1")
		if err != nil {
			t.Fatalf("rootStackSpecs: %v", err)
		}
		if len(specs) != 1 {
			t.Fatalf("specs = %d, want 1", len(specs))
		}
		if specs[0].Values["cacheBucket"] != "ocel-proj-cache" {
			t.Errorf("specs[0].Values = %v, want cacheBucket=ocel-proj-cache", specs[0].Values)
		}
	})
}

func TestPreviewBaseDomain(t *testing.T) {
	cases := map[string]string{
		"*.preview.acme.com": "preview.acme.com",
		"*.acme.com":         "acme.com",
		"acme.com":           "",
		"":                   "",
	}
	for in, want := range cases {
		if got := previewBaseDomain(in); got != want {
			t.Errorf("previewBaseDomain(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestPreviewGenericName_DistinctFromProduction(t *testing.T) {
	prod := workerScriptName("proj-production", "web")
	preview := previewGenericName("proj", "web")
	if prod == preview {
		t.Fatalf("preview root worker name %q collides with production", preview)
	}
	if want := "ocel-proj-preview-web"; preview != want {
		t.Errorf("previewGenericName = %q, want %q", preview, want)
	}
}

func TestRootStackSpecs_PreviewCarriesPreviewVarsAndWildcardDomain(t *testing.T) {
	setWorkerBundle(t)
	setStoreWorkerBundle(t)
	manifest := &deploymentsv1.Manifest{
		ProjectId: "proj",
		Apps:      []*deploymentsv1.ManifestApp{{Name: "web", Framework: "next"}},
		Functions: []*deploymentsv1.ManifestFunction{{LogicalName: "web_index", Framework: "next", App: "web", RouteId: "/"}},
		Domains:   map[string]string{"preview": "*.preview.acme.com"},
	}
	cfg := Config{
		Edge:  &recordingEdge{},
		Slug:  "proj",
		Class: deploymentsv1.Environment_CLASS_PREVIEW,
	}

	specs, err := rootStackSpecs(cfg, manifest, "v1")
	if err != nil {
		t.Fatalf("rootStackSpecs: %v", err)
	}
	if len(specs) != 1 {
		t.Fatalf("specs = %d, want 1", len(specs))
	}
	spec := specs[0]
	if spec.GenericName != "ocel-proj-preview-web" {
		t.Errorf("GenericName = %q, want ocel-proj-preview-web", spec.GenericName)
	}
	if spec.Domain != "*.preview.acme.com" {
		t.Errorf("Domain = %q, want the preview wildcard", spec.Domain)
	}
	if spec.Generic.Vars[envPreview] != "1" {
		t.Errorf("Vars[%s] = %q, want 1", envPreview, spec.Generic.Vars[envPreview])
	}
	if spec.Generic.Vars[envPreviewBaseDomain] != "preview.acme.com" {
		t.Errorf("Vars[%s] = %q, want preview.acme.com", envPreviewBaseDomain, spec.Generic.Vars[envPreviewBaseDomain])
	}
	if spec.Generic.Vars["OCEL_APP"] != "web" {
		t.Errorf("Vars[OCEL_APP] = %q, want web", spec.Generic.Vars["OCEL_APP"])
	}
}

// The generic worker is AWS_IAM-gated behind its Lambdas, so it must be handed
// the edge reader's key to sign forwards — the access key as a plain var, the
// secret key as a secret binding (never plaintext).
func TestRootStackSpecs_BindsEdgeSigningCredentials(t *testing.T) {
	setWorkerBundle(t)
	setStoreWorkerBundle(t)
	manifest := &deploymentsv1.Manifest{
		ProjectId: "proj",
		Apps:      []*deploymentsv1.ManifestApp{{Name: "web", Framework: "next"}},
		Functions: []*deploymentsv1.ManifestFunction{{LogicalName: "web_index", Framework: "next", App: "web", RouteId: "/"}},
	}

	t.Run("bound when the substrate has edge credentials", func(t *testing.T) {
		cfg := Config{Edge: &recordingEdge{}, EdgeAccessKeyID: "AKIAEDGE", EdgeSecretKey: "secret-edge"}
		specs, err := rootStackSpecs(cfg, manifest, "v1")
		if err != nil {
			t.Fatalf("rootStackSpecs: %v", err)
		}
		g := specs[0].Generic
		if g.Vars[edge.EdgeAccessKeyIDVar] != "AKIAEDGE" {
			t.Errorf("generic Vars[%s] = %q, want AKIAEDGE", edge.EdgeAccessKeyIDVar, g.Vars[edge.EdgeAccessKeyIDVar])
		}
		if g.Secrets[edge.EdgeSecretKeyVar] != "secret-edge" {
			t.Errorf("generic Secrets[%s] = %q, want secret-edge", edge.EdgeSecretKeyVar, g.Secrets[edge.EdgeSecretKeyVar])
		}
		if _, leaked := g.Vars[edge.EdgeSecretKeyVar]; leaked {
			t.Error("the signing secret must never appear in plain-text Vars")
		}
	})

	t.Run("absent on a substrate predating edge credentials", func(t *testing.T) {
		cfg := Config{Edge: &recordingEdge{}}
		specs, err := rootStackSpecs(cfg, manifest, "v1")
		if err != nil {
			t.Fatalf("rootStackSpecs: %v", err)
		}
		g := specs[0].Generic
		if _, ok := g.Vars[edge.EdgeAccessKeyIDVar]; ok {
			t.Error("no access-key var expected without edge credentials")
		}
		if g.Secrets[edge.EdgeSecretKeyVar] != "" {
			t.Error("no secret expected without edge credentials")
		}
	})
}

func TestFinalizeProductionDeploy_ReconcileThenStageThenPromoteInOrder(t *testing.T) {
	fake := &recordingRootStack{}
	ctx := context.Background()
	specs := []edge.RootStackSpec{{Version: "v1", GenericName: "web-generic"}}
	results := []appDeployResult{
		{App: "web", BuildID: "b1", Record: edge.DeploymentRecord{App: "web", BuildID: "b1"}},
		{App: "api", BuildID: "b2", Record: edge.DeploymentRecord{App: "api", BuildID: "b2"}},
	}

	state, err := finalizeDeploy(ctx, fake, specs, nil, "promo1", "", "", 100, results)
	if err != nil {
		t.Fatalf("finalizeDeploy: %v", err)
	}

	if len(fake.reconciles) != 1 {
		t.Fatalf("reconciles = %d, want 1", len(fake.reconciles))
	}
	if len(fake.staged) != 2 {
		t.Fatalf("staged = %d, want 2 (one per app)", len(fake.staged))
	}
	if len(fake.promotions) != 1 {
		t.Fatalf("promotions = %d, want 1", len(fake.promotions))
	}
	if fake.promotions[0].PromotionID != "promo1" {
		t.Errorf("promotion id = %q, want %q", fake.promotions[0].PromotionID, "promo1")
	}
	want := map[string]string{"web": "b1", "api": "b2"}
	if len(fake.promotions[0].Builds) != len(want) {
		t.Fatalf("promotion builds = %v, want %v", fake.promotions[0].Builds, want)
	}
	for app, buildID := range want {
		if got := fake.promotions[0].Builds[app]; got != buildID {
			t.Errorf("promotion.Builds[%q] = %q, want %q", app, got, buildID)
		}
	}
	if state[edge.RootStackKeyEndpoint] == "" {
		t.Error("expected a reconciled state to be returned")
	}
}

func TestFinalizeProductionDeploy_StampsTheTagOntoThePromotion(t *testing.T) {
	fake := &recordingRootStack{}
	ctx := context.Background()
	results := []appDeployResult{
		{App: "web", BuildID: "b1", Record: edge.DeploymentRecord{App: "web", BuildID: "b1"}},
	}

	if _, err := finalizeDeploy(ctx, fake, []edge.RootStackSpec{{Version: "v1"}}, nil, "promo1", "v1.2.3", "", 100, results); err != nil {
		t.Fatalf("finalizeDeploy: %v", err)
	}

	if len(fake.promotions) != 1 || fake.promotions[0].Tag != "v1.2.3" {
		t.Errorf("promotions = %v, want the promote to carry tag %q", fake.promotions, "v1.2.3")
	}
}

func TestFinalizeDeploy_PromotesTheGivenPointer(t *testing.T) {
	ctx := context.Background()
	results := []appDeployResult{
		{App: "web", BuildID: "b1", Record: edge.DeploymentRecord{App: "web", BuildID: "b1"}},
	}

	prod := &recordingRootStack{}
	if _, err := finalizeDeploy(ctx, prod, []edge.RootStackSpec{{Version: "v1"}}, nil, "promo1", "", "", 100, results); err != nil {
		t.Fatalf("finalizeDeploy(production): %v", err)
	}
	if len(prod.promotePointers) != 1 || prod.promotePointers[0] != "" {
		t.Errorf("production promote pointer = %v, want the reserved default (empty)", prod.promotePointers)
	}

	preview := &recordingRootStack{}
	if _, err := finalizeDeploy(ctx, preview, []edge.RootStackSpec{{Version: "v1"}}, nil, "promo1", "", "pr-42", 100, results); err != nil {
		t.Fatalf("finalizeDeploy(preview): %v", err)
	}
	if len(preview.promotePointers) != 1 || preview.promotePointers[0] != "pr-42" {
		t.Errorf("preview promote pointer = %v, want [pr-42]", preview.promotePointers)
	}
}

func TestPromotePointer_EmptyForProductionIdentityForPreview(t *testing.T) {
	if got := promotePointer(Config{Class: deploymentsv1.Environment_CLASS_PRODUCTION, Identity: "ignored"}); got != "" {
		t.Errorf("promotePointer(production) = %q, want empty", got)
	}
	if got := promotePointer(Config{Class: deploymentsv1.Environment_CLASS_PREVIEW, Identity: "pr-42"}); got != "pr-42" {
		t.Errorf("promotePointer(preview) = %q, want pr-42", got)
	}
}

func TestBootstrapCommand_ByClass(t *testing.T) {
	if got := bootstrapCommand(Config{Class: deploymentsv1.Environment_CLASS_PRODUCTION}); got != "ocel bootstrap" {
		t.Errorf("bootstrapCommand(production) = %q", got)
	}
	if got := bootstrapCommand(Config{Class: deploymentsv1.Environment_CLASS_PREVIEW}); got != "ocel bootstrap --preview" {
		t.Errorf("bootstrapCommand(preview) = %q", got)
	}
}

func TestValidateTag_RejectsMalformedTagsHostSide(t *testing.T) {
	if err := validateTag(""); err != nil {
		t.Errorf("validateTag(\"\") = %v, want nil (untagged default)", err)
	}
	if err := validateTag("v1.2.3"); err != nil {
		t.Errorf("validateTag(%q) = %v, want nil", "v1.2.3", err)
	}
	for _, bad := range []string{"feature/x", "has space", "über"} {
		if err := validateTag(bad); err == nil {
			t.Errorf("validateTag(%q) = nil, want an error", bad)
		}
	}
}

func TestCheckTagAvailable_RejectsATagAlreadyInUse(t *testing.T) {
	fake := &recordingRootStack{
		history: []edge.HistoryEntry{
			{Promotion: edge.Promotion{PromotionID: "promo-1", Tag: "v1.2.3"}, Active: true},
		},
	}
	ctx := context.Background()
	state, err := fake.ReconcileRootStack(ctx, edge.RootStackSpec{Version: "v1"}, nil)
	if err != nil {
		t.Fatalf("ReconcileRootStack: %v", err)
	}

	if err := checkTagAvailable(ctx, fake, state, "v1.2.3"); err == nil {
		t.Fatal("expected a duplicate tag to be rejected")
	}
}

func TestCheckTagAvailable_AllowsAFreshTag(t *testing.T) {
	fake := &recordingRootStack{
		history: []edge.HistoryEntry{
			{Promotion: edge.Promotion{PromotionID: "promo-1", Tag: "v1.2.3"}, Active: true},
		},
	}
	ctx := context.Background()
	state, err := fake.ReconcileRootStack(ctx, edge.RootStackSpec{Version: "v1"}, nil)
	if err != nil {
		t.Fatalf("ReconcileRootStack: %v", err)
	}

	if err := checkTagAvailable(ctx, fake, state, "v2.0.0"); err != nil {
		t.Errorf("checkTagAvailable rejected a fresh tag: %v", err)
	}
}

func TestCheckTagAvailable_NoOpForUntaggedOrFirstDeploy(t *testing.T) {
	fake := &recordingRootStack{
		history: []edge.HistoryEntry{
			{Promotion: edge.Promotion{PromotionID: "promo-1", Tag: "v1.2.3"}, Active: true},
		},
	}
	ctx := context.Background()

	// Untagged deploy: no check regardless of history.
	if err := checkTagAvailable(ctx, fake, edge.RootStackState{edge.RootStackKeyEndpoint: "http://store"}, ""); err != nil {
		t.Errorf("untagged deploy should never fail the tag check: %v", err)
	}
	// First-ever deploy: no store yet (no endpoint), so no history to read.
	if err := checkTagAvailable(ctx, fake, nil, "v1.2.3"); err != nil {
		t.Errorf("first deploy (no store) should never fail the tag check: %v", err)
	}
}

func TestFinalizeProductionDeploy_StagesBeforeAnyPromote(t *testing.T) {
	fake := &orderTrackingRootStack{recordingRootStack: &recordingRootStack{}}
	ctx := context.Background()
	results := []appDeployResult{
		{App: "web", BuildID: "b1", Record: edge.DeploymentRecord{App: "web", BuildID: "b1"}},
	}

	if _, err := finalizeDeploy(ctx, fake, []edge.RootStackSpec{{Version: "v1"}}, nil, "promo1", "", "", 100, results); err != nil {
		t.Fatalf("finalizeDeploy: %v", err)
	}

	want := []string{"reconcile", "stage", "promote"}
	if len(fake.calls) != len(want) {
		t.Fatalf("calls = %v, want %v", fake.calls, want)
	}
	for i, c := range want {
		if fake.calls[i] != c {
			t.Errorf("calls[%d] = %q, want %q (full sequence: %v)", i, fake.calls[i], c, fake.calls)
		}
	}
}

func TestFinalizeProductionDeploy_AppFailureAbortsPromote(t *testing.T) {
	fake := &recordingRootStack{}
	ctx := context.Background()
	results := []appDeployResult{
		{App: "web", BuildID: "b1", Record: edge.DeploymentRecord{App: "web", BuildID: "b1"}},
		{App: "api", Err: errors.New("app-deploy stack failed")},
	}

	_, err := finalizeDeploy(ctx, fake, []edge.RootStackSpec{{Version: "v1"}}, nil, "promo1", "", "", 100, results)
	if err == nil {
		t.Fatal("expected an error when one app's deploy failed")
	}

	if len(fake.staged) != 1 {
		t.Errorf("staged = %d, want 1 (only the successful app)", len(fake.staged))
	}
	if len(fake.promotions) != 0 {
		t.Errorf("promotions = %d, want 0: a failed app must abort the promote", len(fake.promotions))
	}
}

func TestFinalizeProductionDeploy_SecondDeployProducesNewPromotionRetainingPrior(t *testing.T) {
	fake := &recordingRootStack{}
	ctx := context.Background()
	specs := []edge.RootStackSpec{{Version: "v1"}}
	results := []appDeployResult{{App: "web", BuildID: "b1", Record: edge.DeploymentRecord{App: "web", BuildID: "b1"}}}

	state, err := finalizeDeploy(ctx, fake, specs, nil, "promo1", "", "", 100, results)
	if err != nil {
		t.Fatalf("first finalizeDeploy: %v", err)
	}

	results2 := []appDeployResult{{App: "web", BuildID: "b2", Record: edge.DeploymentRecord{App: "web", BuildID: "b2"}}}
	if _, err := finalizeDeploy(ctx, fake, specs, state, "promo2", "", "", 200, results2); err != nil {
		t.Fatalf("second finalizeDeploy: %v", err)
	}

	if len(fake.promotions) != 2 {
		t.Fatalf("promotions = %d, want 2 (both retained)", len(fake.promotions))
	}
	if fake.promotions[0].PromotionID != "promo1" || fake.promotions[1].PromotionID != "promo2" {
		t.Errorf("promotions = %+v, want promo1 then promo2", fake.promotions)
	}
}

// orderTrackingRootStack wraps recordingRootStack to additionally record the
// relative order of reconcile/stage/promote calls, which recordingRootStack's
// own per-kind slices cannot express on their own.
type orderTrackingRootStack struct {
	*recordingRootStack
	calls []string
}

func (f *orderTrackingRootStack) ReconcileRootStack(ctx context.Context, spec edge.RootStackSpec, prior edge.RootStackState) (edge.RootStackState, error) {
	f.calls = append(f.calls, "reconcile")
	return f.recordingRootStack.ReconcileRootStack(ctx, spec, prior)
}

func (f *orderTrackingRootStack) PutStaged(ctx context.Context, state edge.RootStackState, record edge.DeploymentRecord) error {
	f.calls = append(f.calls, "stage")
	return f.recordingRootStack.PutStaged(ctx, state, record)
}

func (f *orderTrackingRootStack) Promote(ctx context.Context, state edge.RootStackState, promotion edge.Promotion, pointer string) error {
	f.calls = append(f.calls, "promote")
	return f.recordingRootStack.Promote(ctx, state, promotion, pointer)
}

func TestReconcileRootStack_ThreadsStateAcrossMultipleSpecs(t *testing.T) {
	fake := &recordingRootStack{}
	ctx := context.Background()
	specs := []edge.RootStackSpec{
		{Version: "v1", GenericName: "web-generic"},
		{Version: "v1", GenericName: "admin-generic"},
	}

	state, err := reconcileRootStack(ctx, fake, specs, nil)
	if err != nil {
		t.Fatalf("reconcileRootStack: %v", err)
	}
	if len(fake.reconciles) != 2 {
		t.Fatalf("reconciles = %d, want 2 (one per spec)", len(fake.reconciles))
	}
	if state[edge.RootStackKeyEndpoint] == "" {
		t.Error("expected a non-empty reconciled state")
	}
}

func TestReconcileRootStack_NoSpecsReturnsPriorUnchanged(t *testing.T) {
	fake := &recordingRootStack{}
	ctx := context.Background()
	prior := edge.RootStackState{edge.RootStackKeyEndpoint: "https://prior"}

	state, err := reconcileRootStack(ctx, fake, nil, prior)
	if err != nil {
		t.Fatalf("reconcileRootStack: %v", err)
	}
	if len(fake.reconciles) != 0 {
		t.Errorf("reconciles = %d, want 0", len(fake.reconciles))
	}
	if state[edge.RootStackKeyEndpoint] != "https://prior" {
		t.Errorf("state = %v, want prior unchanged", state)
	}
}
