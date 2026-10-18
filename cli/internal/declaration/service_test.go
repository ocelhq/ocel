package declaration

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
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

		s.ForgetDeclarations()

		if got := s.Resources(); len(got) != 0 {
			t.Fatalf("Resources() after ForgetDeclarations = %+v, want empty", got)
		}

		declarePostgres(t, s, "second")
		got := s.Resources()
		if len(got) != 1 || got[0].Name != "second" || got[0].Type != resourcesv1.ResourceType_RESOURCE_TYPE_POSTGRES {
			t.Fatalf("Resources() after ForgetDeclarations and a declaration = %+v, want only the postgres second", got)
		}
	})

	t.Run("concurrent declarations are all kept", func(t *testing.T) {
		t.Parallel()

		s := NewService(nil)
		var wg sync.WaitGroup
		for i := range 50 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				declarePostgres(t, s, fmt.Sprintf("r%d", i))
			}()
		}
		wg.Wait()

		if got := len(s.Resources()); got != 50 {
			t.Fatalf("Resources() len = %d, want 50", got)
		}
	})
}

func TestAKVDeclarationResentFromItsLineReplacesTheOneBefore(t *testing.T) {
	t.Parallel()

	s := NewService(nil)
	store := func(entries ...string) *resourcesv1.DeclareRequest {
		config := &resourcesv1.KvConfig{}
		for _, entry := range entries {
			config.Entries = append(config.Entries, &resourcesv1.KvEntry{Name: entry, Pattern: entry + "/:id", Shape: resourcesv1.KvShape_KV_SHAPE_TEXT})
		}
		return &resourcesv1.DeclareRequest{
			Resource: &resourcesv1.ResourceIdentifier{Name: "cache", Type: resourcesv1.ResourceType_RESOURCE_TYPE_KV},
			Config:   &resourcesv1.DeclareRequest_Kv{Kv: config},
			Source:   "/app/cache.go:10",
		}
	}
	for _, req := range []*resourcesv1.DeclareRequest{store(), store("session"), store("session", "requests")} {
		if _, err := s.Declare(context.Background(), req); err != nil {
			t.Fatalf("Declare: %v", err)
		}
	}
	other := store()
	other.Source = "/app/other.go:3"
	if _, err := s.Declare(context.Background(), other); err != nil {
		t.Fatalf("Declare: %v", err)
	}

	got := s.Resources()
	if len(got) != 2 || len(got[0].KV.GetEntries()) != 2 || got[1].Source != "/app/other.go:3" {
		t.Errorf("Resources() = %+v, want the store from cache.go:10 once, with both entries, and the one from other.go:3 kept beside it", got)
	}
}

func TestARealtimeDeclarationResentFromItsLineReplacesTheOneBefore(t *testing.T) {
	t.Parallel()

	s := NewService(nil)
	realtime := func(patterns ...string) *resourcesv1.DeclareRequest {
		config := &resourcesv1.RealtimeConfig{}
		for _, pattern := range patterns {
			config.Channels = append(config.Channels, &resourcesv1.RealtimeChannel{Pattern: pattern, Source: "/app/infra.go:20"})
		}
		return &resourcesv1.DeclareRequest{
			Resource: &resourcesv1.ResourceIdentifier{Name: "app", Type: resourcesv1.ResourceType_RESOURCE_TYPE_REALTIME},
			Config:   &resourcesv1.DeclareRequest_Realtime{Realtime: config},
			Source:   "/app/infra.go:12",
		}
	}
	for _, req := range []*resourcesv1.DeclareRequest{realtime(), realtime("orders/:orderId"), realtime("orders/:orderId", "status")} {
		if _, err := s.Declare(context.Background(), req); err != nil {
			t.Fatalf("Declare: %v", err)
		}
	}

	got := s.Resources()
	if len(got) != 1 || len(got[0].Realtime.GetChannels()) != 2 {
		t.Errorf("Resources() = %+v, want the realtime resource from infra.go:12 once, with both channels", got)
	}
}

func TestTheSameDeclarationSentTwiceIsKeptOnce(t *testing.T) {
	t.Parallel()

	s := NewService(nil)
	for range 2 {
		_, err := s.Declare(context.Background(), &resourcesv1.DeclareRequest{
			Resource: &resourcesv1.ResourceIdentifier{Name: "main", Type: resourcesv1.ResourceType_RESOURCE_TYPE_POSTGRES},
			Config:   &resourcesv1.DeclareRequest_Postgres{Postgres: &resourcesv1.PostgresConfig{}},
			Source:   "/app/db.go:4",
		})
		if err != nil {
			t.Fatalf("Declare: %v", err)
		}
	}

	if got := s.Resources(); len(got) != 1 {
		t.Errorf("Resources() = %+v, want one postgres: a module imported under two names sends the same request twice", got)
	}
}

func TestADifferentDeclarationFromTheSameLineIsKeptBesideTheFirstForTheManifestToRefuse(t *testing.T) {
	t.Parallel()

	s := NewService(nil)
	for _, version := range []string{"16", "17"} {
		_, err := s.Declare(context.Background(), &resourcesv1.DeclareRequest{
			Resource: &resourcesv1.ResourceIdentifier{Name: "main", Type: resourcesv1.ResourceType_RESOURCE_TYPE_POSTGRES},
			Config:   &resourcesv1.DeclareRequest_Postgres{Postgres: &resourcesv1.PostgresConfig{Version: version}},
			Source:   "/app/db.go:4",
		})
		if err != nil {
			t.Fatalf("Declare: %v", err)
		}
	}

	if got := s.Resources(); len(got) != 2 {
		t.Errorf("Resources() = %+v, want both postgres declarations kept", got)
	}
}

func TestAKVSeriesResentByASecondImportKeepsTheStoreItGrewTo(t *testing.T) {
	t.Parallel()

	s := NewService(nil)
	store := func(entries ...string) *resourcesv1.DeclareRequest {
		config := &resourcesv1.KvConfig{}
		for _, entry := range entries {
			config.Entries = append(config.Entries, &resourcesv1.KvEntry{Name: entry, Pattern: entry + "/:id", Shape: resourcesv1.KvShape_KV_SHAPE_TEXT})
		}
		return &resourcesv1.DeclareRequest{
			Resource: &resourcesv1.ResourceIdentifier{Name: "cache", Type: resourcesv1.ResourceType_RESOURCE_TYPE_KV},
			Config:   &resourcesv1.DeclareRequest_Kv{Kv: config},
			Source:   "/app/cache.py:10",
		}
	}
	for range 2 {
		for _, req := range []*resourcesv1.DeclareRequest{store(), store("visits"), store("visits", "names")} {
			if _, err := s.Declare(context.Background(), req); err != nil {
				t.Fatalf("Declare: %v", err)
			}
		}
	}

	if got := s.Resources(); len(got) != 1 || len(got[0].KV.GetEntries()) != 2 {
		t.Errorf("Resources() = %+v, want the store once with both entries", got)
	}
}

func TestTheSameEnvDeclarationSentTwiceIsAnsweredTheSameAndClaimedOnce(t *testing.T) {
	t.Parallel()

	declarations := variables.NewDeclarations(emptyValues{}, variables.Scope{})
	s := NewService(declarations)
	req := &resourcesv1.DeclareEnvRequest{
		Groups: []*resourcesv1.GroupDefinition{{Key: "github", Required: true}},
		Definitions: []*resourcesv1.VariableDefinition{{
			Key: "GITHUB_ID", Class: resourcesv1.VariableClass_VARIABLE_CLASS_PLAIN, Group: "github", Source: "/app/env.py:3",
		}},
	}
	for range 2 {
		if _, err := s.DeclareEnv(context.Background(), req); err != nil {
			t.Fatalf("DeclareEnv: %v", err)
		}
	}

	if got := declarations.Definitions(); len(got) != 1 {
		t.Errorf("Definitions() = %v, want GITHUB_ID once", got)
	}
}

func TestAKeyDeclaredInTwoFilesIsRefusedNamingBoth(t *testing.T) {
	t.Parallel()

	s := NewService(variables.NewDeclarations(emptyValues{}, variables.Scope{}))
	declare := func(source string) error {
		_, err := s.DeclareEnv(context.Background(), &resourcesv1.DeclareEnvRequest{Definitions: []*resourcesv1.VariableDefinition{{
			Key: "API_URL", Class: resourcesv1.VariableClass_VARIABLE_CLASS_PLAIN, Source: source,
		}}})
		return err
	}
	if err := declare("/app/a.go:3"); err != nil {
		t.Fatalf("DeclareEnv: %v", err)
	}
	if err := declare("/app/a.go:9"); err != nil {
		t.Fatalf("DeclareEnv from the same file again: %v, want it kept: a key may be declared twice in its one file", err)
	}

	err := declare("/app/b.go:5")
	if err == nil || !strings.Contains(err.Error(), "/app/a.go") || !strings.Contains(err.Error(), "/app/b.go") {
		t.Errorf("DeclareEnv from a second file = %v, want it refused naming both files", err)
	}
}

func TestAReportedProblemSentTwiceIsReportedOnce(t *testing.T) {
	t.Parallel()

	declarations := variables.NewDeclarations(emptyValues{}, variables.Scope{})
	s := NewService(declarations)
	_, _ = s.DeclareEnv(context.Background(), &resourcesv1.DeclareEnvRequest{Definitions: []*resourcesv1.VariableDefinition{{
		Key: "PORT", Class: resourcesv1.VariableClass_VARIABLE_CLASS_PLAIN, Required: true, Source: "/app/env.go:3",
	}}})
	problem := &resourcesv1.VariableProblem{Key: "PORT", Kind: resourcesv1.VariableProblem_KIND_INVALID, Detail: "is not a number"}
	for range 2 {
		if _, err := s.ReportEnvProblems(context.Background(), &resourcesv1.ReportEnvProblemsRequest{Problems: []*resourcesv1.VariableProblem{problem}}); err != nil {
			t.Fatalf("ReportEnvProblems: %v", err)
		}
	}

	var missing *variables.MissingError
	if err := declarations.RefuseIncomplete(); !errors.As(err, &missing) || len(missing.Problems) != 1 {
		t.Errorf("RefuseIncomplete() = %v, want the one invalid PORT", err)
	}
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
