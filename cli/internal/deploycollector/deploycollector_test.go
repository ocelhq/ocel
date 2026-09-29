package deploycollector

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"connectrpc.com/connect"

	"github.com/ocelhq/ocel/cli/internal/variables"
	resourcesv1 "github.com/ocelhq/ocel/pkg/proto/app/resources/v1"
	"github.com/ocelhq/ocel/pkg/proto/app/resources/v1/resourcesv1connect"
)

func TestTheDeployCollectorRefusesADeclarationItsSchemaForbidsAsTheDevServerDoes(t *testing.T) {
	t.Parallel()

	c := New(variables.NewDeclarations(emptyValues{}, variables.Scope{}))
	server := httptest.NewServer(c.Mux())
	t.Cleanup(server.Close)

	_, err := resourcesv1connect.NewResourceServiceClient(server.Client(), server.URL).DeclareEnv(context.Background(), &resourcesv1.DeclareEnvRequest{
		Definitions: []*resourcesv1.VariableDefinition{{
			Key:     "POSTHOG_ID",
			Class:   resourcesv1.VariableClass_VARIABLE_CLASS_PLAIN,
			Folders: []string{"web"},
		}},
	})

	var connectErr *connect.Error
	if !errors.As(err, &connectErr) || connectErr.Code() != connect.CodeInvalidArgument {
		t.Fatalf("DeclareEnv with an unanchored folder err = %v, want %v: deploy admits only what dev admits", err, connect.CodeInvalidArgument)
	}
}

func TestCollector(t *testing.T) {
	t.Parallel()

	t.Run("declare records the full typed config", func(t *testing.T) {
		t.Parallel()

		c := New(variables.NewDeclarations(emptyValues{}, variables.Scope{}))

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
			t.Errorf("Type = %v, want %v", got[0].Type, resourcesv1.ResourceType_RESOURCE_TYPE_POSTGRES)
		}
		if got[0].Postgres.GetVersion() != "17" {
			t.Errorf("Postgres.Version = %q, want %q — config oneof must not be discarded", got[0].Postgres.GetVersion(), "17")
		}
	})

	t.Run("declare rejects an invalid declare", func(t *testing.T) {
		t.Parallel()

		c := New(variables.NewDeclarations(emptyValues{}, variables.Scope{}))

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

		c := New(variables.NewDeclarations(emptyValues{}, variables.Scope{}))
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
