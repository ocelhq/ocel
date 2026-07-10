package manifestbuilder

import (
	"bytes"
	"encoding/json"
	"os"
	"strings"
	"testing"

	providerv1 "github.com/ocelhq/ocel/pkg/proto/provider/v1"
	resourcesv1 "github.com/ocelhq/ocel/pkg/proto/resources/v1"
)

const goldenPath = "testdata/golden_manifest.json"

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

func toGolden(m *providerv1.Manifest) goldenManifest {
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
	return g
}

func marshal(t *testing.T, m *providerv1.Manifest) []byte {
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

func TestBuild_GoldenFile_DeterministicOutput(t *testing.T) {
	first, err := Build("proj_1", synthDeclarations())
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	second, err := Build("proj_1", synthDeclarations())
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
	inOrder, err := Build("proj_1", declarations)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	reversed := []Declaration{declarations[1], declarations[0]}
	reorderedManifest, err := Build("proj_1", reversed)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	if !bytes.Equal(marshal(t, inOrder), marshal(t, reorderedManifest)) {
		t.Fatalf("reordering declarations changed manifest output")
	}
}

func TestBuild_AddResourceLeavesExistingLogicalNamesUnchanged(t *testing.T) {
	base := synthDeclarations()
	before, err := Build("proj_1", base)
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
	after, err := Build("proj_1", withExtra)
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
	manifest, err := Build("proj_1", []Declaration{
		{Type: resourcesv1.ResourceType_RESOURCE_TYPE_POSTGRES, ID: "main", Postgres: &resourcesv1.PostgresConfig{Version: "17"}, Source: "app/db.ts:5"},
	})
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

func TestBuild_DuplicateTypeAndID_NamesBothDeclarationsAndSources(t *testing.T) {
	_, err := Build("proj_1", []Declaration{
		{Type: resourcesv1.ResourceType_RESOURCE_TYPE_POSTGRES, ID: "main", Source: "app/db.ts:5"},
		{Type: resourcesv1.ResourceType_RESOURCE_TYPE_POSTGRES, ID: "main", Source: "app/other.ts:12"},
	})
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
	_, err := Build("proj_1", []Declaration{
		{Type: resourcesv1.ResourceType_RESOURCE_TYPE_UNSPECIFIED, ID: "main"},
	})
	if err == nil {
		t.Fatal("Build: expected error for unsupported resource type, got nil")
	}
}

func TestBuild_EmptyID(t *testing.T) {
	_, err := Build("proj_1", []Declaration{
		{Type: resourcesv1.ResourceType_RESOURCE_TYPE_POSTGRES, ID: ""},
	})
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
