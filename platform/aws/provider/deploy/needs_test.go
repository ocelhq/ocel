package deploy

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/platform/aws/provider/edges/apigateway"
	"github.com/ocelhq/ocel/platform/aws/provider/edges/cloudfront"
	cloudflare "github.com/ocelhq/ocel/platform/edge/cloudflare/deploy"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

type verifyingEdge struct {
	*recordingEdge
	identity edge.CredentialIdentity
	err      error
	calls    int
}

func (v *verifyingEdge) VerifyCredentials(context.Context) (edge.CredentialIdentity, error) {
	v.calls++
	return v.identity, v.err
}

func writeNeeds(t *testing.T, artifactRoot, app string, needs map[edge.Need]edge.NeedDetail) {
	t.Helper()
	dir := appArtifactRoot(artifactRoot, app)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(edge.ServeDescriptor{Framework: frameworkNext, BuildID: "B1", Entry: "/", Needs: needs})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, edge.ServeDescriptorFile), raw, 0o644); err != nil {
		t.Fatal(err)
	}
}

func writeRawNeeds(t *testing.T, artifactRoot, app, needs string) {
	t.Helper()
	dir := appArtifactRoot(artifactRoot, app)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	body := `{"framework":"next","buildId":"B1","entry":"/","needs":` + needs + `}`
	if err := os.WriteFile(filepath.Join(dir, edge.ServeDescriptorFile), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func needsManifest(apps ...string) *contractv1.Manifest {
	m := &contractv1.Manifest{Slug: "shop"}
	for _, a := range apps {
		m.Apps = append(m.Apps, &contractv1.ManifestApp{Name: a, Framework: frameworkNext, DeploymentId: testDeploymentID})
	}
	return m
}

type recordedDegrade struct {
	need   edge.Need
	detail string
}

func degradeCollector() (*[]recordedDegrade, func(edge.Need, string)) {
	var seen []recordedDegrade
	return &seen, func(need edge.Need, detail string) {
		seen = append(seen, recordedDegrade{need: need, detail: detail})
	}
}

func TestCheckNeedsRefusesAnUnsupportedNeed(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	writeNeeds(t, root, "web", map[edge.Need]edge.NeedDetail{
		edge.NeedEdgeMiddleware: {Count: 2, Routes: []string{"/dashboard", "/admin"}, Matchers: []string{"/dashboard/:path*"}},
	})
	seen, degraded := degradeCollector()
	cfg := Config{Edge: &recordingEdge{kind: cloudfront.Kind}, ArtifactRoot: root, Degraded: degraded}

	_, err := checkNeeds(context.Background(), cfg, needsManifest("web"))
	if err == nil {
		t.Fatal("checkNeeds err = nil, want the unsupported need refused")
	}
	var refusal *UnsupportedNeedError
	if !errors.As(err, &refusal) {
		t.Fatalf("checkNeeds err = %T, want *UnsupportedNeedError", err)
	}
	if refusal.Need != edge.NeedEdgeMiddleware || refusal.App != "web" || refusal.Edge != cloudfront.Kind {
		t.Errorf("refusal = %+v, want it to name web, edge-middleware and the CloudFront edge", refusal)
	}
	for _, want := range []string{
		"edge-middleware",
		"next start",
		"/dashboard",
		"/admin",
		"\"edge-middleware\"",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("refusal = %q, want it to carry %q", err, want)
		}
	}
	if len(*seen) != 0 {
		t.Errorf("degraded fired %v on a refusal", *seen)
	}
}

func TestCheckNeedsRefusalNamesTheRoutesItHas(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	writeNeeds(t, root, "web", map[edge.Need]edge.NeedDetail{
		edge.NeedEdgeMiddleware: {Count: 3},
	})
	cfg := Config{Edge: &recordingEdge{kind: cloudfront.Kind}, ArtifactRoot: root}

	_, err := checkNeeds(context.Background(), cfg, needsManifest("web"))
	if err == nil {
		t.Fatal("checkNeeds err = nil, want the unsupported need refused")
	}
	if !strings.Contains(err.Error(), "3 routes") {
		t.Errorf("refusal = %q, want it to count the routes it cannot name", err)
	}
}

func TestCheckNeedsStreamsOneDegradedPerWaivedNeed(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	writeNeeds(t, root, "web", map[edge.Need]edge.NeedDetail{
		edge.NeedEdgeMiddleware: {Count: 1, Routes: []string{"/dashboard"}},
		edge.NeedPPRResume:      {Count: 1, Routes: []string{"/"}},
		edge.NeedStreaming:      {Count: 4},
	})
	seen, degraded := degradeCollector()
	cfg := Config{
		Edge:          &recordingEdge{kind: cloudfront.Kind},
		ArtifactRoot:  root,
		AllowDegraded: []string{"edge-middleware", "ppr-resume"},
		Degraded:      degraded,
	}

	if _, err := checkNeeds(context.Background(), cfg, needsManifest("web")); err != nil {
		t.Fatalf("checkNeeds err = %v, want the waived needs let through", err)
	}
	if len(*seen) != 2 {
		t.Fatalf("degraded fired %d times, want one per waived need: %+v", len(*seen), *seen)
	}
	if (*seen)[0].need != edge.NeedEdgeMiddleware || (*seen)[1].need != edge.NeedPPRResume {
		t.Errorf("degraded needs = %+v, want edge-middleware then ppr-resume", *seen)
	}
	if !strings.Contains((*seen)[0].detail, "next start") || !strings.Contains((*seen)[0].detail, "/dashboard") {
		t.Errorf("degraded detail = %q, want the degrade and the routes", (*seen)[0].detail)
	}
}

func TestCheckNeedsRefusesANeedNoEdgeKnows(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	writeRawNeeds(t, root, "web", `{"edge-telepathy":{"count":1}}`)
	cfg := Config{Edge: &recordingEdge{kind: cloudflare.Kind}, ArtifactRoot: root}

	_, err := checkNeeds(context.Background(), cfg, needsManifest("web"))
	if err == nil {
		t.Fatal("checkNeeds err = nil, want the unknown need refused")
	}
	var unknown *UnknownNeedError
	if !errors.As(err, &unknown) {
		t.Fatalf("checkNeeds err = %T, want *UnknownNeedError", err)
	}
	if !strings.Contains(err.Error(), "edge-telepathy") || !strings.Contains(err.Error(), "edge-middleware") {
		t.Errorf("refusal = %q, want it to name the need and the ones an app may declare", err)
	}
}

func TestCheckNeedsWaivingAnUnknownNeedStillRefuses(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	writeRawNeeds(t, root, "web", `{"edge-telepathy":{"count":1}}`)
	cfg := Config{Edge: &recordingEdge{kind: cloudflare.Kind}, ArtifactRoot: root, AllowDegraded: []string{"edge-telepathy"}}

	if _, err := checkNeeds(context.Background(), cfg, needsManifest("web")); err == nil {
		t.Fatal("checkNeeds err = nil, want a waiver not to invent a need")
	}
}

func TestCheckNeedsChecksEntitlementOnlyForAPresentCodeNeed(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name      string
		needs     map[edge.Need]edge.NeedDetail
		wantCalls int
		wantErr   bool
	}{
		{
			name:      "a code need asks the edge whether the account may run it",
			needs:     map[edge.Need]edge.NeedDetail{edge.NeedEdgeMiddleware: {Count: 1, Routes: []string{"/x"}}},
			wantCalls: 1,
			wantErr:   true,
		},
		{
			name:      "the edge runtime asks too",
			needs:     map[edge.Need]edge.NeedDetail{edge.NeedEdgeRuntime: {Count: 1, Routes: []string{"/x"}}},
			wantCalls: 1,
			wantErr:   true,
		},
		{
			name:      "needs that ship no code never ask",
			needs:     map[edge.Need]edge.NeedDetail{edge.NeedEdgeCache: {Count: 2}, edge.NeedStreaming: {Count: 1}},
			wantCalls: 0,
		},
		{
			name:      "an app declaring nothing never asks",
			needs:     map[edge.Need]edge.NeedDetail{},
			wantCalls: 0,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			root := t.TempDir()
			writeNeeds(t, root, "web", tc.needs)
			ed := &verifyingEdge{
				recordingEdge: &recordingEdge{kind: cloudflare.Kind},
				identity:      edge.CredentialIdentity{Account: "acct-1", Plan: "Workers Free", CodeEntitlement: edge.EntitlementWithheld},
			}
			cfg := Config{Edge: ed, ArtifactRoot: root}

			_, err := checkNeeds(context.Background(), cfg, needsManifest("web"))
			if ed.calls != tc.wantCalls {
				t.Errorf("VerifyCredentials called %d times, want %d", ed.calls, tc.wantCalls)
			}
			if tc.wantErr {
				var gap *EdgeEntitlementError
				if !errors.As(err, &gap) {
					t.Fatalf("checkNeeds err = %v (%T), want *EdgeEntitlementError", err, err)
				}
				if !strings.Contains(err.Error(), "Workers Free") || !strings.Contains(err.Error(), "allowDegraded") {
					t.Errorf("entitlement refusal = %q, want the plan and the waiver spelled out", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("checkNeeds err = %v, want none", err)
			}
		})
	}
}

func TestCheckNeedsPassesAGrantedEntitlement(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	writeNeeds(t, root, "web", map[edge.Need]edge.NeedDetail{edge.NeedEdgeMiddleware: {Count: 1}})
	ed := &verifyingEdge{
		recordingEdge: &recordingEdge{kind: cloudflare.Kind},
		identity:      edge.CredentialIdentity{Account: "acct-1", CodeEntitlement: edge.EntitlementGranted},
	}
	if _, err := checkNeeds(context.Background(), Config{Edge: ed, ArtifactRoot: root}, needsManifest("web")); err != nil {
		t.Fatalf("checkNeeds err = %v, want an entitled account through", err)
	}
}

func TestCheckNeedsSaysNothingForAnAppThatDeclaresNoNeeds(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	seen, degraded := degradeCollector()
	cfg := Config{Edge: &recordingEdge{kind: apigateway.Kind}, ArtifactRoot: root, Degraded: degraded}

	if _, err := checkNeeds(context.Background(), cfg, needsManifest("api")); err != nil {
		t.Fatalf("checkNeeds err = %v, want an app with no serve descriptor untouched", err)
	}
	if len(*seen) != 0 {
		t.Errorf("degraded fired %v for an app that declares nothing", *seen)
	}
}

func TestRealizeCompareRefusesBeforeItMutatesAnything(t *testing.T) {
	t.Parallel()

	current := edge.StoreSchemaVersion
	root := t.TempDir()
	writeNeeds(t, root, "web", map[edge.Need]edge.NeedDetail{
		edge.NeedEdgeMiddleware: {Count: 1, Routes: []string{"/dashboard"}},
	})
	ed := &recordingEdge{kind: cloudfront.Kind, storeSchemaVersion: &current}
	up := &fakeUploader{exists: map[string]bool{}}
	cfg := Config{
		Edge:           ed,
		StoreEndpoint:  fakeStoreEndpoint,
		Slug:           "shop",
		ArtifactRoot:   root,
		ArtifactBucket: "artifacts",
		AssetBucket:    "assets",
		Uploader:       up,
	}

	_, err := realize(context.Background(), cfg, &Realized{}, needsManifest("web"), nil, nil)
	if err == nil {
		t.Fatal("realize err = nil, want the unsupported need refused")
	}
	var refusal *UnsupportedNeedError
	if !errors.As(err, &refusal) {
		t.Fatalf("realize err = %v (%T), want *UnsupportedNeedError", err, err)
	}
	if n := len(up.puts); n != 0 {
		t.Errorf("the refused deploy uploaded %d objects: %v", n, up.puts)
	}
	if len(ed.reconciles) != 0 || len(ed.staged) != 0 || len(ed.deployed) != 0 {
		t.Errorf("the refused deploy touched the edge: reconciles=%d staged=%d deployed=%d",
			len(ed.reconciles), len(ed.staged), len(ed.deployed))
	}
}

func TestBuildDeploymentRecordCarriesTheNeedsAndWhatBecameOfThem(t *testing.T) {
	t.Parallel()

	root := writeTree(t, map[string]string{"apps/web/routing-manifest.json": `{"buildId":"WEB1"}`})
	writeNeeds(t, root, "web", map[edge.Need]edge.NeedDetail{
		edge.NeedEdgeMiddleware: {Count: 1, Routes: []string{"/dashboard"}},
		edge.NeedEdgeCache:      {Count: 2},
		edge.NeedStreaming:      {Count: 3},
	})
	manifest := &contractv1.Manifest{
		Slug:      "proj",
		Apps:      []*contractv1.ManifestApp{{Name: "web", Framework: frameworkNext}},
		Functions: []*contractv1.ManifestFunction{{LogicalName: "web_index", Framework: frameworkNext, App: "web", RouteId: "/"}},
	}
	cfg := Config{
		ArtifactRoot:  root,
		Slug:          "proj",
		Edge:          &recordingEdge{kind: cloudfront.Kind},
		OriginSecret:  testOriginSecret,
		AllowDegraded: []string{"edge-middleware"},
	}

	records, err := checkNeeds(context.Background(), cfg, manifest)
	if err != nil {
		t.Fatalf("checkNeeds err = %v, want the waived need through", err)
	}

	record, err := buildDeploymentRecord(cfg, records, manifest, manifest.GetApps()[0], deployedAs("WEB1"), nil, appBuildsFor(t, cfg, manifest), nil)
	if err != nil {
		t.Fatalf("buildDeploymentRecord: %v", err)
	}
	wantNeeds := []edge.Need{edge.NeedEdgeMiddleware, edge.NeedEdgeCache, edge.NeedStreaming}
	if !slicesEqualNeeds(record.Needs, wantNeeds) {
		t.Errorf("Needs = %v, want %v", record.Needs, wantNeeds)
	}
	if !slicesEqualNeeds(record.SupportInEffect, []edge.Need{edge.NeedEdgeCache, edge.NeedStreaming}) {
		t.Errorf("SupportInEffect = %v, want the two the CloudFront edge serves", record.SupportInEffect)
	}
	if !slicesEqualNeeds(record.Waived, []edge.Need{edge.NeedEdgeMiddleware}) {
		t.Errorf("Waived = %v, want edge-middleware", record.Waived)
	}
}

func TestBuildDeploymentRecordLeavesNeedsOutForAnAppWithNone(t *testing.T) {
	t.Parallel()

	root := writeTree(t, map[string]string{"apps/api/routing-manifest.json": `{"buildId":"API1"}`})
	manifest := &contractv1.Manifest{
		Slug:      "proj",
		Apps:      []*contractv1.ManifestApp{{Name: "api", Framework: "express"}},
		Functions: []*contractv1.ManifestFunction{{LogicalName: "api_index", Framework: "express", App: "api", RouteId: "/"}},
	}
	cfg := Config{ArtifactRoot: root, Slug: "proj", Edge: &recordingEdge{kind: cloudflare.Kind}}

	record, err := buildDeploymentRecord(cfg, nil, manifest, manifest.GetApps()[0], deployedAs("API1"), nil, appBuildsFor(t, cfg, manifest), nil)
	if err != nil {
		t.Fatalf("buildDeploymentRecord: %v", err)
	}
	encoded, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"needs", "supportInEffect", "waived"} {
		if strings.Contains(string(encoded), `"`+field+`"`) {
			t.Errorf("record carries %q for an app that declares no needs: %s", field, encoded)
		}
	}
}

func slicesEqualNeeds(got, want []edge.Need) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

func TestStagedRecordCarriesTheNeedsAndWhatBecameOfThem(t *testing.T) {
	t.Parallel()

	root := writeTree(t, map[string]string{"apps/web/routing-manifest.json": `{"buildId":"WEB1"}`})
	writeNeeds(t, root, "web", map[edge.Need]edge.NeedDetail{
		edge.NeedEdgeMiddleware: {Count: 1, Routes: []string{"/dashboard"}},
		edge.NeedEdgeCache:      {Count: 2},
	})
	manifest := &contractv1.Manifest{
		Slug:      "proj",
		Apps:      []*contractv1.ManifestApp{{Name: "web", Framework: frameworkNext, DeploymentId: testDeploymentID}},
		Functions: []*contractv1.ManifestFunction{{LogicalName: "web_index", Framework: frameworkNext, App: "web", RouteId: "/"}},
	}
	ed := &recordingEdge{kind: cloudfront.Kind}
	cfg := Config{
		ArtifactRoot:  root,
		Slug:          "proj",
		AssetBucket:   "assets",
		Uploader:      &fakeUploader{exists: map[string]bool{}},
		Edge:          ed,
		OriginSecret:  testOriginSecret,
		AllowDegraded: []string{"edge-middleware"},
	}

	records, err := checkNeeds(context.Background(), cfg, manifest)
	if err != nil {
		t.Fatalf("checkNeeds err = %v, want the waived need through", err)
	}
	record, err := buildDeploymentRecord(cfg, records, manifest, manifest.GetApps()[0], deployedAs("WEB1"), nil, appBuildsFor(t, cfg, manifest), nil)
	if err != nil {
		t.Fatalf("buildDeploymentRecord: %v", err)
	}
	stack := ed.reconciled(t, edge.StackSpec{Version: "v1"})
	if _, err := stageAndPromote(context.Background(), cfg, stack, "promo1", "", "", 100,
		[]appDeployResult{{App: "web", Identity: deployedAs("WEB1"), Record: record}}); err != nil {
		t.Fatalf("stageAndPromote: %v", err)
	}

	if len(ed.staged) != 1 {
		t.Fatalf("staged %d records, want 1", len(ed.staged))
	}
	staged := ed.staged[0]
	if !slicesEqualNeeds(staged.Needs, []edge.Need{edge.NeedEdgeMiddleware, edge.NeedEdgeCache}) {
		t.Errorf("staged Needs = %v, want the two the app declared", staged.Needs)
	}
	if !slicesEqualNeeds(staged.SupportInEffect, []edge.Need{edge.NeedEdgeCache}) {
		t.Errorf("staged SupportInEffect = %v, want edge-cache", staged.SupportInEffect)
	}
	if !slicesEqualNeeds(staged.Waived, []edge.Need{edge.NeedEdgeMiddleware}) {
		t.Errorf("staged Waived = %v, want edge-middleware", staged.Waived)
	}
	encoded, err := json.Marshal(staged)
	if err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{`"needs"`, `"supportInEffect"`, `"waived"`} {
		if !strings.Contains(string(encoded), field) {
			t.Errorf("the ledger body omits %s: %s", field, encoded)
		}
	}
}

func TestEveryNeedSpellsOutItsDegrade(t *testing.T) {
	t.Parallel()

	for _, need := range edge.AllNeeds() {
		if degradeOf[need] == "" {
			t.Errorf("need %q has no degrade spelled out; a deploy cannot say what running it on the origin costs", need)
		}
	}
}

func TestCheckNeedsRefusalNamesTheMatchersWhenItHasNoRoutes(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	writeNeeds(t, root, "web", map[edge.Need]edge.NeedDetail{
		edge.NeedEdgeMiddleware: {Count: 7, Matchers: []string{"/dashboard/:path*", "/admin/:path*"}},
	})
	cfg := Config{Edge: &recordingEdge{kind: cloudfront.Kind}, ArtifactRoot: root}

	_, err := checkNeeds(context.Background(), cfg, needsManifest("web"))
	if err == nil {
		t.Fatal("checkNeeds err = nil, want the unsupported need refused")
	}
	if !strings.Contains(err.Error(), "the routes matching /dashboard/:path*, /admin/:path*") {
		t.Errorf("refusal = %q, want it to name the matchers it has instead of the routes it does not", err)
	}
}

func TestCheckNeedsRefusesWhenItCannotConfirmTheEntitlement(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	writeNeeds(t, root, "web", map[edge.Need]edge.NeedDetail{edge.NeedEdgeMiddleware: {Count: 1, Routes: []string{"/x"}}})
	ed := &verifyingEdge{recordingEdge: &recordingEdge{kind: cloudflare.Kind}, err: errors.New("token was rejected")}
	cfg := Config{Edge: ed, ArtifactRoot: root, AllowDegraded: []string{"edge-middleware"}}

	_, err := checkNeeds(context.Background(), cfg, needsManifest("web"))
	var gap *EdgeEntitlementError
	if !errors.As(err, &gap) {
		t.Fatalf("checkNeeds err = %v (%T), want *EdgeEntitlementError", err, err)
	}
	if !strings.Contains(err.Error(), "could not confirm the account may run it") || !strings.Contains(err.Error(), "token was rejected") {
		t.Errorf("refusal = %q, want it to say it could not confirm, and why", err)
	}
	if !errors.Is(err, ed.err) {
		t.Errorf("refusal does not unwrap to the verification error")
	}
}

func TestCheckNeedsDegradesACodeNeedTheEntitlementWithholds(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name    string
		waived  []string
		wantErr bool
	}{
		{name: "a waived code need deploys degraded", waived: []string{"edge-middleware"}},
		{name: "an unwaived code need is refused", wantErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			root := t.TempDir()
			writeNeeds(t, root, "web", map[edge.Need]edge.NeedDetail{
				edge.NeedEdgeMiddleware: {Count: 1, Routes: []string{"/dashboard"}},
			})
			ed := &verifyingEdge{
				recordingEdge: &recordingEdge{kind: cloudflare.Kind},
				identity:      edge.CredentialIdentity{Account: "acct-1", Plan: "Workers Free", CodeEntitlement: edge.EntitlementWithheld},
			}
			seen, degraded := degradeCollector()
			cfg := Config{Edge: ed, ArtifactRoot: root, AllowDegraded: tc.waived, Degraded: degraded}

			records, err := checkNeeds(context.Background(), cfg, needsManifest("web"))
			if tc.wantErr {
				var gap *EdgeEntitlementError
				if !errors.As(err, &gap) {
					t.Fatalf("checkNeeds err = %v (%T), want *EdgeEntitlementError", err, err)
				}
				if !strings.Contains(err.Error(), "the Workers Free plan") || !strings.Contains(err.Error(), `"edge-middleware"`) {
					t.Errorf("refusal = %q, want the plan and the waiver to add", err)
				}
				if len(*seen) != 0 {
					t.Errorf("degraded fired %v on a refusal", *seen)
				}
				return
			}
			if err != nil {
				t.Fatalf("checkNeeds err = %v, want the waiver to let the withheld need through", err)
			}
			if len(*seen) != 1 || (*seen)[0].need != edge.NeedEdgeMiddleware {
				t.Fatalf("degraded = %+v, want the one waived need", *seen)
			}
			_, inEffect, waived := records.forApp("web")
			if len(inEffect) != 0 {
				t.Errorf("SupportInEffect = %v, want nothing in effect on a plan that runs no code at the edge", inEffect)
			}
			if !slicesEqualNeeds(waived, []edge.Need{edge.NeedEdgeMiddleware}) {
				t.Errorf("Waived = %v, want edge-middleware", waived)
			}
		})
	}
}

func TestStagedRecordWaivesTheNeedThePlanWithholds(t *testing.T) {
	t.Parallel()

	root := writeTree(t, map[string]string{"apps/web/routing-manifest.json": `{"buildId":"WEB1"}`})
	writeNeeds(t, root, "web", map[edge.Need]edge.NeedDetail{
		edge.NeedEdgeMiddleware: {Count: 1, Routes: []string{"/dashboard"}},
		edge.NeedEdgeCache:      {Count: 2},
	})
	manifest := &contractv1.Manifest{
		Slug:      "proj",
		Apps:      []*contractv1.ManifestApp{{Name: "web", Framework: frameworkNext, DeploymentId: testDeploymentID}},
		Functions: []*contractv1.ManifestFunction{{LogicalName: "web_index", Framework: frameworkNext, App: "web", RouteId: "/"}},
	}
	ed := &verifyingEdge{
		recordingEdge: &recordingEdge{kind: cloudflare.Kind},
		identity:      edge.CredentialIdentity{Account: "acct-1", Plan: "Workers Free", CodeEntitlement: edge.EntitlementWithheld},
	}
	cfg := Config{
		ArtifactRoot:  root,
		Slug:          "proj",
		AssetBucket:   "assets",
		Uploader:      &fakeUploader{exists: map[string]bool{}},
		Edge:          ed,
		AllowDegraded: []string{"edge-middleware"},
	}

	records, err := checkNeeds(context.Background(), cfg, manifest)
	if err != nil {
		t.Fatalf("checkNeeds err = %v, want the waived need through", err)
	}
	record, err := buildDeploymentRecord(cfg, records, manifest, manifest.GetApps()[0], deployedAs("WEB1"), nil, appBuildsFor(t, cfg, manifest), nil)
	if err != nil {
		t.Fatalf("buildDeploymentRecord: %v", err)
	}
	stack := ed.reconciled(t, edge.StackSpec{Version: "v1"})
	if _, err := stageAndPromote(context.Background(), cfg, stack, "promo1", "", "", 100,
		[]appDeployResult{{App: "web", Identity: deployedAs("WEB1"), Record: record}}); err != nil {
		t.Fatalf("stageAndPromote: %v", err)
	}

	if len(ed.staged) != 1 {
		t.Fatalf("staged %d records, want 1", len(ed.staged))
	}
	staged := ed.staged[0]
	if !slicesEqualNeeds(staged.SupportInEffect, []edge.Need{edge.NeedEdgeCache}) {
		t.Errorf("staged SupportInEffect = %v, want the record not to claim an edge that serves middleware it cannot run", staged.SupportInEffect)
	}
	if !slicesEqualNeeds(staged.Waived, []edge.Need{edge.NeedEdgeMiddleware}) {
		t.Errorf("staged Waived = %v, want edge-middleware", staged.Waived)
	}
}
