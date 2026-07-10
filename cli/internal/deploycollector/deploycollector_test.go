package deploycollector

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	resourcesv1 "github.com/ocelhq/ocel/pkg/proto/resources/v1"
)

func TestCollector_Declare_RecordsFullTypedConfig(t *testing.T) {
	c := New()

	_, err := c.Declare(context.Background(), &resourcesv1.DeclareRequest{
		Resource: &resourcesv1.ResourceIdentifier{Name: "main", Type: resourcesv1.ResourceType_RESOURCE_TYPE_POSTGRES},
		Config:   &resourcesv1.DeclareRequest_Postgres{Postgres: &resourcesv1.PostgresConfig{Version: "17"}},
	})
	if err != nil {
		t.Fatalf("Declare: %v", err)
	}

	got := c.Snapshot()
	if len(got) != 1 {
		t.Fatalf("Snapshot() len = %d, want 1", len(got))
	}
	if got[0].Name != "main" {
		t.Errorf("Name = %q, want %q", got[0].Name, "main")
	}
	if got[0].Type != resourcesv1.ResourceType_RESOURCE_TYPE_POSTGRES {
		t.Errorf("Type = %v, want RESOURCE_TYPE_POSTGRES", got[0].Type)
	}
	if got[0].Postgres.GetVersion() != "17" {
		t.Errorf("Postgres.Version = %q, want %q — config oneof must not be discarded", got[0].Postgres.GetVersion(), "17")
	}
}

func TestCollector_Declare_RejectsInvalidDeclare(t *testing.T) {
	c := New()

	_, err := c.Declare(context.Background(), &resourcesv1.DeclareRequest{})
	if err == nil {
		t.Fatal("Declare: expected error for missing resource, got nil")
	}
	if len(c.Snapshot()) != 0 {
		t.Fatalf("Snapshot() len = %d, want 0 after a rejected Declare", len(c.Snapshot()))
	}
}

func TestCollector_Mux_AcksSyncWithoutProvisioning(t *testing.T) {
	c := New()
	server := httptest.NewServer(c.Mux())
	defer server.Close()

	resp, err := http.Post(server.URL+"/sync", "application/json", nil)
	if err != nil {
		t.Fatalf("POST /sync: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("POST /sync status = %d, want 200", resp.StatusCode)
	}
	if len(c.Snapshot()) != 0 {
		t.Fatalf("Snapshot() len = %d, want 0 — /sync must not provision or record anything", len(c.Snapshot()))
	}
}
