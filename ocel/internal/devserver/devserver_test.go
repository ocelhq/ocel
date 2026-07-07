package devserver

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ocelhq/ocel/internal/manifest"
	"github.com/ocelhq/ocel/internal/provision"
	resourcesv1 "github.com/ocelhq/ocel/pkg/proto/resources/v1"
	"github.com/ocelhq/ocel/pkg/proto/resources/v1/resourcesv1connect"
)

func TestDeclare_RejectsUnspecifiedResourceType(t *testing.T) {
	s := New("https://api.example.com", "tok", "proj_1")

	_, err := s.Declare(context.Background(), &resourcesv1.DeclareRequest{
		Resource: &resourcesv1.ResourceIdentifier{Name: "main"},
	})
	if err == nil {
		t.Fatal("Declare: expected error for unspecified resource type, got nil")
	}
}

func TestDeclareThenSync_ProvisionsDeclaredResource(t *testing.T) {
	s := New("https://api.example.com", "tok", "proj_1")
	ts := httptest.NewServer(s.Mux())
	defer ts.Close()

	client := resourcesv1connect.NewResourceServiceClient(http.DefaultClient, ts.URL)
	_, err := client.Declare(context.Background(), &resourcesv1.DeclareRequest{
		Resource: &resourcesv1.ResourceIdentifier{Name: "main", Type: resourcesv1.ResourceType_RESOURCE_TYPE_POSTGRES},
	})
	if err != nil {
		t.Fatalf("Declare: %v", err)
	}

	resp, err := http.Post(ts.URL+"/sync", "application/octet-stream", nil)
	if err != nil {
		t.Fatalf("POST /sync: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("POST /sync status = %d, want 200", resp.StatusCode)
	}

	result := <-s.Sync()
	if result.Err != nil {
		t.Fatalf("Sync result error: %v", result.Err)
	}
	if result.ProjectConfig.ProjectID != "proj_1" {
		t.Fatalf("ProjectConfig.ProjectID = %q, want %q", result.ProjectConfig.ProjectID, "proj_1")
	}
	if len(result.Resources) != 1 || result.Resources[0].Name != "main" {
		t.Fatalf("Resources = %+v, want one entry named main", result.Resources)
	}
}

func TestSync_MethodNotAllowedForNonPost(t *testing.T) {
	s := New("https://api.example.com", "tok", "proj_1")
	ts := httptest.NewServer(s.Mux())
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/sync")
	if err != nil {
		t.Fatalf("GET /sync: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("GET /sync status = %d, want 405", resp.StatusCode)
	}
}

func TestNew_WithProvisionerOverridesStubImplementations(t *testing.T) {
	fetchCalled := false
	provisionCalled := false

	s := New("https://api.example.com", "tok", "proj_1", WithProvisioner(
		func(_ context.Context, _, _, projectID string) (provision.ProjectConfig, error) {
			fetchCalled = true
			return provision.ProjectConfig{ProjectID: projectID, OrgID: "org_custom", UserID: "user_custom"}, nil
		},
		func(context.Context, provision.ProjectConfig, []manifest.Entry) ([]provision.ProvisionedResource, error) {
			provisionCalled = true
			return []provision.ProvisionedResource{{Name: "custom"}}, nil
		},
	))

	ts := httptest.NewServer(s.Mux())
	defer ts.Close()

	resp, err := http.Post(ts.URL+"/sync", "application/octet-stream", nil)
	if err != nil {
		t.Fatalf("POST /sync: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("POST /sync status = %d, want 200", resp.StatusCode)
	}

	result := <-s.Sync()
	if result.Err != nil {
		t.Fatalf("Sync result error: %v", result.Err)
	}
	if !fetchCalled {
		t.Fatal("WithProvisioner: custom fetchProjectConfig was not called")
	}
	if !provisionCalled {
		t.Fatal("WithProvisioner: custom provision was not called")
	}
	if result.ProjectConfig.OrgID != "org_custom" {
		t.Fatalf("ProjectConfig.OrgID = %q, want %q", result.ProjectConfig.OrgID, "org_custom")
	}
	if len(result.Resources) != 1 || result.Resources[0].Name != "custom" {
		t.Fatalf("Resources = %+v, want one entry named custom", result.Resources)
	}
}

func TestSync_PropagatesProvisionError(t *testing.T) {
	s := New("https://api.example.com", "tok", "proj_1")
	s.provision = func(context.Context, provision.ProjectConfig, []manifest.Entry) ([]provision.ProvisionedResource, error) {
		return nil, errors.New("boom")
	}
	ts := httptest.NewServer(s.Mux())
	defer ts.Close()

	resp, err := http.Post(ts.URL+"/sync", "application/octet-stream", nil)
	if err != nil {
		t.Fatalf("POST /sync: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("POST /sync status = %d, want 500", resp.StatusCode)
	}

	result := <-s.Sync()
	if result.Err == nil {
		t.Fatal("Sync result: expected error, got nil")
	}
}
