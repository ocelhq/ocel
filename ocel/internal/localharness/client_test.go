package localharness

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/ocelhq/ocel/internal/manifest"
	"github.com/ocelhq/ocel/internal/provision"
	resourcesv1 "github.com/ocelhq/ocel/pkg/proto/resources/v1"
)

func TestClient_FetchProjectConfig_SpeaksHandshakeProtocol(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/dev/project-config", func(w http.ResponseWriter, r *http.Request) {
		var req projectConfigRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if req.ProjectID != "proj_1" {
			t.Fatalf("ProjectID = %q, want %q", req.ProjectID, "proj_1")
		}
		_ = json.NewEncoder(w).Encode(projectConfigResponse{
			OrgID:     "org_1",
			ProjectID: req.ProjectID,
			UserID:    "user_1",
			EnvVars:   map[string]string{"FOO": "bar"},
		})
	})
	ts := httptest.NewServer(mux)
	defer ts.Close()

	client := NewClient(ts.URL, "tok")
	cfg, err := client.FetchProjectConfig(context.Background(), "https://api.example.com", "tok", "proj_1")
	if err != nil {
		t.Fatalf("FetchProjectConfig: %v", err)
	}
	if cfg.OrgID != "org_1" || cfg.ProjectID != "proj_1" || cfg.UserID != "user_1" {
		t.Fatalf("cfg = %+v", cfg)
	}
	if cfg.EnvVars["FOO"] != "bar" {
		t.Fatalf("EnvVars = %+v", cfg.EnvVars)
	}
}

func TestClient_FetchProjectConfig_PropagatesNonOKStatus(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer ts.Close()

	client := NewClient(ts.URL, "tok")
	_, err := client.FetchProjectConfig(context.Background(), "https://api.example.com", "tok", "proj_1")
	if err == nil {
		t.Fatal("FetchProjectConfig: expected error, got nil")
	}
}

func TestClient_Provision_SpeaksResolveProtocol(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/resources/resolve", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			ProjectID string `json:"projectId"`
			Resources []struct {
				Name string `json:"name"`
				Type string `json:"type"`
			} `json:"resources"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if req.ProjectID != "proj_1" {
			t.Fatalf("ProjectID = %q, want %q", req.ProjectID, "proj_1")
		}
		if len(req.Resources) != 1 || req.Resources[0].Name != "main" || req.Resources[0].Type != "POSTGRES" {
			t.Fatalf("Resources = %+v", req.Resources)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"env": map[string]string{
				"OCEL_RESOURCE_POSTGRES_main": `{"connectionString":"postgres://x"}`,
			},
			"expiresAt": time.Now().Add(time.Hour).Format(time.RFC3339),
		})
	})
	ts := httptest.NewServer(mux)
	defer ts.Close()

	client := NewClient(ts.URL, "tok")
	resources := []manifest.Entry{{Name: "main", Type: resourcesv1.ResourceType_RESOURCE_TYPE_POSTGRES}}
	got, err := client.Provision(context.Background(), provision.ProjectConfig{ProjectID: "proj_1"}, resources)
	if err != nil {
		t.Fatalf("Provision: %v", err)
	}
	if len(got) != 1 || got[0].Name != "main" || got[0].Type != resourcesv1.ResourceType_RESOURCE_TYPE_POSTGRES {
		t.Fatalf("got = %+v", got)
	}
	if got[0].Env["OCEL_RESOURCE_POSTGRES_main"] == "" {
		t.Fatal("Env missing key OCEL_RESOURCE_POSTGRES_main")
	}
}

func TestClient_Provision_PropagatesNonOKStatus(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer ts.Close()

	client := NewClient(ts.URL, "tok")
	resources := []manifest.Entry{{Name: "main", Type: resourcesv1.ResourceType_RESOURCE_TYPE_POSTGRES}}
	_, err := client.Provision(context.Background(), provision.ProjectConfig{ProjectID: "proj_1"}, resources)
	if err == nil {
		t.Fatal("Provision: expected error, got nil")
	}
}

func TestClient_Provision_EmptyResourcesSkipsRequest(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("Provision made an HTTP request for an empty resource list")
	}))
	defer ts.Close()

	client := NewClient(ts.URL, "tok")
	got, err := client.Provision(context.Background(), provision.ProjectConfig{ProjectID: "proj_1"}, nil)
	if err != nil {
		t.Fatalf("Provision: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("got = %+v, want empty", got)
	}
}

func TestClient_SendsBearerTokenOnBothEndpoints(t *testing.T) {
	var gotAuth []string
	mux := http.NewServeMux()
	mux.HandleFunc("/dev/project-config", func(w http.ResponseWriter, r *http.Request) {
		gotAuth = append(gotAuth, r.Header.Get("Authorization"))
		_ = json.NewEncoder(w).Encode(projectConfigResponse{ProjectID: "proj_1"})
	})
	mux.HandleFunc("/api/resources/resolve", func(w http.ResponseWriter, r *http.Request) {
		gotAuth = append(gotAuth, r.Header.Get("Authorization"))
		_ = json.NewEncoder(w).Encode(map[string]any{
			"env": map[string]string{
				"OCEL_RESOURCE_POSTGRES_main": `{"connectionString":"postgres://x"}`,
			},
			"expiresAt": time.Now().Add(time.Hour).Format(time.RFC3339),
		})
	})
	ts := httptest.NewServer(mux)
	defer ts.Close()

	client := NewClient(ts.URL, "tok_secret")
	if _, err := client.FetchProjectConfig(context.Background(), "https://api.example.com", "tok_secret", "proj_1"); err != nil {
		t.Fatalf("FetchProjectConfig: %v", err)
	}
	resources := []manifest.Entry{{Name: "main", Type: resourcesv1.ResourceType_RESOURCE_TYPE_POSTGRES}}
	if _, err := client.Provision(context.Background(), provision.ProjectConfig{ProjectID: "proj_1"}, resources); err != nil {
		t.Fatalf("Provision: %v", err)
	}

	want := []string{"Bearer tok_secret", "Bearer tok_secret"}
	if len(gotAuth) != 2 || gotAuth[0] != want[0] || gotAuth[1] != want[1] {
		t.Fatalf("Authorization headers = %q, want %q", gotAuth, want)
	}
}
