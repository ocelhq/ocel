package devserver

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/ocelhq/ocel/internal/manifest"
	"github.com/ocelhq/ocel/internal/provision"
	devv1 "github.com/ocelhq/ocel/pkg/proto/dev/v1"
	"github.com/ocelhq/ocel/pkg/proto/dev/v1/devv1connect"
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

func TestDeclareResetSyncDeclare_SyncOnlySeesResourcesDeclaredAfterReset(t *testing.T) {
	s := New("https://api.example.com", "tok", "proj_1")
	ts := httptest.NewServer(s.Mux())
	defer ts.Close()

	client := resourcesv1connect.NewResourceServiceClient(http.DefaultClient, ts.URL)
	_, err := client.Declare(context.Background(), &resourcesv1.DeclareRequest{
		Resource: &resourcesv1.ResourceIdentifier{Name: "stale", Type: resourcesv1.ResourceType_RESOURCE_TYPE_POSTGRES},
	})
	if err != nil {
		t.Fatalf("Declare: %v", err)
	}

	s.ResetManifest()

	_, err = client.Declare(context.Background(), &resourcesv1.DeclareRequest{
		Resource: &resourcesv1.ResourceIdentifier{Name: "fresh", Type: resourcesv1.ResourceType_RESOURCE_TYPE_POSTGRES},
	})
	if err != nil {
		t.Fatalf("Declare: %v", err)
	}

	resp, err := http.Post(ts.URL+"/sync", "application/octet-stream", nil)
	if err != nil {
		t.Fatalf("POST /sync: %v", err)
	}
	defer resp.Body.Close()

	result := <-s.Sync()
	if result.Err != nil {
		t.Fatalf("Sync result error: %v", result.Err)
	}
	if len(result.Resources) != 1 || result.Resources[0].Name != "fresh" {
		t.Fatalf("Resources = %+v, want one entry named fresh", result.Resources)
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

func TestSubscribe_ReceivesEnvPushedAfterConnecting(t *testing.T) {
	s := New("https://api.example.com", "tok", "proj_1")
	// Seed an initial env so the subscribe call below has something to
	// receive immediately (a follower connecting before the leader has
	// resolved anything simply waits — see
	// TestSubscribe_NewSubscriberImmediatelyGetsLatestEnv for that case).
	s.PushEnv(map[string]string{"INITIAL": "1"})

	ts := httptest.NewServer(s.Mux())
	defer ts.Close()

	client := devv1connect.NewDevServiceClient(http.DefaultClient, ts.URL)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	stream, err := client.Subscribe(ctx, &devv1.SubscribeRequest{})
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	defer stream.Close()

	if !stream.Receive() {
		t.Fatalf("stream.Receive() (initial) = false, err = %v", stream.Err())
	}

	s.PushEnv(map[string]string{"OCEL_RESOURCE_POSTGRES_main": "conn"})

	if !stream.Receive() {
		t.Fatalf("stream.Receive() (update) = false, err = %v", stream.Err())
	}
	got := stream.Msg().Env
	if got["OCEL_RESOURCE_POSTGRES_main"] != "conn" {
		t.Fatalf("pushed env = %+v, want OCEL_RESOURCE_POSTGRES_main=conn", got)
	}
}

func TestSubscribe_NewSubscriberImmediatelyGetsLatestEnv(t *testing.T) {
	s := New("https://api.example.com", "tok", "proj_1")
	s.PushEnv(map[string]string{"FOO": "bar"})

	ts := httptest.NewServer(s.Mux())
	defer ts.Close()

	client := devv1connect.NewDevServiceClient(http.DefaultClient, ts.URL)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	stream, err := client.Subscribe(ctx, &devv1.SubscribeRequest{})
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	defer stream.Close()

	if !stream.Receive() {
		t.Fatalf("stream.Receive() = false, err = %v", stream.Err())
	}
	if got := stream.Msg().Env["FOO"]; got != "bar" {
		t.Fatalf("pushed env FOO = %q, want %q", got, "bar")
	}
}
