package localharness

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ocelhq/ocel/internal/manifest"
	"github.com/ocelhq/ocel/internal/provision"
	resourcesv1 "github.com/ocelhq/ocel/pkg/proto/resources/v1"
)

func TestClient_FetchProjectConfig_SpeaksHandshakeProtocol(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/project-config", func(w http.ResponseWriter, r *http.Request) {
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

	client := NewClient(ts.URL)
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

	client := NewClient(ts.URL)
	_, err := client.FetchProjectConfig(context.Background(), "https://api.example.com", "tok", "proj_1")
	if err == nil {
		t.Fatal("FetchProjectConfig: expected error, got nil")
	}
}

func TestClient_Provision_SpeaksHandshakeProtocol(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/provision", func(w http.ResponseWriter, r *http.Request) {
		var req provisionRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if req.ProjectConfig.ProjectID != "proj_1" {
			t.Fatalf("ProjectConfig.ProjectID = %q, want %q", req.ProjectConfig.ProjectID, "proj_1")
		}
		if len(req.Resources) != 1 || req.Resources[0].Name != "main" || req.Resources[0].Type != "POSTGRES" {
			t.Fatalf("Resources = %+v", req.Resources)
		}
		_ = json.NewEncoder(w).Encode([]provisionedResourceWire{
			{Name: "main", Type: "POSTGRES", Env: map[string]string{
				"OCEL_RESOURCE_POSTGRES_main": `{"connectionString":"postgres://x"}`,
			}},
		})
	})
	ts := httptest.NewServer(mux)
	defer ts.Close()

	client := NewClient(ts.URL)
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

	client := NewClient(ts.URL)
	_, err := client.Provision(context.Background(), provision.ProjectConfig{ProjectID: "proj_1"}, nil)
	if err == nil {
		t.Fatal("Provision: expected error, got nil")
	}
}
