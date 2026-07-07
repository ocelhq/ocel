package provision

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

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
	if cfg.APIURL != "https://api.example.com" {
		t.Fatalf("APIURL = %q, want %q", cfg.APIURL, "https://api.example.com")
	}
	if cfg.Token != "tok_123" {
		t.Fatalf("Token = %q, want %q", cfg.Token, "tok_123")
	}
}

func TestResourceTypeName_RendersCanonicalName(t *testing.T) {
	got, err := ResourceTypeName(resourcesv1.ResourceType_RESOURCE_TYPE_POSTGRES)
	if err != nil {
		t.Fatalf("ResourceTypeName: %v", err)
	}
	if got != "POSTGRES" {
		t.Fatalf("ResourceTypeName() = %q, want %q", got, "POSTGRES")
	}
}

func TestResourceTypeName_RejectsUnspecifiedType(t *testing.T) {
	_, err := ResourceTypeName(resourcesv1.ResourceType_RESOURCE_TYPE_UNSPECIFIED)
	if err == nil {
		t.Fatal("ResourceTypeName: expected error for unspecified type, got nil")
	}
}

func TestProvision_EmptyManifestYieldsNoResourcesWithoutCallingResolve(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("Provision made an HTTP request for an empty resource list")
	}))
	defer ts.Close()

	got, err := Provision(context.Background(), ProjectConfig{ProjectID: "proj_abc", APIURL: ts.URL}, nil)
	if err != nil {
		t.Fatalf("Provision: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("Provision() = %+v, want empty", got)
	}
}

func TestProvision_CallsResolveEndpointAndInjectsConnectionStringUnderCanonicalEnvKey(t *testing.T) {
	var gotAuth string
	mux := http.NewServeMux()
	mux.HandleFunc("/api/resources/resolve", func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")

		var req resolveRequestBody
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if req.ProjectID != "proj_abc" {
			t.Fatalf("ProjectID = %q, want %q", req.ProjectID, "proj_abc")
		}
		if len(req.Resources) != 1 || req.Resources[0].Name != "main" || req.Resources[0].Type != "POSTGRES" {
			t.Fatalf("Resources = %+v", req.Resources)
		}

		_ = json.NewEncoder(w).Encode(resolveResponseBody{
			Env: map[string]string{
				"OCEL_RESOURCE_POSTGRES_main": `{"connectionString":"postgres://resolved/main"}`,
			},
			ExpiresAt: time.Now().Add(time.Hour).Format(time.RFC3339),
		})
	})
	ts := httptest.NewServer(mux)
	defer ts.Close()

	resources := []manifest.Entry{
		{Name: "main", Type: resourcesv1.ResourceType_RESOURCE_TYPE_POSTGRES},
	}

	got, err := Provision(context.Background(), ProjectConfig{ProjectID: "proj_abc", APIURL: ts.URL, Token: "tok_123"}, resources)
	if err != nil {
		t.Fatalf("Provision: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("Provision() len = %d, want 1", len(got))
	}
	if gotAuth != "Bearer tok_123" {
		t.Fatalf("Authorization = %q, want %q", gotAuth, "Bearer tok_123")
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
	if parsed.ConnectionString != "postgres://resolved/main" {
		t.Fatalf("connectionString = %q, want %q", parsed.ConnectionString, "postgres://resolved/main")
	}
}

func TestProvision_PropagatesNonOKStatus(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer ts.Close()

	resources := []manifest.Entry{
		{Name: "main", Type: resourcesv1.ResourceType_RESOURCE_TYPE_POSTGRES},
	}
	_, err := Provision(context.Background(), ProjectConfig{ProjectID: "proj_abc", APIURL: ts.URL}, resources)
	if err == nil {
		t.Fatal("Provision: expected error, got nil")
	}
}

func TestProvision_MissingEnvKeyInResponseIsAnError(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(resolveResponseBody{Env: map[string]string{}})
	}))
	defer ts.Close()

	resources := []manifest.Entry{
		{Name: "main", Type: resourcesv1.ResourceType_RESOURCE_TYPE_POSTGRES},
	}
	_, err := Provision(context.Background(), ProjectConfig{ProjectID: "proj_abc", APIURL: ts.URL}, resources)
	if err == nil {
		t.Fatal("Provision: expected error for missing env key, got nil")
	}
}
