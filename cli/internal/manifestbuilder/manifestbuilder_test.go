package manifestbuilder

import (
	"bytes"
	"encoding/json"
	"os"
	"strings"
	"testing"

	deploymentsv1 "github.com/ocelhq/ocel/pkg/proto/deployments/v1"
	resourcesv1 "github.com/ocelhq/ocel/pkg/proto/resources/v1"
)

const goldenPath = "testdata/golden_manifest.json"
const functionsGoldenPath = "testdata/golden_manifest_functions.json"

// protojson deliberately injects random whitespace into its output (see
// google.golang.org/protobuf/internal/encoding/json's detrand use) to stop
// callers from depending on byte-stable JSON, so it can't back a
// byte-identical golden test. goldenManifest is a plain mirror of the
// Manifest shape, serialized with the standard library's deterministic
// encoding/json instead, purely for this test's comparisons.
type goldenManifest struct {
	SchemaVersion string           `json:"schema_version"`
	ProjectID     string           `json:"project_id"`
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
	g := goldenManifest{SchemaVersion: m.GetSchemaVersion(), ProjectID: m.GetProjectId()}
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
		{Name: "api/documents", Runtime: "nodejs24.x", Handler: "app/api.ts", ArtifactPath: "dist/api.zip", Framework: "next", RouteID: "/api/documents"},
		{Name: "worker", Runtime: "nodejs24.x", Handler: "app/worker.ts", ArtifactPath: "dist/worker.zip", Framework: ""},
	}
}

func TestBuild_CarriesProjectIdAndSlug(t *testing.T) {
	manifest, err := Build("proj_1", "acme-web", nil, nil, nil, nil)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if manifest.GetProjectId() != "proj_1" || manifest.GetSlug() != "acme-web" {
		t.Errorf("projectId/slug = %q/%q, want proj_1/acme-web", manifest.GetProjectId(), manifest.GetSlug())
	}
}

func TestBuild_GoldenFile_DeterministicOutput(t *testing.T) {
	first, err := Build("proj_1", "proj-1", nil, nil, synthDeclarations(), nil)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	second, err := Build("proj_1", "proj-1", nil, nil, synthDeclarations(), nil)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	firstJSON := marshal(t, first)
	secondJSON := marshal(t, second)
	if !bytes.Equal(firstJSON, secondJSON) {
		t.Fatalf("Build is not deterministic:\nfirst:\n%s\nsecond:\n%s", firstJSON, secondJSON)
	}

	golden, err := os.ReadFile(goldenPath)
	if err != nil {
		t.Fatalf("read golden file: %v", err)
	}
	if !bytes.Equal(firstJSON, golden) {
		t.Fatalf("Build output does not match golden file %s:\ngot:\n%s\nwant:\n%s", goldenPath, firstJSON, golden)
	}
}

func TestBuild_ReorderInvariance(t *testing.T) {
	declarations := synthDeclarations()
	inOrder, err := Build("proj_1", "proj-1", nil, nil, declarations, nil)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	reversed := []Declaration{declarations[1], declarations[0]}
	reorderedManifest, err := Build("proj_1", "proj-1", nil, nil, reversed, nil)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	if !bytes.Equal(marshal(t, inOrder), marshal(t, reorderedManifest)) {
		t.Fatalf("reordering declarations changed manifest output")
	}
}

func TestBuild_AddResourceLeavesExistingLogicalNamesUnchanged(t *testing.T) {
	base := synthDeclarations()
	before, err := Build("proj_1", "proj-1", nil, nil, base, nil)
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
	after, err := Build("proj_1", "proj-1", nil, nil, withExtra, nil)
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
}

func TestBuild_TypedConfigRoundTripsAsOneof(t *testing.T) {
	manifest, err := Build("proj_1", "proj-1", nil, nil, []Declaration{
		{Type: resourcesv1.ResourceType_RESOURCE_TYPE_POSTGRES, ID: "main", Postgres: &resourcesv1.PostgresConfig{Version: "17"}, Source: "app/db.ts:5"},
	}, nil)
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
}

func TestBuild_BucketConfigRoundTripsAsOneof(t *testing.T) {
	manifest, err := Build("proj_1", "proj-1", nil, nil, []Declaration{
		{Type: resourcesv1.ResourceType_RESOURCE_TYPE_BUCKET, ID: "storage", Bucket: &resourcesv1.BucketConfig{AllowedOrigins: []string{"https://app.example.com"}}, Source: "app/storage.ts:3"},
	}, nil)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if len(manifest.GetResources()) != 1 {
		t.Fatalf("got %d resources, want 1", len(manifest.GetResources()))
	}

	resource := manifest.GetResources()[0]
	if resource.GetLogicalName() != "bucket_storage" {
		t.Fatalf("logical_name = %q, want %q", resource.GetLogicalName(), "bucket_storage")
	}
	bucket := resource.GetBucket()
	if bucket == nil {
		t.Fatalf("resource.GetBucket() = nil, want typed BucketConfig")
	}
	if got := bucket.GetAllowedOrigins(); len(got) != 1 || got[0] != "https://app.example.com" {
		t.Fatalf("bucket.AllowedOrigins = %v, want [https://app.example.com]", got)
	}
}

func TestBuild_DuplicateTypeAndID_NamesBothDeclarationsAndSources(t *testing.T) {
	_, err := Build("proj_1", "proj-1", nil, nil, []Declaration{
		{Type: resourcesv1.ResourceType_RESOURCE_TYPE_POSTGRES, ID: "main", Source: "app/db.ts:5"},
		{Type: resourcesv1.ResourceType_RESOURCE_TYPE_POSTGRES, ID: "main", Source: "app/other.ts:12"},
	}, nil)
	if err == nil {
		t.Fatal("Build: expected duplicate error, got nil")
	}

	dupErr, ok := err.(*DuplicateError)
	if !ok {
		t.Fatalf("Build error = %T, want *DuplicateError", err)
	}
	if dupErr.TypeToken != "postgres" || dupErr.ID != "main" {
		t.Fatalf("DuplicateError = %+v, want type=postgres id=main", dupErr)
	}
	if dupErr.FirstSource != "app/db.ts:5" || dupErr.SecondSource != "app/other.ts:12" {
		t.Fatalf("DuplicateError sources = %q, %q, want both offending source locations", dupErr.FirstSource, dupErr.SecondSource)
	}

	msg := dupErr.Error()
	for _, want := range []string{"postgres", "main", "app/db.ts:5", "app/other.ts:12"} {
		if !strings.Contains(msg, want) {
			t.Fatalf("error message %q does not contain %q", msg, want)
		}
	}
}

func TestBuild_UnsupportedResourceType(t *testing.T) {
	_, err := Build("proj_1", "proj-1", nil, nil, []Declaration{
		{Type: resourcesv1.ResourceType_RESOURCE_TYPE_UNSPECIFIED, ID: "main"},
	}, nil)
	if err == nil {
		t.Fatal("Build: expected error for unsupported resource type, got nil")
	}
}

func TestBuild_EmptyID(t *testing.T) {
	_, err := Build("proj_1", "proj-1", nil, nil, []Declaration{
		{Type: resourcesv1.ResourceType_RESOURCE_TYPE_POSTGRES, ID: ""},
	}, nil)
	if err == nil {
		t.Fatal("Build: expected error for empty id, got nil")
	}
}

func TestNormalizeLogicalName(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"postgres_main", "postgres_main"},
		{"postgres_My DB!", "postgres_my_db_"},
		{"postgres_api-v2.prod", "postgres_api_v2_prod"},
		{"postgres_ALLCAPS", "postgres_allcaps"},
	}
	for _, c := range cases {
		if got := normalizeLogicalName(c.in); got != c.want {
			t.Errorf("normalizeLogicalName(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestBuild_FunctionsGoldenFile_DeterministicOutput(t *testing.T) {
	first, err := Build("proj_1", "proj-1", nil, nil, synthDeclarations(), synthFunctions())
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	second, err := Build("proj_1", "proj-1", nil, nil, synthDeclarations(), synthFunctions())
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	firstJSON := marshal(t, first)
	secondJSON := marshal(t, second)
	if !bytes.Equal(firstJSON, secondJSON) {
		t.Fatalf("Build is not deterministic:\nfirst:\n%s\nsecond:\n%s", firstJSON, secondJSON)
	}

	golden, err := os.ReadFile(functionsGoldenPath)
	if err != nil {
		t.Fatalf("read golden file: %v", err)
	}
	if !bytes.Equal(firstJSON, golden) {
		t.Fatalf("Build output does not match golden file %s:\ngot:\n%s\nwant:\n%s", functionsGoldenPath, firstJSON, golden)
	}
}

func TestBuild_FunctionsReorderInvariance(t *testing.T) {
	functions := synthFunctions()
	inOrder, err := Build("proj_1", "proj-1", nil, nil, nil, functions)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	reversed := []Function{functions[1], functions[0]}
	reordered, err := Build("proj_1", "proj-1", nil, nil, nil, reversed)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	if !bytes.Equal(marshal(t, inOrder), marshal(t, reordered)) {
		t.Fatalf("reordering functions changed manifest output")
	}
}

func TestBuild_CarriesDomains(t *testing.T) {
	domains := map[string]string{"production": "app.acme.com"}
	manifest, err := Build("proj_1", "proj-1", domains, nil, nil, nil)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if got := manifest.GetDomains()["production"]; got != "app.acme.com" {
		t.Fatalf("Domains[production] = %q, want %q", got, "app.acme.com")
	}
}

func TestBuild_FunctionLogicalNameNormalized(t *testing.T) {
	manifest, err := Build("proj_1", "proj-1", nil, nil, nil, []Function{
		{Name: "Web API", Runtime: "nodejs24.x", Handler: "app/api.ts", ArtifactPath: "dist/api.zip", Framework: "express"},
	})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if len(manifest.GetFunctions()) != 1 {
		t.Fatalf("got %d functions, want 1", len(manifest.GetFunctions()))
	}
	if got := manifest.GetFunctions()[0].GetLogicalName(); got != "web_api" {
		t.Fatalf("logical_name = %q, want %q", got, "web_api")
	}
}

func TestBuild_FunctionRouteIDCarriedDistinctFromLogicalName(t *testing.T) {
	manifest, err := Build("proj_1", "proj-1", nil, nil, nil, []Function{
		{Name: "api/documents", Runtime: "nodejs24.x", Handler: "route.js", ArtifactPath: "functions/api/documents.func", Framework: "next", RouteID: "/api/documents"},
	})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	fn := manifest.GetFunctions()[0]
	if got, want := fn.GetLogicalName(), "api_documents"; got != want {
		t.Fatalf("logical_name = %q, want %q", got, want)
	}
	if got, want := fn.GetRouteId(), "/api/documents"; got != want {
		t.Fatalf("route_id = %q, want %q (must be preserved verbatim, not normalized)", got, want)
	}
}

func TestBuild_CarriesAppsSortedByName(t *testing.T) {
	apps := []App{
		{Name: "web", Framework: "next", Domains: map[string]string{"production": "example.com"}},
		{Name: "admin", Framework: "express"},
	}
	manifest, err := Build("proj_1", "proj-1", nil, apps, nil, []Function{
		{Name: "web", Runtime: "nodejs24.x", Handler: "h.js", ArtifactPath: "a", Framework: "next", App: "web"},
		{Name: "admin", Runtime: "nodejs24.x", Handler: "h.js", ArtifactPath: "b", Framework: "express", App: "admin"},
	})
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
	if got[1].GetDomains()["production"] != "example.com" {
		t.Fatalf("web production domain = %q, want %q", got[1].GetDomains()["production"], "example.com")
	}
	if len(got[0].GetDomains()) != 0 {
		t.Fatalf("admin domains = %v, want empty", got[0].GetDomains())
	}
}

func TestBuild_FunctionRecordsOwningApp(t *testing.T) {
	manifest, err := Build("proj_1", "proj-1", nil, []App{{Name: "web", Framework: "express"}}, nil, []Function{
		{Name: "web", Runtime: "nodejs24.x", Handler: "h.js", ArtifactPath: "a", Framework: "express", App: "web"},
	})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if got := manifest.GetFunctions()[0].GetApp(); got != "web" {
		t.Fatalf("function app = %q, want %q", got, "web")
	}
}

// A project that configures no apps still deploys: the builder detects one at
// the project root and names it, and that name must reach the manifest.
func TestBuild_SynthesizesAppFromFunctionsWhenNoneConfigured(t *testing.T) {
	manifest, err := Build("proj_1", "proj-1", nil, nil, nil, []Function{
		{Name: "api/documents", Runtime: "nodejs24.x", Handler: "h.js", ArtifactPath: "a", Framework: "next", App: "storefront"},
		{Name: "index", Runtime: "nodejs24.x", Handler: "h.js", ArtifactPath: "b", Framework: "next", App: "storefront"},
	})
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
}

// Config may omit framework and let the builder detect it; the manifest app
// should still report the framework its functions were built with.
func TestBuild_ConfiguredAppFrameworkFilledFromItsFunctions(t *testing.T) {
	manifest, err := Build("proj_1", "proj-1", nil, []App{{Name: "web"}}, nil, []Function{
		{Name: "web", Runtime: "nodejs24.x", Handler: "h.js", ArtifactPath: "a", Framework: "express", App: "web"},
	})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if got := manifest.GetApps()[0].GetFramework(); got != "express" {
		t.Fatalf("app framework = %q, want %q", got, "express")
	}
}

// A configured app that emits no functions is still part of the project.
func TestBuild_ConfiguredAppWithNoFunctionsStillAppears(t *testing.T) {
	manifest, err := Build("proj_1", "proj-1", nil, []App{{Name: "web", Framework: "express"}}, nil, nil)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if len(manifest.GetApps()) != 1 || manifest.GetApps()[0].GetName() != "web" {
		t.Fatalf("apps = %v, want one app named web", manifest.GetApps())
	}
}

func TestBuild_NoAppsAndNoFunctionsYieldsNoApps(t *testing.T) {
	manifest, err := Build("proj_1", "proj-1", nil, nil, synthDeclarations(), nil)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if len(manifest.GetApps()) != 0 {
		t.Fatalf("apps = %v, want none", manifest.GetApps())
	}
}
