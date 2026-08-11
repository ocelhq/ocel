package deploy

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	cloudflare "github.com/ocelhq/ocel/platform/edge/cloudflare/deploy"

	deploymentsv1 "github.com/ocelhq/ocel/pkg/proto/deployments/v1"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
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
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"underscores and uppercase collapse to lowercase hyphens", "ocel-proj_ABC-prod", "ocel-proj-abc-prod"},
		{"dots become hyphens", "ocel-Proj.123", "ocel-proj-123"},
		{"leading and trailing separators are trimmed", "--weird__name--", "weird-name"},
		{"empty falls back to the default worker name", "", "ocel-worker"},
		{"an all-separator name falls back to the default worker name", "////", "ocel-worker"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := sanitizeWorkerName(tc.in); got != tc.want {
				t.Errorf("sanitizeWorkerName(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}

	t.Run("clamps to 63 chars", func(t *testing.T) {
		t.Parallel()
		long := strings.Repeat("a", 100)
		if got := sanitizeWorkerName(long); len(got) != 63 {
			t.Errorf("expected clamp to 63 chars, got %d", len(got))
		}
	})
}

type recordingEdge struct {
	deployed []edge.AppDeployment
}

func (f *recordingEdge) Kind() edge.Kind { return edge.KindCloudflare }

func (f *recordingEdge) AssembleApp(src edge.WorkerSource, r edge.Resolver) (edge.Worker, error) {
	return cloudflare.New().AssembleApp(src, r)
}

func (f *recordingEdge) Bootstrap(context.Context, edge.Class) (edge.BootstrapOutput, error) {
	return edge.BootstrapOutput{Trust: edge.TrustExternal}, nil
}

func (f *recordingEdge) DeployApp(_ context.Context, app edge.AppDeployment) (edge.AppResult, error) {
	f.deployed = append(f.deployed, app)
	return edge.AppResult{URL: "https://" + app.Name + ".acme.workers.dev"}, nil
}

func (f *recordingEdge) called() bool { return len(f.deployed) > 0 }

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

type legacyEdge struct {
	recordingEdge
	existing map[string]bool
	asked    []string
}

func (l *legacyEdge) FindApp(_ context.Context, name string) (bool, error) {
	l.asked = append(l.asked, name)
	return l.existing[name], nil
}

type otherEdge struct{ recordingEdge }

func (o *otherEdge) Kind() edge.Kind { return "provider-native" }

func TestDeployEdgeWorker(t *testing.T) {
	t.Run("a framework with no worker is a no-op", func(t *testing.T) {
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
	})

	t.Run("assembles, uploads and reports the URL", func(t *testing.T) {
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
		cfg := Config{Edge: fake, ArtifactRoot: artifactRoot, StackName: "proj_1-prod", Slug: "proj_1", Env: "prod"}
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
		if up.Name != "ocel-proj-1--prod-web" {
			t.Errorf("Name = %q, want ocel-proj-1--prod-web", up.Name)
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
		if len(out) != 1 || out[0].GetFunction().GetUrl() != "https://ocel-proj-1--prod-web.acme.workers.dev" {
			t.Errorf("expected the worker URL output, got %v", out)
		}
	})

	t.Run("fully configured binding set", func(t *testing.T) {
		artifactRoot := t.TempDir()
		writeRoutingManifest(t, artifactRoot, "web", `{"buildId":"b1","appName":"web"}`)
		setWorkerBundle(t)

		fake := &recordingEdge{}
		cfg := Config{
			Edge:            fake,
			ArtifactRoot:    artifactRoot,
			StackName:       "proj_1-prod",
			Slug:            "proj_1",
			Region:          "us-west-2",
			AssetBucket:     "ocel-assets",
			StateTable:      "ocel-state",
			Env:             "prod",
			EdgeAccessKeyID: "AKIAEDGE",
			EdgeSecretKey:   "secret-edge",
		}
		manifest := &deploymentsv1.Manifest{
			Slug: "proj",
			Functions: []*deploymentsv1.ManifestFunction{
				{LogicalName: "index", Framework: "next", App: "web", RouteId: "/"},
			},
		}
		outputs := []*deploymentsv1.ResourceOutput{fnOutput("index", "https://fn.lambda-url.aws/")}

		if _, err := deployEdgeWorker(context.Background(), cfg, manifest, outputs, nil); err != nil {
			t.Fatalf("deployEdgeWorker: %v", err)
		}

		wantVars := map[string]string{
			"OCEL_EDGE_ACCESS_KEY_ID": "AKIAEDGE",
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
	})

	t.Run("no cache bindings without edge creds", func(t *testing.T) {
		artifactRoot := writeMinimalWorkerArtifacts(t)
		fake := &recordingEdge{}
		cfg := Config{Edge: fake, ArtifactRoot: artifactRoot, StackName: "proj_1-prod", Slug: "proj_1", Env: "prod"}
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
		if len(up.Worker.Vars) != 0 {
			t.Errorf("expected no vars without edge creds, got %v", up.Worker.Vars)
		}
	})

	t.Run("an unresolvable route fails naming it", func(t *testing.T) {
		artifactRoot := writeMinimalWorkerArtifacts(t)
		fake := &recordingEdge{}
		cfg := Config{Edge: fake, ArtifactRoot: artifactRoot, StackName: "proj_1-prod", Slug: "proj_1", Env: "prod"}
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
	})

	t.Run("an unsupported edge names the edge", func(t *testing.T) {
		artifactRoot := writeMinimalWorkerArtifacts(t)
		cfg := Config{Edge: &otherEdge{}, ArtifactRoot: artifactRoot, StackName: "proj_1-prod", Slug: "proj_1", Env: "prod"}
		manifest := &deploymentsv1.Manifest{
			Functions: []*deploymentsv1.ManifestFunction{{LogicalName: "index", Framework: "next", App: "web", RouteId: "/"}},
		}

		_, err := deployEdgeWorker(context.Background(), cfg, manifest, nil, nil)
		if err == nil {
			t.Fatal("expected an edge with no worker bundle to fail")
		}
		if !strings.Contains(err.Error(), "provider-native") {
			t.Errorf("error must name the edge that has no bundle, got %q", err)
		}
	})

	t.Run("domains only for production", func(t *testing.T) {
		cases := []struct {
			name    string
			class   deploymentsv1.Environment_Class
			domains map[string]*deploymentsv1.DomainList
			want    []string
		}{
			{"production with domains", deploymentsv1.Environment_CLASS_PRODUCTION, classDomains("production", "app.acme.com", "www.acme.com"), []string{"app.acme.com", "www.acme.com"}},
			{"production without domain", deploymentsv1.Environment_CLASS_PRODUCTION, nil, nil},
			{"preview ignores domain", deploymentsv1.Environment_CLASS_PREVIEW, classDomains("production", "app.acme.com"), nil},
			{"unspecified ignores domain", deploymentsv1.Environment_CLASS_UNSPECIFIED, classDomains("production", "app.acme.com"), nil},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				artifactRoot := writeMinimalWorkerArtifacts(t)
				fake := &recordingEdge{}
				cfg := Config{Edge: fake, ArtifactRoot: artifactRoot, StackName: "proj_1-prod", Slug: "proj_1", Env: "prod", Class: tc.class}
				manifest := &deploymentsv1.Manifest{
					Functions: []*deploymentsv1.ManifestFunction{{LogicalName: "api_documents", Framework: "next", App: "web", RouteId: "/api/documents"}},
					Domains:   tc.domains,
				}
				outputs := []*deploymentsv1.ResourceOutput{fnOutput("api_documents", "https://fn.lambda-url.aws/")}

				if _, err := deployEdgeWorker(context.Background(), cfg, manifest, outputs, nil); err != nil {
					t.Fatalf("deployEdgeWorker: %v", err)
				}
				if got := fake.only(t).Domains; !slicesEqual(got, tc.want) {
					t.Errorf("Domains = %v, want %v", got, tc.want)
				}
			})
		}
	})

	t.Run("one worker per app", func(t *testing.T) {
		artifactRoot, manifest, outputs := twoNextApps(t)
		fake := &recordingEdge{}
		cfg := Config{Edge: fake, ArtifactRoot: artifactRoot, StackName: "proj-prod", Slug: "proj", Env: "prod"}

		out, err := deployEdgeWorker(context.Background(), cfg, manifest, outputs, nil)
		if err != nil {
			t.Fatalf("deployEdgeWorker: %v", err)
		}

		want := []string{"ocel-proj--prod-web", "ocel-proj--prod-docs"}
		if got := fake.names(); !slicesEqual(got, want) {
			t.Fatalf("deployed script names = %v, want %v", got, want)
		}
		if len(out) != 2 {
			t.Fatalf("expected one output per worker, got %v", out)
		}
		if got := appURLs(manifest, append(outputs, out...)); !slicesEqual(got, []string{
			"https://ocel-proj--prod-web.acme.workers.dev",
			"https://ocel-proj--prod-docs.acme.workers.dev",
		}) {
			t.Errorf("appURLs = %v, want both worker URLs", got)
		}
	})

	t.Run("an app domain wins", func(t *testing.T) {
		artifactRoot, manifest, outputs := twoNextApps(t)
		manifest.Domains = classDomains("production", "project.acme.com")
		manifest.GetApps()[0].Domains = classDomains("production", "web.acme.com")
		manifest.GetApps()[1].Domains = classDomains("production", "docs.acme.com")

		fake := &recordingEdge{}
		cfg := Config{Edge: fake, ArtifactRoot: artifactRoot, StackName: "proj-prod", Slug: "proj", Env: "prod", Class: deploymentsv1.Environment_CLASS_PRODUCTION}
		if _, err := deployEdgeWorker(context.Background(), cfg, manifest, outputs, nil); err != nil {
			t.Fatalf("deployEdgeWorker: %v", err)
		}

		want := map[string][]string{"ocel-proj--prod-web": {"web.acme.com"}, "ocel-proj--prod-docs": {"docs.acme.com"}}
		for _, d := range fake.deployed {
			if !slicesEqual(d.Domains, want[d.Name]) {
				t.Errorf("%s Domains = %v, want %v", d.Name, d.Domains, want[d.Name])
			}
		}
	})

	t.Run("a project domain needs exactly one worker app", func(t *testing.T) {
		artifactRoot := t.TempDir()
		writeRoutingManifest(t, artifactRoot, "web", `{"buildId":"bweb"}`)
		setWorkerBundle(t)

		webApp, webFn := nextApp("web", nil)
		manifest := &deploymentsv1.Manifest{
			Slug:    "proj",
			Domains: classDomains("production", "project.acme.com"),
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
		cfg := Config{Edge: fake, ArtifactRoot: artifactRoot, StackName: "proj-prod", Slug: "proj", Env: "prod", Class: deploymentsv1.Environment_CLASS_PRODUCTION}
		out, err := deployEdgeWorker(context.Background(), cfg, manifest, outputs, nil)
		if err != nil {
			t.Fatalf("deployEdgeWorker: %v", err)
		}
		if got := fake.only(t).Domains; !slicesEqual(got, []string{"project.acme.com"}) {
			t.Errorf("Domains = %v, want the project-level domain", got)
		}
		if got := appURLs(manifest, append(outputs, out...)); !slicesEqual(got, []string{
			"https://ocel-proj--prod-web.acme.workers.dev",
			"https://api-fn.lambda-url.aws/",
		}) {
			t.Errorf("appURLs = %v", got)
		}
	})

	t.Run("a project domain with two worker apps is ambiguous", func(t *testing.T) {
		artifactRoot, manifest, outputs := twoNextApps(t)
		manifest.Domains = classDomains("production", "project.acme.com")

		fake := &recordingEdge{}
		cfg := Config{Edge: fake, ArtifactRoot: artifactRoot, StackName: "proj-prod", Slug: "proj", Env: "prod", Class: deploymentsv1.Environment_CLASS_PRODUCTION}
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
	})

	t.Run("preview deploys every worker without domains", func(t *testing.T) {
		artifactRoot, manifest, outputs := twoNextApps(t)
		manifest.Domains = classDomains("production", "project.acme.com")
		manifest.GetApps()[0].Domains = classDomains("production", "web.acme.com")

		fake := &recordingEdge{}
		cfg := Config{Edge: fake, ArtifactRoot: artifactRoot, StackName: "proj-preview-pr-7", Slug: "proj", Env: "preview-pr-7", Class: deploymentsv1.Environment_CLASS_PREVIEW}
		if _, err := deployEdgeWorker(context.Background(), cfg, manifest, outputs, nil); err != nil {
			t.Fatalf("deployEdgeWorker: %v", err)
		}

		want := []string{"ocel-proj--preview-pr-7-web", "ocel-proj--preview-pr-7-docs"}
		if got := fake.names(); !slicesEqual(got, want) {
			t.Fatalf("deployed script names = %v, want %v", got, want)
		}
		for _, d := range fake.deployed {
			if len(d.Domains) != 0 {
				t.Errorf("%s Domains = %v, want none outside production", d.Name, d.Domains)
			}
		}
	})

	t.Run("warns about a worker at the previous name", func(t *testing.T) {
		artifactRoot, manifest, outputs := twoNextApps(t)
		fake := &legacyEdge{existing: map[string]bool{"ocel-proj-prod": true}}
		cfg := Config{Edge: fake, ArtifactRoot: artifactRoot, StackName: "proj-prod", Slug: "proj", Env: "prod"}

		var msgs []string
		if _, err := deployEdgeWorker(context.Background(), cfg, manifest, outputs, func(m string) { msgs = append(msgs, m) }); err != nil {
			t.Fatalf("deployEdgeWorker: %v", err)
		}

		var warned string
		for _, m := range msgs {
			if strings.Contains(m, "ocel-proj-prod") && !strings.Contains(m, "ocel-proj--prod-") {
				warned = m
			}
		}
		if warned == "" {
			t.Fatalf("expected a warning naming the previous script, got %v", msgs)
		}
		t.Logf("warning = %s", warned)
		if len(fake.deployed) != 2 {
			t.Errorf("expected both workers to deploy, got %v", fake.names())
		}
	})

	t.Run("no warning without a legacy worker", func(t *testing.T) {
		artifactRoot, manifest, outputs := twoNextApps(t)
		fake := &legacyEdge{}
		cfg := Config{Edge: fake, ArtifactRoot: artifactRoot, StackName: "proj-prod", Slug: "proj", Env: "prod"}

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
	})

	t.Run("hands the edge its own bootstrap values", func(t *testing.T) {
		artifactRoot, manifest, outputs := twoNextApps(t)
		fake := &recordingEdge{}
		values := map[string]string{"bucketName": "edge-cache-7f3", "zoneID": "z1"}
		cfg := Config{Edge: fake, ArtifactRoot: artifactRoot, StackName: "proj-prod", Slug: "proj", Env: "prod", EdgeValues: values}

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
	})

	t.Run("a configured app with no functions deploys no worker", func(t *testing.T) {
		setWorkerBundle(t)
		fake := &recordingEdge{}
		manifest := &deploymentsv1.Manifest{
			Slug: "proj",
			Apps: []*deploymentsv1.ManifestApp{{Name: "marketing", Framework: "next"}},
		}
		cfg := Config{Edge: fake, ArtifactRoot: t.TempDir(), StackName: "proj-prod", Slug: "proj", Env: "prod"}

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
	})

	t.Run("a zero-function app does not block others", func(t *testing.T) {
		artifactRoot := t.TempDir()
		writeRoutingManifest(t, artifactRoot, "web", `{"buildId":"bweb"}`)
		setWorkerBundle(t)

		webApp, webFn := nextApp("web", nil)
		manifest := &deploymentsv1.Manifest{
			Slug:      "proj",
			Apps:      []*deploymentsv1.ManifestApp{{Name: "marketing", Framework: "next"}, webApp},
			Functions: []*deploymentsv1.ManifestFunction{webFn},
		}
		outputs := []*deploymentsv1.ResourceOutput{fnOutput("web_index", "https://web-fn.lambda-url.aws/")}
		fake := &recordingEdge{}

		if _, err := deployEdgeWorker(context.Background(), Config{Edge: fake, ArtifactRoot: artifactRoot, StackName: "proj-prod", Slug: "proj", Env: "prod"}, manifest, outputs, nil); err != nil {
			t.Fatalf("deployEdgeWorker: %v", err)
		}
		if got := fake.names(); !slicesEqual(got, []string{"ocel-proj--prod-web"}) {
			t.Errorf("deployed = %v, want only the app that emitted functions", got)
		}
	})
}

func TestDeployResolver(t *testing.T) {
	t.Run("edge credentials not configured", func(t *testing.T) {
		artifactRoot := writeMinimalWorkerArtifacts(t)
		r := &deployResolver{
			cfg:      Config{ArtifactRoot: artifactRoot},
			manifest: &deploymentsv1.Manifest{Slug: "proj"},
		}
		if creds, ok := r.EdgeCredentials(); ok {
			t.Errorf("expected not-configured, got %+v", creds)
		}
	})

	t.Run("edge credentials configured", func(t *testing.T) {
		r := &deployResolver{
			cfg: Config{EdgeAccessKeyID: "AKIAEDGE", EdgeSecretKey: "secret-edge"},
		}
		creds, ok := r.EdgeCredentials()
		if !ok {
			t.Fatal("expected configured credentials")
		}
		if creds.AccessKeyID != "AKIAEDGE" || creds.SecretKey != "secret-edge" {
			t.Errorf("creds = %+v, want the edge reader's key pair", creds)
		}
	})
}

func TestWorkerScriptName(t *testing.T) {
	t.Run("the app segment survives truncation", func(t *testing.T) {
		slug := strings.Repeat("verylongproject", 5)
		web := workerScriptName(slug, "prod", "web")
		docs := workerScriptName(slug, "prod", "docs")

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
	})
}

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

func setWorkerBundle(t *testing.T) {
	t.Helper()
	bundle := filepath.Join(t.TempDir(), "index.js")
	if err := os.WriteFile(bundle, []byte("export default {}"), 0o644); err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(edge.KindBundleManifest{edge.KindCloudflare: bundle})
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

func classDomains(class string, hostnames ...string) map[string]*deploymentsv1.DomainList {
	if len(hostnames) == 0 {
		return nil
	}
	return map[string]*deploymentsv1.DomainList{class: {Hostnames: hostnames}}
}

func nextApp(name string, domains map[string]*deploymentsv1.DomainList) (*deploymentsv1.ManifestApp, *deploymentsv1.ManifestFunction) {
	return &deploymentsv1.ManifestApp{Name: name, Framework: "next", Domains: domains},
		&deploymentsv1.ManifestFunction{LogicalName: name + "_index", Framework: "next", App: name, RouteId: "/"}
}

func twoNextApps(t *testing.T) (string, *deploymentsv1.Manifest, []*deploymentsv1.ResourceOutput) {
	t.Helper()
	artifactRoot := t.TempDir()
	writeRoutingManifest(t, artifactRoot, "web", `{"buildId":"bweb"}`)
	writeRoutingManifest(t, artifactRoot, "docs", `{"buildId":"bdocs"}`)
	setWorkerBundle(t)

	webApp, webFn := nextApp("web", nil)
	docsApp, docsFn := nextApp("docs", nil)
	manifest := &deploymentsv1.Manifest{
		Slug:      "proj",
		Apps:      []*deploymentsv1.ManifestApp{webApp, docsApp},
		Functions: []*deploymentsv1.ManifestFunction{webFn, docsFn},
	}
	outputs := []*deploymentsv1.ResourceOutput{
		fnOutput("web_index", "https://web-fn.lambda-url.aws/"),
		fnOutput("docs_index", "https://docs-fn.lambda-url.aws/"),
	}
	return artifactRoot, manifest, outputs
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
