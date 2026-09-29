package declaration

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"connectrpc.com/connect"

	"github.com/ocelhq/ocel/cli/internal/variables"
	resourcesv1 "github.com/ocelhq/ocel/pkg/proto/app/resources/v1"
	"github.com/ocelhq/ocel/pkg/proto/app/resources/v1/resourcesv1connect"
)

func TestCollectionRefusesADeclarationItsSchemaForbidsAsTheDevServerDoes(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(collectionMux(NewService(variables.NewDeclarations(emptyValues{}, variables.Scope{}))))
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

func TestServiceKeepsOnlyTheDeclarationsThatParse(t *testing.T) {
	t.Parallel()

	t.Run("declare records the full typed config", func(t *testing.T) {
		t.Parallel()

		s := NewService(variables.NewDeclarations(emptyValues{}, variables.Scope{}))

		_, err := s.Declare(context.Background(), &resourcesv1.DeclareRequest{
			Resource: &resourcesv1.ResourceIdentifier{Name: "main", Type: resourcesv1.ResourceType_RESOURCE_TYPE_POSTGRES},
			Config:   &resourcesv1.DeclareRequest_Postgres{Postgres: &resourcesv1.PostgresConfig{Version: "17"}},
		})
		if err != nil {
			t.Fatalf("Declare: %v", err)
		}

		got := s.Resources()
		if len(got) != 1 {
			t.Fatalf("Resources() len = %d, want 1", len(got))
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

		s := NewService(variables.NewDeclarations(emptyValues{}, variables.Scope{}))

		_, err := s.Declare(context.Background(), &resourcesv1.DeclareRequest{})
		if err == nil {
			t.Fatal("Declare: expected error for missing resource, got nil")
		}
		if len(s.Resources()) != 0 {
			t.Fatalf("Resources() len = %d, want 0 after a rejected Declare", len(s.Resources()))
		}
	})

	t.Run("collection acks sync without provisioning", func(t *testing.T) {
		t.Parallel()

		s := NewService(variables.NewDeclarations(emptyValues{}, variables.Scope{}))
		server := httptest.NewServer(collectionMux(s))
		defer server.Close()

		resp, err := http.Post(server.URL+"/sync", "application/json", nil)
		if err != nil {
			t.Fatalf("POST /sync: %v", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			t.Fatalf("POST /sync status = %d, want 200", resp.StatusCode)
		}
		if len(s.Resources()) != 0 {
			t.Fatalf("Resources() len = %d, want 0 — /sync must not provision or record anything", len(s.Resources()))
		}
	})
}

func TestServiceListsEveryDeclarationItKept(t *testing.T) {
	t.Parallel()

	t.Run("a declaration is listed", func(t *testing.T) {
		t.Parallel()

		s := NewService(nil)

		declarePostgres(t, s, "main")

		got := s.Resources()
		if len(got) != 1 || got[0].Name != "main" || got[0].Type != resourcesv1.ResourceType_RESOURCE_TYPE_POSTGRES {
			t.Fatalf("Resources() = %+v, want the postgres main", got)
		}
	})

	t.Run("the listing is an independent copy", func(t *testing.T) {
		t.Parallel()

		s := NewService(nil)
		declarePostgres(t, s, "main")

		snap := s.Resources()
		snap[0].Name = "mutated"

		got := s.Resources()
		if got[0].Name != "main" {
			t.Fatalf("mutating a snapshot affected the service: got %q", got[0].Name)
		}
	})

	t.Run("forgetting clears the declarations", func(t *testing.T) {
		t.Parallel()

		s := NewService(nil)
		declarePostgres(t, s, "main")

		s.ForgetResources()

		if got := s.Resources(); len(got) != 0 {
			t.Fatalf("Resources() after ForgetResources = %+v, want empty", got)
		}

		declarePostgres(t, s, "second")
		got := s.Resources()
		if len(got) != 1 || got[0].Name != "second" || got[0].Type != resourcesv1.ResourceType_RESOURCE_TYPE_POSTGRES {
			t.Fatalf("Resources() after ForgetResources and a declaration = %+v, want only the postgres second", got)
		}
	})

	t.Run("concurrent declarations are all kept", func(t *testing.T) {
		t.Parallel()

		s := NewService(nil)
		var wg sync.WaitGroup
		for range 50 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				declarePostgres(t, s, "r")
			}()
		}
		wg.Wait()

		if got := len(s.Resources()); got != 50 {
			t.Fatalf("Resources() len = %d, want 50", got)
		}
	})
}

func declarePostgres(t *testing.T, s *Service, name string) {
	t.Helper()
	_, err := s.Declare(context.Background(), &resourcesv1.DeclareRequest{
		Resource: &resourcesv1.ResourceIdentifier{Name: name, Type: resourcesv1.ResourceType_RESOURCE_TYPE_POSTGRES},
		Config:   &resourcesv1.DeclareRequest_Postgres{Postgres: &resourcesv1.PostgresConfig{}},
	})
	if err != nil {
		t.Errorf("Declare(%q): %v", name, err)
	}
}
