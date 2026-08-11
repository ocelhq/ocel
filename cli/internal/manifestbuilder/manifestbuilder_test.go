package manifestbuilder

import (
	"bytes"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/naming"
	deploymentsv1 "github.com/ocelhq/ocel/pkg/proto/deployments/v1"
	resourcesv1 "github.com/ocelhq/ocel/pkg/proto/resources/v1"
)

const goldenPath = "testdata/golden_manifest.json"
const functionsGoldenPath = "testdata/golden_manifest_functions.json"

type goldenManifest struct {
	SchemaVersion string           `json:"schema_version"`
	Slug          string           `json:"slug"`
	Resources     []goldenResource `json:"resources"`
	Functions     []goldenFunction `json:"functions,omitempty"`
}

type goldenFunction struct {
	LogicalName  string `json:"logical_name"`
	Runtime      string `json:"runtime"`
	Handler      string `json:"handler"`
	ArtifactPath string `json:"artifact_path"`
	Framework    string `json:"framework"`
	RouteID      string `json:"route_id"`
}

type goldenResource struct {
	LogicalName string          `json:"logical_name"`
	Type        string          `json:"type"`
	ID          string          `json:"id"`
	Postgres    *goldenPostgres `json:"postgres,omitempty"`
}

type goldenPostgres struct {
	Version string `json:"version"`
}

func toGolden(m *deploymentsv1.Manifest) goldenManifest {
	g := goldenManifest{SchemaVersion: m.GetSchemaVersion(), Slug: m.GetSlug()}
	for _, r := range m.GetResources() {
		gr := goldenResource{
			LogicalName: r.GetLogicalName(),
			Type:        r.GetResource().GetType().String(),
			ID:          r.GetResource().GetName(),
		}
		if pg := r.GetPostgres(); pg != nil {
			gr.Postgres = &goldenPostgres{Version: pg.GetVersion()}
		}
		g.Resources = append(g.Resources, gr)
	}
	for _, f := range m.GetFunctions() {
		g.Functions = append(g.Functions, goldenFunction{
			LogicalName:  f.GetLogicalName(),
			Runtime:      f.GetRuntime(),
			Handler:      f.GetHandler(),
			ArtifactPath: f.GetArtifactPath(),
			Framework:    f.GetFramework(),
			RouteID:      f.GetRouteId(),
		})
	}
	return g
}

func marshal(t *testing.T, m *deploymentsv1.Manifest) []byte {
	t.Helper()
	out, err := json.MarshalIndent(toGolden(m), "", "  ")
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return append(out, '\n')
}

func synthDeclarations() []Declaration {
	return []Declaration{
		{Type: resourcesv1.ResourceType_RESOURCE_TYPE_POSTGRES, ID: "main", Postgres: &resourcesv1.PostgresConfig{Version: "17"}, Source: "app/db.ts:5"},
		{Type: resourcesv1.ResourceType_RESOURCE_TYPE_POSTGRES, ID: "analytics", Postgres: &resourcesv1.PostgresConfig{Version: "16"}, Source: "app/analytics.ts:9"},
	}
}

func synthFunctions() []Function {
	return []Function{
		{Name: "api/documents", App: "web", Runtime: "nodejs24.x", Handler: "app/api.ts", ArtifactPath: "dist/api.zip", Framework: "next", RouteID: "/api/documents"},
		{Name: "worker", App: "web", Runtime: "nodejs24.x", Handler: "app/worker.ts", ArtifactPath: "dist/worker.zip", Framework: ""},
	}
}

func TestBuild(t *testing.T) {
	t.Parallel()

	t.Run("carries slug", func(t *testing.T) {
		t.Parallel()

		manifest, err := Build("acme-web", nil, nil, nil, nil, nil)
		if err != nil {
			t.Fatalf("Build: %v", err)
		}
		if manifest.GetSlug() != "acme-web" {
			t.Errorf("slug = %q, want acme-web", manifest.GetSlug())
		}
	})

	goldenCases := []struct {
		name         string
		declarations []Declaration
		functions    []Function
		golden       string
	}{
		{"produces deterministic output matching the golden file", synthDeclarations(), nil, goldenPath},
		{"produces deterministic output matching the functions golden file", synthDeclarations(), synthFunctions(), functionsGoldenPath},
	}
	for _, c := range goldenCases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()

			first, err := Build("proj-1", nil, nil, c.declarations, c.functions, nil)
			if err != nil {
				t.Fatalf("Build: %v", err)
			}
			second, err := Build("proj-1", nil, nil, c.declarations, c.functions, nil)
			if err != nil {
				t.Fatalf("Build: %v", err)
			}

			firstJSON := marshal(t, first)
			secondJSON := marshal(t, second)
			if !bytes.Equal(firstJSON, secondJSON) {
				t.Fatalf("Build is not deterministic:\nfirst:\n%s\nsecond:\n%s", firstJSON, secondJSON)
			}

			golden, err := os.ReadFile(c.golden)
			if err != nil {
				t.Fatalf("read golden file: %v", err)
			}
			if !bytes.Equal(firstJSON, golden) {
				t.Fatalf("Build output does not match golden file %s:\ngot:\n%s\nwant:\n%s", c.golden, firstJSON, golden)
			}
		})
	}

	t.Run("is invariant to the order declarations arrive in", func(t *testing.T) {
		t.Parallel()

		declarations := synthDeclarations()
		inOrder, err := Build("proj-1", nil, nil, declarations, nil, nil)
		if err != nil {
			t.Fatalf("Build: %v", err)
		}

		reversed := []Declaration{declarations[1], declarations[0]}
		reorderedManifest, err := Build("proj-1", nil, nil, reversed, nil, nil)
		if err != nil {
			t.Fatalf("Build: %v", err)
		}

		if !bytes.Equal(marshal(t, inOrder), marshal(t, reorderedManifest)) {
			t.Fatalf("reordering declarations changed manifest output")
		}
	})

	t.Run("is invariant to the order functions arrive in", func(t *testing.T) {
		t.Parallel()

		functions := synthFunctions()
		inOrder, err := Build("proj-1", nil, nil, nil, functions, nil)
		if err != nil {
			t.Fatalf("Build: %v", err)
		}

		reversed := []Function{functions[1], functions[0]}
		reordered, err := Build("proj-1", nil, nil, nil, reversed, nil)
		if err != nil {
			t.Fatalf("Build: %v", err)
		}

		if !bytes.Equal(marshal(t, inOrder), marshal(t, reordered)) {
			t.Fatalf("reordering functions changed manifest output")
		}
	})

	t.Run("adding a resource leaves existing logical names unchanged", func(t *testing.T) {
		t.Parallel()

		base := synthDeclarations()
		before, err := Build("proj-1", nil, nil, base, nil, nil)
		if err != nil {
			t.Fatalf("Build: %v", err)
		}
		beforeNames := map[string]bool{}
		for _, r := range before.GetResources() {
			beforeNames[r.GetLogicalName()] = true
		}

		withExtra := append(append([]Declaration{}, base...), Declaration{
			Type: resourcesv1.ResourceType_RESOURCE_TYPE_POSTGRES, ID: "billing", Source: "app/billing.ts:2",
		})
		after, err := Build("proj-1", nil, nil, withExtra, nil, nil)
		if err != nil {
			t.Fatalf("Build: %v", err)
		}

		afterNames := map[string]bool{}
		for _, r := range after.GetResources() {
			afterNames[r.GetLogicalName()] = true
		}

		for name := range beforeNames {
			if !afterNames[name] {
				t.Fatalf("logical name %q present before adding a resource is missing after", name)
			}
		}
		if len(afterNames) != len(beforeNames)+1 {
			t.Fatalf("got %d logical names after add, want %d", len(afterNames), len(beforeNames)+1)
		}
	})

	t.Run("round-trips a typed postgres config as a oneof", func(t *testing.T) {
		t.Parallel()

		manifest, err := Build("proj-1", nil, nil, []Declaration{
			{Type: resourcesv1.ResourceType_RESOURCE_TYPE_POSTGRES, ID: "main", Postgres: &resourcesv1.PostgresConfig{Version: "17"}, Source: "app/db.ts:5"},
		}, nil, nil)
		if err != nil {
			t.Fatalf("Build: %v", err)
		}
		if len(manifest.GetResources()) != 1 {
			t.Fatalf("got %d resources, want 1", len(manifest.GetResources()))
		}

		resource := manifest.GetResources()[0]
		postgres := resource.GetPostgres()
		if postgres == nil {
			t.Fatalf("resource.GetPostgres() = nil, want typed PostgresConfig")
		}
		if postgres.GetVersion() != "17" {
			t.Fatalf("postgres.Version = %q, want %q", postgres.GetVersion(), "17")
		}
	})

	t.Run("round-trips a typed bucket config as a oneof", func(t *testing.T) {
		t.Parallel()

		manifest, err := Build("proj-1", nil, nil, []Declaration{
			{Type: resourcesv1.ResourceType_RESOURCE_TYPE_BUCKET, ID: "storage", Bucket: &resourcesv1.BucketConfig{AllowedOrigins: []string{"https://app.example.com"}}, Source: "app/storage.ts:3"},
		}, nil, nil)
		if err != nil {
			t.Fatalf("Build: %v", err)
		}
		if len(manifest.GetResources()) != 1 {
			t.Fatalf("got %d resources, want 1", len(manifest.GetResources()))
		}

		resource := manifest.GetResources()[0]
		if resource.GetLogicalName() != "bucket--storage" {
			t.Fatalf("logical_name = %q, want %q", resource.GetLogicalName(), "bucket--storage")
		}
		bucket := resource.GetBucket()
		if bucket == nil {
			t.Fatalf("resource.GetBucket() = nil, want typed BucketConfig")
		}
		if got := bucket.GetAllowedOrigins(); len(got) != 1 || got[0] != "https://app.example.com" {
			t.Fatalf("bucket.AllowedOrigins = %v, want [https://app.example.com]", got)
		}
	})

	t.Run("names both declarations and their sources on a duplicate type and id", func(t *testing.T) {
		t.Parallel()

		_, err := Build("proj-1", nil, nil, []Declaration{
			{Type: resourcesv1.ResourceType_RESOURCE_TYPE_POSTGRES, ID: "main", Source: "app/db.ts:5"},
			{Type: resourcesv1.ResourceType_RESOURCE_TYPE_POSTGRES, ID: "main", Source: "app/other.ts:12"},
		}, nil, nil)
		if err == nil {
			t.Fatal("Build: expected duplicate error, got nil")
		}

		dupErr, ok := err.(*DuplicateError)
		if !ok {
			t.Fatalf("Build error = %T, want *DuplicateError", err)
		}
		if dupErr.TypeToken != "db" || dupErr.ID != "main" {
			t.Fatalf("DuplicateError = %+v, want type=db id=main", dupErr)
		}
		if dupErr.FirstSource != "app/db.ts:5" || dupErr.SecondSource != "app/other.ts:12" {
			t.Fatalf("DuplicateError sources = %q, %q, want both offending source locations", dupErr.FirstSource, dupErr.SecondSource)
		}

		msg := dupErr.Error()
		for _, want := range []string{"db", "main", "app/db.ts:5", "app/other.ts:12"} {
			if !strings.Contains(msg, want) {
				t.Fatalf("error message %q does not contain %q", msg, want)
			}
		}
	})

	t.Run("keeps the app on its own field so routes cannot borrow it", func(t *testing.T) {
		t.Parallel()

		manifest, err := Build("proj-1", nil, nil, nil, []Function{
			{Name: "api/users", App: "web", Runtime: "nodejs24.x", Handler: "h.js", ArtifactPath: "a"},
			{Name: "users", App: "web-api", Runtime: "nodejs24.x", Handler: "h.js", ArtifactPath: "b"},
		}, nil)
		if err != nil {
			t.Fatalf("Build: %v", err)
		}

		names := []string{manifest.GetFunctions()[0].GetLogicalName(), manifest.GetFunctions()[1].GetLogicalName()}
		if names[0] == names[1] {
			t.Fatalf("both functions got the logical name %q; the app field must stay separable", names[0])
		}
		if names[0] != "fn--web--api-users" || names[1] != "fn--web-api--users" {
			t.Fatalf("logical names = %v, want [fn--web--api-users fn--web-api--users]", names)
		}
	})

	t.Run("names both routes when two of them collide", func(t *testing.T) {
		t.Parallel()

		_, err := Build("proj-1", nil, nil, nil, []Function{
			{Name: "api/users", App: "web", Runtime: "nodejs24.x", Handler: "h.js", ArtifactPath: "a"},
			{Name: "api_users", App: "web", Runtime: "nodejs24.x", Handler: "h.js", ArtifactPath: "b"},
		}, nil)
		if err == nil {
			t.Fatal("Build: expected a collision error, got nil")
		}

		collision, ok := err.(*CollisionError)
		if !ok {
			t.Fatalf("Build error = %T, want *CollisionError", err)
		}
		if collision.LogicalName != "fn--web--api-users" {
			t.Fatalf("CollisionError.LogicalName = %q, want %q", collision.LogicalName, "fn--web--api-users")
		}
		for _, want := range []string{"api/users", "api_users", "web", "fn--web--api-users"} {
			if !strings.Contains(collision.Error(), want) {
				t.Fatalf("error message %q does not name %q", collision.Error(), want)
			}
		}
	})

	t.Run("names both declarations when two ids collide", func(t *testing.T) {
		t.Parallel()

		_, err := Build("proj-1", nil, nil, []Declaration{
			{Type: resourcesv1.ResourceType_RESOURCE_TYPE_BUCKET, ID: "my_uploads", Source: "app/a.ts:1"},
			{Type: resourcesv1.ResourceType_RESOURCE_TYPE_BUCKET, ID: "my-uploads", Source: "app/b.ts:2"},
		}, nil, nil)
		if err == nil {
			t.Fatal("Build: expected a collision error, got nil")
		}

		collision, ok := err.(*CollisionError)
		if !ok {
			t.Fatalf("Build error = %T, want *CollisionError", err)
		}
		for _, want := range []string{"bucket--my-uploads", "my_uploads", "my-uploads", "app/a.ts:1", "app/b.ts:2"} {
			if !strings.Contains(collision.Error(), want) {
				t.Fatalf("error message %q does not name %q", collision.Error(), want)
			}
		}
	})

	t.Run("refuses a function with no app", func(t *testing.T) {
		t.Parallel()

		_, err := Build("proj-1", nil, nil, nil, []Function{
			{Name: "index", Runtime: "nodejs24.x", Handler: "h.js", ArtifactPath: "a"},
		}, nil)
		if err == nil {
			t.Fatal("Build: expected an error for a function with no app, got nil")
		}
	})

	t.Run("refuses an unsupported resource type", func(t *testing.T) {
		t.Parallel()

		_, err := Build("proj-1", nil, nil, []Declaration{
			{Type: resourcesv1.ResourceType_RESOURCE_TYPE_UNSPECIFIED, ID: "main"},
		}, nil, nil)
		if err == nil {
			t.Fatal("Build: expected error for unsupported resource type, got nil")
		}
	})

	t.Run("refuses an empty id", func(t *testing.T) {
		t.Parallel()

		_, err := Build("proj-1", nil, nil, []Declaration{
			{Type: resourcesv1.ResourceType_RESOURCE_TYPE_POSTGRES, ID: ""},
		}, nil, nil)
		if err == nil {
			t.Fatal("Build: expected error for empty id, got nil")
		}
	})

	t.Run("carries domains", func(t *testing.T) {
		t.Parallel()

		domains := map[string][]string{"production": {"app.acme.com", "www.acme.com"}}
		manifest, err := Build("proj-1", domains, nil, nil, nil, nil)
		if err != nil {
			t.Fatalf("Build: %v", err)
		}
		if got := manifest.GetDomains()["production"].GetHostnames(); len(got) != 2 || got[0] != "app.acme.com" || got[1] != "www.acme.com" {
			t.Fatalf("Domains[production] = %v, want [app.acme.com www.acme.com]", got)
		}
	})

	t.Run("normalizes a function logical name", func(t *testing.T) {
		t.Parallel()

		manifest, err := Build("proj-1", nil, nil, nil, []Function{
			{Name: "Web API", App: "web", Runtime: "nodejs24.x", Handler: "app/api.ts", ArtifactPath: "dist/api.zip", Framework: "express"},
		}, nil)
		if err != nil {
			t.Fatalf("Build: %v", err)
		}
		if len(manifest.GetFunctions()) != 1 {
			t.Fatalf("got %d functions, want 1", len(manifest.GetFunctions()))
		}
		if got := manifest.GetFunctions()[0].GetLogicalName(); got != "fn--web--web-api" {
			t.Fatalf("logical_name = %q, want %q", got, "fn--web--web-api")
		}
	})

	t.Run("carries a function route id distinct from its logical name", func(t *testing.T) {
		t.Parallel()

		manifest, err := Build("proj-1", nil, nil, nil, []Function{
			{Name: "api/documents", App: "web", Runtime: "nodejs24.x", Handler: "route.js", ArtifactPath: "functions/api/documents.func", Framework: "next", RouteID: "/api/documents"},
		}, nil)
		if err != nil {
			t.Fatalf("Build: %v", err)
		}
		fn := manifest.GetFunctions()[0]
		if got, want := fn.GetLogicalName(), "fn--web--api-documents"; got != want {
			t.Fatalf("logical_name = %q, want %q", got, want)
		}
		if got, want := fn.GetRouteId(), "/api/documents"; got != want {
			t.Fatalf("route_id = %q, want %q (must be preserved verbatim, not normalized)", got, want)
		}
	})

	t.Run("carries apps sorted by name", func(t *testing.T) {
		t.Parallel()

		apps := []App{
			{Name: "web", Framework: "next", Domains: map[string][]string{"production": {"example.com"}}},
			{Name: "admin", Framework: "express"},
		}
		manifest, err := Build("proj-1", nil, apps, nil, []Function{
			{Name: "web", Runtime: "nodejs24.x", Handler: "h.js", ArtifactPath: "a", Framework: "next", App: "web"},
			{Name: "admin", Runtime: "nodejs24.x", Handler: "h.js", ArtifactPath: "b", Framework: "express", App: "admin"},
		}, nil)
		if err != nil {
			t.Fatalf("Build: %v", err)
		}

		got := manifest.GetApps()
		if len(got) != 2 {
			t.Fatalf("got %d apps, want 2", len(got))
		}
		if got[0].GetName() != "admin" || got[1].GetName() != "web" {
			t.Fatalf("apps = [%q %q], want sorted [admin web]", got[0].GetName(), got[1].GetName())
		}
		if got[1].GetFramework() != "next" {
			t.Fatalf("web framework = %q, want %q", got[1].GetFramework(), "next")
		}
		if got := got[1].GetDomains()["production"].GetHostnames(); len(got) != 1 || got[0] != "example.com" {
			t.Fatalf("web production domain = %v, want [example.com]", got)
		}
		if len(got[0].GetDomains()) != 0 {
			t.Fatalf("admin domains = %v, want empty", got[0].GetDomains())
		}
	})

	t.Run("records the app a function belongs to", func(t *testing.T) {
		t.Parallel()

		manifest, err := Build("proj-1", nil, []App{{Name: "web", Framework: "express"}}, nil, []Function{
			{Name: "web", Runtime: "nodejs24.x", Handler: "h.js", ArtifactPath: "a", Framework: "express", App: "web"},
		}, nil)
		if err != nil {
			t.Fatalf("Build: %v", err)
		}
		if got := manifest.GetFunctions()[0].GetApp(); got != "web" {
			t.Fatalf("function app = %q, want %q", got, "web")
		}
	})

	t.Run("synthesizes an app from functions when none is configured", func(t *testing.T) {
		t.Parallel()

		manifest, err := Build("proj-1", nil, nil, nil, []Function{
			{Name: "api/documents", Runtime: "nodejs24.x", Handler: "h.js", ArtifactPath: "a", Framework: "next", App: "storefront"},
			{Name: "index", Runtime: "nodejs24.x", Handler: "h.js", ArtifactPath: "b", Framework: "next", App: "storefront"},
		}, nil)
		if err != nil {
			t.Fatalf("Build: %v", err)
		}
		apps := manifest.GetApps()
		if len(apps) != 1 {
			t.Fatalf("got %d apps, want exactly 1", len(apps))
		}
		if apps[0].GetName() != "storefront" {
			t.Fatalf("app name = %q, want %q", apps[0].GetName(), "storefront")
		}
		if apps[0].GetFramework() != "next" {
			t.Fatalf("app framework = %q, want %q", apps[0].GetFramework(), "next")
		}
	})

	t.Run("fills a configured app's framework from its functions", func(t *testing.T) {
		t.Parallel()

		manifest, err := Build("proj-1", nil, []App{{Name: "web"}}, nil, []Function{
			{Name: "web", Runtime: "nodejs24.x", Handler: "h.js", ArtifactPath: "a", Framework: "express", App: "web"},
		}, nil)
		if err != nil {
			t.Fatalf("Build: %v", err)
		}
		if got := manifest.GetApps()[0].GetFramework(); got != "express" {
			t.Fatalf("app framework = %q, want %q", got, "express")
		}
	})

	t.Run("a configured app with no functions still appears", func(t *testing.T) {
		t.Parallel()

		manifest, err := Build("proj-1", nil, []App{{Name: "web", Framework: "express"}}, nil, nil, nil)
		if err != nil {
			t.Fatalf("Build: %v", err)
		}
		if len(manifest.GetApps()) != 1 || manifest.GetApps()[0].GetName() != "web" {
			t.Fatalf("apps = %v, want one app named web", manifest.GetApps())
		}
	})

	t.Run("no apps and no functions yields no apps", func(t *testing.T) {
		t.Parallel()

		manifest, err := Build("proj-1", nil, nil, synthDeclarations(), nil, nil)
		if err != nil {
			t.Fatalf("Build: %v", err)
		}
		if len(manifest.GetApps()) != 0 {
			t.Fatalf("apps = %v, want none", manifest.GetApps())
		}
	})

	t.Run("carries each app's own resolved variables", func(t *testing.T) {
		t.Parallel()

		variables := map[string][]Variable{
			"admin": {
				{Key: "STRIPE_API_KEY", Class: resourcesv1.VariableClass_VARIABLE_CLASS_SENSITIVE, Value: "sk-admin"},
				{Key: "POSTHOG_ID", Class: resourcesv1.VariableClass_VARIABLE_CLASS_PLAIN, Value: "ph-admin"},
			},
			"storefront": {
				{Key: "POSTHOG_ID", Class: resourcesv1.VariableClass_VARIABLE_CLASS_PLAIN, Value: "ph-store"},
			},
		}
		manifest, err := Build("proj-1", nil, []App{{Name: "admin", Framework: "express"}}, nil, []Function{
			{Name: "index", Runtime: "nodejs24.x", Handler: "h.js", ArtifactPath: "a", Framework: "next", App: "storefront"},
		}, variables)
		if err != nil {
			t.Fatalf("Build: %v", err)
		}

		byName := map[string][]*deploymentsv1.ManifestVariable{}
		for _, app := range manifest.GetApps() {
			byName[app.GetName()] = app.GetVariables()
		}

		admin := byName["admin"]
		if len(admin) != 2 {
			t.Fatalf("admin carries %d variables, want 2", len(admin))
		}
		if admin[0].GetKey() != "POSTHOG_ID" || admin[1].GetKey() != "STRIPE_API_KEY" {
			t.Errorf("admin variables = %s, %s, want them sorted by key", admin[0].GetKey(), admin[1].GetKey())
		}
		if admin[0].GetValue() != "ph-admin" {
			t.Errorf("admin POSTHOG_ID = %q, want its own resolution", admin[0].GetValue())
		}
		if admin[1].GetClass() != resourcesv1.VariableClass_VARIABLE_CLASS_SENSITIVE {
			t.Errorf("admin lost the class that decides delivery: %v", admin[1].GetClass())
		}

		store := byName["storefront"]
		if len(store) != 1 || store[0].GetValue() != "ph-store" {
			t.Fatalf("storefront variables = %v, want only its own POSTHOG_ID", store)
		}
	})

	t.Run("carries the app's folder binding", func(t *testing.T) {
		t.Parallel()

		manifest, err := Build("proj-1", nil, []App{
			{Name: "admin", Framework: "express", Folder: "/admin"},
			{Name: "web", Framework: "express"},
		}, nil, nil, nil)
		if err != nil {
			t.Fatalf("Build: %v", err)
		}

		byName := map[string]string{}
		for _, app := range manifest.GetApps() {
			byName[app.GetName()] = app.GetFolder()
		}
		if got, want := byName["admin"], "/admin"; got != want {
			t.Errorf("admin folder = %q, want %q", got, want)
		}
		if got := byName["web"]; got != "" {
			t.Errorf("web folder = %q, want the empty root binding", got)
		}
	})

	t.Run("carries each variable's resolved folder", func(t *testing.T) {
		t.Parallel()

		variables := map[string][]Variable{
			"admin": {
				{Key: "SCOPED_KEY", Class: resourcesv1.VariableClass_VARIABLE_CLASS_SECRET, Folder: "/admin"},
				{Key: "ROOT_KEY", Class: resourcesv1.VariableClass_VARIABLE_CLASS_SECRET},
			},
		}
		manifest, err := Build("proj-1", nil, []App{{Name: "admin", Framework: "express", Folder: "/admin"}}, nil, nil, variables)
		if err != nil {
			t.Fatalf("Build: %v", err)
		}

		byKey := map[string]string{}
		for _, app := range manifest.GetApps() {
			for _, v := range app.GetVariables() {
				byKey[v.GetKey()] = v.GetFolder()
			}
		}
		if got, want := byKey["SCOPED_KEY"], "/admin"; got != want {
			t.Errorf("SCOPED_KEY folder = %q, want %q", got, want)
		}
		if got := byKey["ROOT_KEY"]; got != "" {
			t.Errorf("ROOT_KEY folder = %q, want the empty root spelling — the store's %q sentinel is never written above the store", got, "/")
		}
	})
}

func TestLogicalNames(t *testing.T) {
	t.Parallel()

	t.Run("resources read as kind and name", func(t *testing.T) {
		t.Parallel()

		cases := []struct {
			kind naming.Kind
			id   string
			want string
		}{
			{naming.KindDatabase, "main", "db--main"},
			{naming.KindDatabase, "My DB!", "db--my-db"},
			{naming.KindDatabase, "api-v2.prod", "db--api-v2-prod"},
			{naming.KindBucket, "UPLOADS", "bucket--uploads"},
		}
		for _, c := range cases {
			if got := resourceLogicalName(c.kind, c.id); got != c.want {
				t.Errorf("resourceLogicalName(%q, %q) = %q, want %q", c.kind, c.id, got, c.want)
			}
		}
	})

	t.Run("functions keep the app on its own field", func(t *testing.T) {
		t.Parallel()

		cases := []struct {
			app   string
			route string
			want  string
		}{
			{"web", "index", "fn--web--index"},
			{"web", "api/users", "fn--web--api-users"},
			{"web-api", "users", "fn--web-api--users"},
			{"web", "api/todos/[id]", "fn--web--api-todos-id"},
		}
		for _, c := range cases {
			got := functionLogicalName(c.app, c.route)
			if got != c.want {
				t.Errorf("functionLogicalName(%q, %q) = %q, want %q", c.app, c.route, got, c.want)
			}
			if fields := strings.Split(got, naming.FieldSeparator); len(fields) != 3 {
				t.Errorf("%q splits into %d fields on %q, want kind, app and route", got, len(fields), naming.FieldSeparator)
			}
		}
	})
}

func TestManifestVariables(t *testing.T) {
	t.Parallel()

	t.Run("lowers the resolved version", func(t *testing.T) {
		t.Parallel()

		got := manifestVariables([]Variable{
			{Key: "VERSIONED", Class: resourcesv1.VariableClass_VARIABLE_CLASS_SENSITIVE, Value: "v", Version: 4},
			{Key: "UNVERSIONED", Class: resourcesv1.VariableClass_VARIABLE_CLASS_PLAIN, Value: "u"},
		})
		if len(got) != 2 {
			t.Fatalf("manifestVariables = %v, want both", got)
		}
		if got[0].GetKey() != "UNVERSIONED" || got[0].GetVersion() != 0 {
			t.Errorf("UNVERSIONED = %v, want version 0", got[0])
		}
		if got[1].GetKey() != "VERSIONED" || got[1].GetVersion() != 4 {
			t.Errorf("VERSIONED = %v, want the version it resolved at", got[1])
		}
	})
}
