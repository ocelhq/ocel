package provision

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/ocelhq/ocel/internal/manifest"
	resourcesv1 "github.com/ocelhq/ocel/pkg/proto/resources/v1"
)

func TestFetchProjectConfig_ReturnsIdentityForProjectID(t *testing.T) {
	cfg, err := FetchProjectConfig(context.Background(), "https://api.example.com", "tok_123", "proj_abc")
	if err != nil {
		t.Fatalf("FetchProjectConfig: %v", err)
	}
	if cfg.ProjectID != "proj_abc" {
		t.Fatalf("ProjectID = %q, want %q", cfg.ProjectID, "proj_abc")
	}
	if cfg.OrgID == "" {
		t.Fatal("OrgID is empty")
	}
	if cfg.UserID == "" {
		t.Fatal("UserID is empty")
	}
}

func TestProvision_EmptyManifestYieldsNoResources(t *testing.T) {
	got, err := Provision(context.Background(), ProjectConfig{ProjectID: "proj_abc"}, nil)
	if err != nil {
		t.Fatalf("Provision: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("Provision() = %+v, want empty", got)
	}
}

func TestProvision_InjectsConnectionStringUnderCanonicalEnvKey(t *testing.T) {
	resources := []manifest.Entry{
		{Name: "main", Type: resourcesv1.ResourceType_RESOURCE_TYPE_POSTGRES},
	}

	got, err := Provision(context.Background(), ProjectConfig{ProjectID: "proj_abc"}, resources)
	if err != nil {
		t.Fatalf("Provision: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("Provision() len = %d, want 1", len(got))
	}

	resource := got[0]
	if resource.Name != "main" {
		t.Fatalf("Name = %q, want %q", resource.Name, "main")
	}
	if resource.Type != resourcesv1.ResourceType_RESOURCE_TYPE_POSTGRES {
		t.Fatalf("Type = %v, want POSTGRES", resource.Type)
	}

	const key = "OCEL_RESOURCE_POSTGRES_main"
	raw, ok := resource.Env[key]
	if !ok {
		t.Fatalf("Env missing key %q, got %+v", key, resource.Env)
	}

	var parsed struct {
		ConnectionString string `json:"connectionString"`
	}
	if err := json.Unmarshal([]byte(raw), &parsed); err != nil {
		t.Fatalf("Env[%q] = %q is not valid JSON: %v", key, raw, err)
	}
	if parsed.ConnectionString == "" {
		t.Fatalf("connectionString is empty")
	}
}
