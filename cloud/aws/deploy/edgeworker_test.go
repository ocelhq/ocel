package deploy

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/cloud/edge"
	deploymentsv1 "github.com/ocelhq/ocel/pkg/proto/deployments/v1"
)

func fnOutput(logicalName, url string) *deploymentsv1.ResourceOutput {
	return &deploymentsv1.ResourceOutput{
		LogicalName: logicalName,
		Output: &deploymentsv1.ResourceOutput_Function{
			Function: &deploymentsv1.FunctionOutput{Url: url},
		},
	}
}

func TestSanitizeWorkerName(t *testing.T) {
	cases := map[string]string{
		"ocel-proj_ABC-prod": "ocel-proj-abc-prod",
		"ocel-Proj.123":      "ocel-proj-123",
		"--weird__name--":    "weird-name",
		"":                   "ocel-worker",
		"////":               "ocel-worker",
	}
	for in, want := range cases {
		if got := sanitizeWorkerName(in); got != want {
			t.Errorf("sanitizeWorkerName(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestSanitizeWorkerName_ClampsTo63Chars(t *testing.T) {
	long := strings.Repeat("a", 100)
	if got := sanitizeWorkerName(long); len(got) != 63 {
		t.Errorf("expected clamp to 63 chars, got %d", len(got))
	}
}

// recordingEdge captures every AppDeployment it is handed so orchestration can
// be asserted without touching any real edge API.
type recordingEdge struct {
	deployed []edge.AppDeployment
}

func (f *recordingEdge) Kind() edge.Kind { return edge.KindCloudflare }

func (f *recordingEdge) Bootstrap(context.Context, edge.Class) (edge.BootstrapOutput, error) {
	return edge.BootstrapOutput{Trust: edge.TrustExternal}, nil
}

func (f *recordingEdge) DeployApp(_ context.Context, app edge.AppDeployment) (edge.AppResult, error) {
	f.deployed = append(f.deployed, app)
	return edge.AppResult{URL: "https://" + app.Name + ".acme.workers.dev"}, nil
}

func (f *recordingEdge) called() bool { return len(f.deployed) > 0 }

// only returns the single deployment a one-app test expects.
func (f *recordingEdge) only(t *testing.T) edge.AppDeployment {
	t.Helper()
	if len(f.deployed) != 1 {
		t.Fatalf("expected exactly one deployment, got %d", len(f.deployed))
	}
	return f.deployed[0]
}

func (f *recordingEdge) names() []string {
	var names []string
	for _, d := range f.deployed {
		names = append(names, d.Name)
	}
	return names
}

// legacyEdge additionally answers whether a deployment exists, standing in for
// an edge that can report a worker left at the previous unqualified name.
type legacyEdge struct {
	recordingEdge
	existing map[string]bool
	asked    []string
}

func (l *legacyEdge) FindApp(_ context.Context, name string) (bool, error) {
	l.asked = append(l.asked, name)
	return l.existing[name], nil
}

// otherEdge stands in for a future provider-native edge no framework has
// registered a worker for.
type otherEdge struct{ recordingEdge }

func (o *otherEdge) Kind() edge.Kind { return "provider-native" }

func TestDeployEdgeWorker_FrameworkWithNoWorkerIsANoOp(t *testing.T) {
	fake := &recordingEdge{}
	manifest := &deploymentsv1.Manifest{
		Functions: []*deploymentsv1.ManifestFunction{
			{LogicalName: "web_api", Framework: "express"},
		},
	}

	out, err := deployEdgeWorker(context.Background(), Config{Edge: fake}, manifest, nil, nil)
	if err != nil {
		t.Fatalf("deployEdgeWorker: %v", err)
	}
	if fake.called() {
		t.Error("a framework registering no worker must not reach the edge")
	}
	if out != nil {
		t.Errorf("expected no outputs, got %v", out)
	}
}

func TestDeployEdgeWorker_AssemblesUploadAndReportsURL(t *testing.T) {
	artifactRoot := t.TempDir()
	appDir := writeRoutingManifest(t, artifactRoot, "web", `{"buildId":"b"}`)
	if err := os.MkdirAll(filepath.Join(appDir, "static"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(appDir, "static", "next.svg"), []byte("<svg/>"), 0o644); err != nil {
		t.Fatal(err)
	}
	setWorkerBundle(t)

	fake := &recordingEdge{}
	cfg := Config{Edge: fake, ArtifactRoot: artifactRoot, StackName: "proj_1-prod"}
	manifest := &deploymentsv1.Manifest{
		Functions: []*deploymentsv1.ManifestFunction{
			{LogicalName: "api_documents", Framework: "next", App: "web", RouteId: "/api/documents"},
		},
	}
	outputs := []*deploymentsv1.ResourceOutput{fnOutput("api_documents", "https://fn.lambda-url.aws/")}

	out, err := deployEdgeWorker(context.Background(), cfg, manifest, outputs, nil)
	if err != nil {
		t.Fatalf("deployEdgeWorker: %v", err)
	}

	up := fake.only(t)
	if up.Name != "ocel-proj-1-prod-web" {
		t.Errorf("Name = %q, want ocel-proj-1-prod-web", up.Name)
	}
	if string(up.Worker.Main.Content) != "export default {}" {
		t.Errorf("Main content = %q", up.Worker.Main.Content)
	}
	if len(up.Worker.Modules) != 1 || up.Worker.Modules[0].Name != "routing-manifest.json" {
		t.Errorf("expected the routing manifest module, got %v", up.Worker.Modules)
	}
	if up.Worker.Modules[0].ContentType != "text/plain" {
		t.Errorf("manifest module Content-Type = %q, want text/plain (no JSON module type exists)", up.Worker.Modules[0].ContentType)
	}
	if len(up.Worker.Assets) != 1 || up.Worker.Assets[0].Path != "/next.svg" {
		t.Errorf("expected the static asset, got %v", up.Worker.Assets)
	}
	if got, want := up.Worker.Vars["FUNCTION_URLS"], `{"/api/documents":"https://fn.lambda-url.aws/"}`; got != want {
		t.Errorf("FUNCTION_URLS = %q, want %q", got, want)
	}
	if len(out) != 1 || out[0].GetFunction().GetUrl() != "https://ocel-proj-1-prod-web.acme.workers.dev" {
		t.Errorf("expected the worker URL output, got %v", out)
	}
}

// The exact binding set a fully-configured Next app emits. Dropping any one of
// the OCEL_ISR_* / OCEL_EDGE_* bindings silently degrades the worker to
// forwarding every prerender route to the Lambda — still correct, just slower
// and costlier — so only pinning the whole set catches it.
func TestDeployEdgeWorker_FullyConfiguredBindingSet(t *testing.T) {
	artifactRoot := t.TempDir()
	writeRoutingManifest(t, artifactRoot, "web", `{"buildId":"b1","appName":"web"}`)
	setWorkerBundle(t)

	fake := &recordingEdge{}
	cfg := Config{
		Edge:            fake,
		ArtifactRoot:    artifactRoot,
		StackName:       "proj_1-prod",
		Region:          "us-west-2",
		AssetBucket:     "ocel-assets",
		StateTable:      "ocel-state",
		Env:             "prod",
		EdgeAccessKeyID: "AKIAEDGE",
		EdgeSecretKey:   "secret-edge",
	}
	manifest := &deploymentsv1.Manifest{
		ProjectId: "proj",
		Functions: []*deploymentsv1.ManifestFunction{
			{LogicalName: "index", Framework: "next", App: "web", RouteId: "/"},
		},
	}
	outputs := []*deploymentsv1.ResourceOutput{fnOutput("index", "https://fn.lambda-url.aws/")}

	if _, err := deployEdgeWorker(context.Background(), cfg, manifest, outputs, nil); err != nil {
		t.Fatalf("deployEdgeWorker: %v", err)
	}

	wantVars := map[string]string{
		"FUNCTION_URLS":           `{"/":"https://fn.lambda-url.aws/"}`,
		"OCEL_EDGE_ACCESS_KEY_ID": "AKIAEDGE",
		"OCEL_AWS_REGION":         "us-west-2",
		"OCEL_ISR_BUCKET":         "ocel-assets",
		"OCEL_ISR_PREFIX":         "prod/proj/web/b1",
		"OCEL_STATE_TABLE":        "ocel-state",
		"OCEL_STATE_TABLE_INDEX":  "gsi1",
		"OCEL_ISR_TAG_NAMESPACE":  "TAG#prod#proj#web#b1#",
	}
	up := fake.only(t)
	if len(up.Worker.Vars) != len(wantVars) {
		t.Errorf("got %d vars, want %d: %v", len(up.Worker.Vars), len(wantVars), up.Worker.Vars)
	}
	for k, want := range wantVars {
		if got := up.Worker.Vars[k]; got != want {
			t.Errorf("Vars[%s] = %q, want %q", k, got, want)
		}
	}
	if len(up.Worker.Secrets) != 1 || up.Worker.Secrets["OCEL_EDGE_SECRET_KEY"] != "secret-edge" {
		t.Errorf("Secrets = %v, want only OCEL_EDGE_SECRET_KEY", up.Worker.Secrets)
	}
	if _, leaked := up.Worker.Vars["OCEL_EDGE_SECRET_KEY"]; leaked {
		t.Error("the secret access key must not appear in plain-text Vars")
	}
	if up.Worker.AssetBinding != "ASSETS" {
		t.Errorf("AssetBinding = %q, want ASSETS", up.Worker.AssetBinding)
	}
}

func TestDeployEdgeWorker_NoCacheBindingsWithoutEdgeCreds(t *testing.T) {
	artifactRoot := writeMinimalWorkerArtifacts(t)
	fake := &recordingEdge{}
	cfg := Config{Edge: fake, ArtifactRoot: artifactRoot, StackName: "proj_1-prod"}
	manifest := &deploymentsv1.Manifest{
		Functions: []*deploymentsv1.ManifestFunction{{LogicalName: "index", Framework: "next", App: "web", RouteId: "/"}},
	}
	outputs := []*deploymentsv1.ResourceOutput{fnOutput("index", "https://fn.lambda-url.aws/")}

	if _, err := deployEdgeWorker(context.Background(), cfg, manifest, outputs, nil); err != nil {
		t.Fatalf("a substrate predating edge credentials must still deploy: %v", err)
	}

	up := fake.only(t)
	if up.Worker.Secrets != nil {
		t.Errorf("expected no secrets without edge creds, got %v", up.Worker.Secrets)
	}
	if len(up.Worker.Vars) != 1 || up.Worker.Vars["FUNCTION_URLS"] == "" {
		t.Errorf("expected only FUNCTION_URLS, got %v", up.Worker.Vars)
	}
}

// The two ways a cache legitimately does not exist. Both must read as
// not-configured, never as an error, or the deploy would fail where today it
// degrades to forwarding.
func TestDeployResolver_CacheStoreNotConfigured(t *testing.T) {
	artifactRoot := writeMinimalWorkerArtifacts(t)
	withCreds := Config{ArtifactRoot: artifactRoot, EdgeAccessKeyID: "AKIAEDGE", EdgeSecretKey: "secret-edge"}
	nextApp := &deploymentsv1.Manifest{
		ProjectId: "proj",
		Functions: []*deploymentsv1.ManifestFunction{{LogicalName: "index", Framework: "next", App: "web", RouteId: "/"}},
	}

	cases := map[string]*deployResolver{
		"substrate predates edge credentials": {cfg: Config{ArtifactRoot: artifactRoot}, manifest: nextApp},
		"app has no prerendered content":      {cfg: withCreds, manifest: &deploymentsv1.Manifest{ProjectId: "proj"}},
	}
	for name, r := range cases {
		t.Run(name, func(t *testing.T) {
			store, configured, err := r.CacheStore()
			if err != nil {
				t.Fatalf("not-configured must not be an error: %v", err)
			}
			if configured {
				t.Errorf("expected not-configured, got %+v", store)
			}
		})
	}
}

func TestDeployEdgeWorker_UnresolvableRouteFailsNamingIt(t *testing.T) {
	artifactRoot := writeMinimalWorkerArtifacts(t)
	fake := &recordingEdge{}
	cfg := Config{Edge: fake, ArtifactRoot: artifactRoot, StackName: "proj_1-prod"}
	manifest := &deploymentsv1.Manifest{
		Functions: []*deploymentsv1.ManifestFunction{{LogicalName: "orphan", Framework: "next", App: "web", RouteId: "/orphan"}},
	}

	_, err := deployEdgeWorker(context.Background(), cfg, manifest, nil, nil)
	if err == nil {
		t.Fatal("expected an unresolvable route to fail the deploy")
	}
	if !strings.Contains(err.Error(), "/orphan") {
		t.Errorf("error must name the function, got %q", err)
	}
	if fake.called() {
		t.Error("a worker that cannot route must never reach the edge")
	}
}

func TestDeployEdgeWorker_UnsupportedPairingNamesBoth(t *testing.T) {
	artifactRoot := writeMinimalWorkerArtifacts(t)
	cfg := Config{Edge: &otherEdge{}, ArtifactRoot: artifactRoot, StackName: "proj_1-prod"}
	manifest := &deploymentsv1.Manifest{
		Functions: []*deploymentsv1.ManifestFunction{{LogicalName: "index", Framework: "next", App: "web", RouteId: "/"}},
	}

	_, err := deployEdgeWorker(context.Background(), cfg, manifest, nil, nil)
	if err == nil {
		t.Fatal("expected an unsupported framework/edge pairing to fail")
	}
	if !strings.Contains(err.Error(), "next") || !strings.Contains(err.Error(), "provider-native") {
		t.Errorf("error must name both framework and edge, got %q", err)
	}
}

func TestDeployEdgeWorker_CustomDomainOnlyForProduction(t *testing.T) {
	cases := []struct {
		name    string
		class   deploymentsv1.Environment_Class
		domains map[string]string
		want    string
	}{
		{"production with domain", deploymentsv1.Environment_CLASS_PRODUCTION, map[string]string{"production": "app.acme.com"}, "app.acme.com"},
		{"production without domain", deploymentsv1.Environment_CLASS_PRODUCTION, nil, ""},
		{"preview ignores domain", deploymentsv1.Environment_CLASS_PREVIEW, map[string]string{"production": "app.acme.com"}, ""},
		{"unspecified ignores domain", deploymentsv1.Environment_CLASS_UNSPECIFIED, map[string]string{"production": "app.acme.com"}, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			artifactRoot := writeMinimalWorkerArtifacts(t)
			fake := &recordingEdge{}
			cfg := Config{Edge: fake, ArtifactRoot: artifactRoot, StackName: "proj_1-prod", Class: tc.class}
			manifest := &deploymentsv1.Manifest{
				Functions: []*deploymentsv1.ManifestFunction{{LogicalName: "api_documents", Framework: "next", App: "web", RouteId: "/api/documents"}},
				Domains:   tc.domains,
			}
			outputs := []*deploymentsv1.ResourceOutput{fnOutput("api_documents", "https://fn.lambda-url.aws/")}

			if _, err := deployEdgeWorker(context.Background(), cfg, manifest, outputs, nil); err != nil {
				t.Fatalf("deployEdgeWorker: %v", err)
			}
			if got := fake.only(t).Domain; got != tc.want {
				t.Errorf("Domain = %q, want %q", got, tc.want)
			}
		})
	}
}

// writeRoutingManifest seeds one app's routing manifest in its own subtree of
// the build output, mirroring the builder's per-app namespacing.
func writeRoutingManifest(t *testing.T, artifactRoot, app, content string) string {
	t.Helper()
	dir := appArtifactRoot(artifactRoot, app)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "routing-manifest.json"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

// setWorkerBundle writes a worker bundle and exports the manifest pointing the
// Next-on-Cloudflare pairing at it, standing in for the npm launcher.
func setWorkerBundle(t *testing.T) {
	t.Helper()
	bundle := filepath.Join(t.TempDir(), "index.js")
	if err := os.WriteFile(bundle, []byte("export default {}"), 0o644); err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(edge.BundleManifest{edge.FrameworkNext: {edge.KindCloudflare: bundle}})
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv(edge.EnvWorkerBundles, string(raw))
}

func writeMinimalWorkerArtifacts(t *testing.T) string {
	t.Helper()
	artifactRoot := t.TempDir()
	writeRoutingManifest(t, artifactRoot, "web", `{"buildId":"b"}`)
	setWorkerBundle(t)
	return artifactRoot
}

// nextApp is a Next app plus its single index function, the shape most
// multi-app assertions below only vary the names of.
func nextApp(name string, domains map[string]string) (*deploymentsv1.ManifestApp, *deploymentsv1.ManifestFunction) {
	return &deploymentsv1.ManifestApp{Name: name, Framework: "next", Domains: domains},
		&deploymentsv1.ManifestFunction{LogicalName: name + "_index", Framework: "next", App: name, RouteId: "/"}
}

// twoNextApps builds a project of two Next apps, each with its own build output
// and one function, and the realized Function URL outputs for both.
func twoNextApps(t *testing.T) (string, *deploymentsv1.Manifest, []*deploymentsv1.ResourceOutput) {
	t.Helper()
	artifactRoot := t.TempDir()
	writeRoutingManifest(t, artifactRoot, "web", `{"buildId":"bweb"}`)
	writeRoutingManifest(t, artifactRoot, "docs", `{"buildId":"bdocs"}`)
	setWorkerBundle(t)

	webApp, webFn := nextApp("web", nil)
	docsApp, docsFn := nextApp("docs", nil)
	manifest := &deploymentsv1.Manifest{
		ProjectId: "proj",
		Apps:      []*deploymentsv1.ManifestApp{webApp, docsApp},
		Functions: []*deploymentsv1.ManifestFunction{webFn, docsFn},
	}
	outputs := []*deploymentsv1.ResourceOutput{
		fnOutput("web_index", "https://web-fn.lambda-url.aws/"),
		fnOutput("docs_index", "https://docs-fn.lambda-url.aws/"),
	}
	return artifactRoot, manifest, outputs
}

func TestDeployEdgeWorker_OneWorkerPerApp(t *testing.T) {
	artifactRoot, manifest, outputs := twoNextApps(t)
	fake := &recordingEdge{}
	cfg := Config{Edge: fake, ArtifactRoot: artifactRoot, StackName: "proj-prod"}

	out, err := deployEdgeWorker(context.Background(), cfg, manifest, outputs, nil)
	if err != nil {
		t.Fatalf("deployEdgeWorker: %v", err)
	}

	want := []string{"ocel-proj-prod-web", "ocel-proj-prod-docs"}
	if got := fake.names(); !slicesEqual(got, want) {
		t.Fatalf("deployed script names = %v, want %v", got, want)
	}
	// Each worker routes only to its own app's function.
	for _, d := range fake.deployed {
		app := strings.TrimPrefix(d.Name, "ocel-proj-prod-")
		if got, want := d.Worker.Vars["FUNCTION_URLS"], `{"/":"https://`+app+`-fn.lambda-url.aws/"}`; got != want {
			t.Errorf("%s FUNCTION_URLS = %q, want %q", d.Name, got, want)
		}
	}
	if len(out) != 2 {
		t.Fatalf("expected one output per worker, got %v", out)
	}
	if got := appURLs(manifest, append(outputs, out...)); !slicesEqual(got, []string{
		"https://ocel-proj-prod-web.acme.workers.dev",
		"https://ocel-proj-prod-docs.acme.workers.dev",
	}) {
		t.Errorf("appURLs = %v, want both worker URLs", got)
	}
}

// The app segment is what keeps two apps apart, so it must survive a project and
// environment long enough to overrun the platform's 63-char script-name limit.
func TestWorkerScriptName_AppSegmentSurvivesTruncation(t *testing.T) {
	stack := strings.Repeat("verylongproject", 5) + "-production"
	web := workerScriptName(stack, "web")
	docs := workerScriptName(stack, "docs")

	for _, name := range []string{web, docs} {
		if len(name) > maxWorkerNameLen {
			t.Errorf("%q is %d chars, over the %d-char limit", name, len(name), maxWorkerNameLen)
		}
	}
	if !strings.HasSuffix(web, "-web") || !strings.HasSuffix(docs, "-docs") {
		t.Errorf("app segment was truncated away: %q, %q", web, docs)
	}
	if web == docs {
		t.Fatalf("two apps collided on one script name: %q", web)
	}
	t.Logf("web  = %s (%d)", web, len(web))
	t.Logf("docs = %s (%d)", docs, len(docs))
}

func TestDeployEdgeWorker_AppDomainWins(t *testing.T) {
	artifactRoot, manifest, outputs := twoNextApps(t)
	manifest.Domains = map[string]string{"production": "project.acme.com"}
	manifest.GetApps()[0].Domains = map[string]string{"production": "web.acme.com"}
	manifest.GetApps()[1].Domains = map[string]string{"production": "docs.acme.com"}

	fake := &recordingEdge{}
	cfg := Config{Edge: fake, ArtifactRoot: artifactRoot, StackName: "proj-prod", Class: deploymentsv1.Environment_CLASS_PRODUCTION}
	if _, err := deployEdgeWorker(context.Background(), cfg, manifest, outputs, nil); err != nil {
		t.Fatalf("deployEdgeWorker: %v", err)
	}

	want := map[string]string{"ocel-proj-prod-web": "web.acme.com", "ocel-proj-prod-docs": "docs.acme.com"}
	for _, d := range fake.deployed {
		if d.Domain != want[d.Name] {
			t.Errorf("%s Domain = %q, want %q", d.Name, d.Domain, want[d.Name])
		}
	}
}

func TestDeployEdgeWorker_ProjectDomainNeedsExactlyOneWorkerApp(t *testing.T) {
	artifactRoot := t.TempDir()
	writeRoutingManifest(t, artifactRoot, "web", `{"buildId":"bweb"}`)
	setWorkerBundle(t)

	webApp, webFn := nextApp("web", nil)
	manifest := &deploymentsv1.Manifest{
		ProjectId: "proj",
		Domains:   map[string]string{"production": "project.acme.com"},
		Apps: []*deploymentsv1.ManifestApp{
			webApp,
			{Name: "api", Framework: "express"},
		},
		Functions: []*deploymentsv1.ManifestFunction{
			webFn,
			{LogicalName: "api_handler", Framework: "express", App: "api"},
		},
	}
	outputs := []*deploymentsv1.ResourceOutput{
		fnOutput("web_index", "https://web-fn.lambda-url.aws/"),
		fnOutput("api_handler", "https://api-fn.lambda-url.aws/"),
	}

	fake := &recordingEdge{}
	cfg := Config{Edge: fake, ArtifactRoot: artifactRoot, StackName: "proj-prod", Class: deploymentsv1.Environment_CLASS_PRODUCTION}
	out, err := deployEdgeWorker(context.Background(), cfg, manifest, outputs, nil)
	if err != nil {
		t.Fatalf("deployEdgeWorker: %v", err)
	}
	if got := fake.only(t).Domain; got != "project.acme.com" {
		t.Errorf("Domain = %q, want the project-level domain", got)
	}
	// The Express app has no registry entry, so it is served from its own
	// Function URL.
	if got := appURLs(manifest, append(outputs, out...)); !slicesEqual(got, []string{
		"https://ocel-proj-prod-web.acme.workers.dev",
		"https://api-fn.lambda-url.aws/",
	}) {
		t.Errorf("appURLs = %v", got)
	}
}

func TestDeployEdgeWorker_ProjectDomainWithTwoWorkerAppsIsAmbiguous(t *testing.T) {
	artifactRoot, manifest, outputs := twoNextApps(t)
	manifest.Domains = map[string]string{"production": "project.acme.com"}

	fake := &recordingEdge{}
	cfg := Config{Edge: fake, ArtifactRoot: artifactRoot, StackName: "proj-prod", Class: deploymentsv1.Environment_CLASS_PRODUCTION}
	_, err := deployEdgeWorker(context.Background(), cfg, manifest, outputs, nil)
	if err == nil {
		t.Fatal("expected an ambiguous project-level domain to fail the deploy")
	}
	for _, want := range []string{"project.acme.com", `"web"`, `"docs"`} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error must mention %s, got %q", want, err)
		}
	}
	if fake.called() {
		t.Error("an ambiguous domain must fail before anything reaches the edge")
	}
	t.Logf("error = %v", err)
}

// A preview deploy consults no domain at all, and still deploys every app's
// worker — the same path production takes.
func TestDeployEdgeWorker_PreviewDeploysEveryWorkerWithoutDomains(t *testing.T) {
	artifactRoot, manifest, outputs := twoNextApps(t)
	manifest.Domains = map[string]string{"production": "project.acme.com"}
	manifest.GetApps()[0].Domains = map[string]string{"production": "web.acme.com"}

	fake := &recordingEdge{}
	cfg := Config{Edge: fake, ArtifactRoot: artifactRoot, StackName: "proj-preview-pr-7", Class: deploymentsv1.Environment_CLASS_PREVIEW}
	if _, err := deployEdgeWorker(context.Background(), cfg, manifest, outputs, nil); err != nil {
		t.Fatalf("deployEdgeWorker: %v", err)
	}

	want := []string{"ocel-proj-preview-pr-7-web", "ocel-proj-preview-pr-7-docs"}
	if got := fake.names(); !slicesEqual(got, want) {
		t.Fatalf("deployed script names = %v, want %v", got, want)
	}
	for _, d := range fake.deployed {
		if d.Domain != "" {
			t.Errorf("%s Domain = %q, want none outside production", d.Name, d.Domain)
		}
	}
}

func TestDeployEdgeWorker_WarnsAboutWorkerAtThePreviousName(t *testing.T) {
	artifactRoot, manifest, outputs := twoNextApps(t)
	fake := &legacyEdge{existing: map[string]bool{"ocel-proj-prod": true}}
	cfg := Config{Edge: fake, ArtifactRoot: artifactRoot, StackName: "proj-prod"}

	var msgs []string
	if _, err := deployEdgeWorker(context.Background(), cfg, manifest, outputs, func(m string) { msgs = append(msgs, m) }); err != nil {
		t.Fatalf("deployEdgeWorker: %v", err)
	}

	var warned string
	for _, m := range msgs {
		if strings.Contains(m, "ocel-proj-prod") && !strings.Contains(m, "ocel-proj-prod-") {
			warned = m
		}
	}
	if warned == "" {
		t.Fatalf("expected a warning naming the previous script, got %v", msgs)
	}
	t.Logf("warning = %s", warned)
	// Warning only: the orphan is reported, never removed, and both apps still
	// deploy.
	if len(fake.deployed) != 2 {
		t.Errorf("expected both workers to deploy, got %v", fake.names())
	}
}

func TestDeployEdgeWorker_NoWarningWithoutALegacyWorker(t *testing.T) {
	artifactRoot, manifest, outputs := twoNextApps(t)
	fake := &legacyEdge{}
	cfg := Config{Edge: fake, ArtifactRoot: artifactRoot, StackName: "proj-prod"}

	var msgs []string
	if _, err := deployEdgeWorker(context.Background(), cfg, manifest, outputs, func(m string) { msgs = append(msgs, m) }); err != nil {
		t.Fatalf("deployEdgeWorker: %v", err)
	}
	for _, m := range msgs {
		if strings.Contains(m, "ocel-proj-prod") {
			t.Errorf("unexpected warning with no legacy worker: %q", m)
		}
	}
	if !slicesEqual(fake.asked, []string{"ocel-proj-prod"}) {
		t.Errorf("asked = %v, want the unqualified name once", fake.asked)
	}
}

func slicesEqual(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range want {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

// The edge's own bootstrap outputs are persisted by the provider and handed
// back verbatim, so the edge can read what it provisioned without re-querying
// its API. The provider never reads an individual key.
func TestDeployEdgeWorker_HandsTheEdgeItsOwnBootstrapValues(t *testing.T) {
	artifactRoot, manifest, outputs := twoNextApps(t)
	fake := &recordingEdge{}
	values := map[string]string{"bucketName": "edge-cache-7f3", "zoneID": "z1"}
	cfg := Config{Edge: fake, ArtifactRoot: artifactRoot, StackName: "proj-prod", EdgeValues: values}

	if _, err := deployEdgeWorker(context.Background(), cfg, manifest, outputs, nil); err != nil {
		t.Fatalf("deployEdgeWorker: %v", err)
	}
	if len(fake.deployed) != 2 {
		t.Fatalf("expected two deployments, got %d", len(fake.deployed))
	}
	for _, d := range fake.deployed {
		if len(d.Values) != len(values) {
			t.Fatalf("%s: Values = %v, want %v", d.Name, d.Values, values)
		}
		for k, want := range values {
			if d.Values[k] != want {
				t.Errorf("%s: Values[%s] = %q, want %q", d.Name, k, d.Values[k], want)
			}
		}
	}
}

// A configured Next app that emits no functions — a static export — produced no
// build output for a worker to route to, so it must deploy without one rather
// than failing on a routing manifest that was never written.
func TestDeployEdgeWorker_ConfiguredAppWithNoFunctionsDeploysNoWorker(t *testing.T) {
	setWorkerBundle(t)
	fake := &recordingEdge{}
	manifest := &deploymentsv1.Manifest{
		ProjectId: "proj",
		Apps:      []*deploymentsv1.ManifestApp{{Name: "marketing", Framework: "next"}},
	}
	cfg := Config{Edge: fake, ArtifactRoot: t.TempDir(), StackName: "proj-prod"}

	out, err := deployEdgeWorker(context.Background(), cfg, manifest, nil, nil)
	if err != nil {
		t.Fatalf("a static-export app must not fail the deploy: %v", err)
	}
	if fake.called() {
		t.Errorf("an app with no functions must not reach the edge, got %v", fake.names())
	}
	if out != nil {
		t.Errorf("expected no outputs, got %v", out)
	}
}

// The zero-function app must not suppress its siblings either.
func TestDeployEdgeWorker_ZeroFunctionAppDoesNotBlockOthers(t *testing.T) {
	artifactRoot := t.TempDir()
	writeRoutingManifest(t, artifactRoot, "web", `{"buildId":"bweb"}`)
	setWorkerBundle(t)

	webApp, webFn := nextApp("web", nil)
	manifest := &deploymentsv1.Manifest{
		ProjectId: "proj",
		Apps:      []*deploymentsv1.ManifestApp{{Name: "marketing", Framework: "next"}, webApp},
		Functions: []*deploymentsv1.ManifestFunction{webFn},
	}
	outputs := []*deploymentsv1.ResourceOutput{fnOutput("web_index", "https://web-fn.lambda-url.aws/")}
	fake := &recordingEdge{}

	if _, err := deployEdgeWorker(context.Background(), Config{Edge: fake, ArtifactRoot: artifactRoot, StackName: "proj-prod"}, manifest, outputs, nil); err != nil {
		t.Fatalf("deployEdgeWorker: %v", err)
	}
	if got := fake.names(); !slicesEqual(got, []string{"ocel-proj-prod-web"}) {
		t.Errorf("deployed = %v, want only the app that emitted functions", got)
	}
}
