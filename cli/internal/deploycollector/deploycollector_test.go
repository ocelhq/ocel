package deploycollector

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ocelhq/ocel/cli/internal/envgate"
	resourcesv1 "github.com/ocelhq/ocel/pkg/proto/app/resources/v1"
	linksv1 "github.com/ocelhq/ocel/pkg/proto/common/links/v1"
)

func TestCollector(t *testing.T) {
	t.Parallel()

	t.Run("declare records the full typed config", func(t *testing.T) {
		t.Parallel()

		c := New(envgate.New(emptyValues{}, envgate.Scope{}))

		_, err := c.Declare(context.Background(), &resourcesv1.DeclareRequest{
			Resource: &resourcesv1.ResourceIdentifier{Name: "main", Type: linksv1.LinkType_LINK_TYPE_POSTGRES},
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
		if got[0].Type != linksv1.LinkType_LINK_TYPE_POSTGRES {
			t.Errorf("Type = %v, want %v", got[0].Type, linksv1.LinkType_LINK_TYPE_POSTGRES)
		}
		if got[0].Postgres.GetVersion() != "17" {
			t.Errorf("Postgres.Version = %q, want %q — config oneof must not be discarded", got[0].Postgres.GetVersion(), "17")
		}
	})

	t.Run("declare rejects an invalid declare", func(t *testing.T) {
		t.Parallel()

		c := New(envgate.New(emptyValues{}, envgate.Scope{}))

		_, err := c.Declare(context.Background(), &resourcesv1.DeclareRequest{})
		if err == nil {
			t.Fatal("Declare: expected error for missing resource, got nil")
		}
		if len(c.Snapshot()) != 0 {
			t.Fatalf("Snapshot() len = %d, want 0 after a rejected Declare", len(c.Snapshot()))
		}
	})

	t.Run("the mux acks sync without provisioning", func(t *testing.T) {
		t.Parallel()

		c := New(envgate.New(emptyValues{}, envgate.Scope{}))
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
	})
}
