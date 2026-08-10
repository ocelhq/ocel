package devserver

import (
	"context"
	"net/http"
	"net/http/httptest"
	"slices"
	"sort"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/cli/internal/envgate"
	resourcesv1 "github.com/ocelhq/ocel/pkg/proto/resources/v1"
	"github.com/ocelhq/ocel/pkg/proto/resources/v1/resourcesv1connect"
)

func declareEnv(t *testing.T, url string, definitions ...*resourcesv1.VariableDefinition) *resourcesv1.DeclareEnvResponse {
	t.Helper()
	client := resourcesv1connect.NewResourceServiceClient(http.DefaultClient, url)
	resp, err := client.DeclareEnv(context.Background(), &resourcesv1.DeclareEnvRequest{Definitions: definitions})
	if err != nil {
		t.Fatalf("DeclareEnv: %v", err)
	}
	return resp
}

func reportProblems(t *testing.T, url string, problems ...*resourcesv1.VariableProblem) {
	t.Helper()
	client := resourcesv1connect.NewResourceServiceClient(http.DefaultClient, url)
	if _, err := client.ReportEnvProblems(context.Background(), &resourcesv1.ReportEnvProblemsRequest{Problems: problems}); err != nil {
		t.Fatalf("ReportEnvProblems: %v", err)
	}
}

func TestCheckEnv_NoValuesInstalled_GatesNothing(t *testing.T) {
	s := New("https://api.example.com", "tok", "proj_1", "http://127.0.0.1:0")
	ts := httptest.NewServer(s.Mux())
	defer ts.Close()

	msg := declareEnv(t, ts.URL, &resourcesv1.VariableDefinition{
		Key: "DATABASE_URL", Class: resourcesv1.VariableClass_VARIABLE_CLASS_PLAIN, Required: true,
	})
	if len(msg.GetCells()) != 0 {
		t.Errorf("cells = %v, want none", msg.GetCells())
	}
	if err := s.CheckEnv(context.Background()); err != nil {
		t.Errorf("CheckEnv = %v, want nil", err)
	}
}

func TestDeclareEnv_AnswersARootKeyWithItsPlaintext(t *testing.T) {
	s := New("https://api.example.com", "tok", "proj_1", "http://127.0.0.1:0")
	s.UseValues(map[string]string{"DATABASE_URL": "postgres://localhost/app"}, envgate.Scope{})
	ts := httptest.NewServer(s.Mux())
	defer ts.Close()

	msg := declareEnv(t, ts.URL, &resourcesv1.VariableDefinition{
		Key: "DATABASE_URL", Class: resourcesv1.VariableClass_VARIABLE_CLASS_PLAIN, Required: true,
	})

	cells := msg.GetCells()
	if len(cells) != 1 {
		t.Fatalf("cells = %v, want exactly one", cells)
	}
	if cells[0].GetFolder() != "" {
		t.Errorf("folder = %q, want the project root spelled as the empty string", cells[0].GetFolder())
	}
	if cells[0].GetValue() != "postgres://localhost/app" {
		t.Errorf("value = %q, want the dotfile's plaintext", cells[0].GetValue())
	}
	if err := s.CheckEnv(context.Background()); err != nil {
		t.Errorf("CheckEnv = %v, want nil — the value is there", err)
	}
}

func TestDeclareEnv_NeverRevealsALiveValue(t *testing.T) {
	s := New("https://api.example.com", "tok", "proj_1", "http://127.0.0.1:0")
	s.UseValues(map[string]string{"WEBHOOK_SECRET": "whsec_must_not_appear"}, envgate.Scope{})
	ts := httptest.NewServer(s.Mux())
	defer ts.Close()

	msg := declareEnv(t, ts.URL, &resourcesv1.VariableDefinition{
		Key: "WEBHOOK_SECRET", Class: resourcesv1.VariableClass_VARIABLE_CLASS_SECRET, Required: true,
	})

	cells := msg.GetCells()
	if len(cells) != 1 {
		t.Fatalf("cells = %v, want presence for the declared key", cells)
	}
	if cells[0].GetValue() != "" {
		t.Errorf("value = %q, want a live cell answered as presence with no plaintext", cells[0].GetValue())
	}
}

func TestDeclareEnv_ARootLineBroadcastsToEveryFolderAScopedKeyNames(t *testing.T) {
	s := New("https://api.example.com", "tok", "proj_1", "http://127.0.0.1:0")
	s.UseValues(map[string]string{"API_BASE": "http://localhost:3000"}, envgate.Scope{
		Apps: []envgate.App{{Name: "web", Folder: "/web"}, {Name: "admin", Folder: "/admin"}},
	})
	ts := httptest.NewServer(s.Mux())
	defer ts.Close()

	msg := declareEnv(t, ts.URL, &resourcesv1.VariableDefinition{
		Key: "API_BASE", Class: resourcesv1.VariableClass_VARIABLE_CLASS_PLAIN, Required: true,
		Folders: []string{"/web", "/admin"},
	})

	var folders []string
	for _, cell := range msg.GetCells() {
		folders = append(folders, cell.GetFolder())
		if cell.GetValue() != "http://localhost:3000" {
			t.Errorf("cell %q value = %q, want the one root value broadcast unchanged", cell.GetFolder(), cell.GetValue())
		}
	}
	sort.Strings(folders)
	if len(folders) != 2 || folders[0] != "/admin" || folders[1] != "/web" {
		t.Fatalf("folders = %v, want one cell per folder in the scope", folders)
	}
	if err := s.CheckEnv(context.Background()); err != nil {
		t.Errorf("CheckEnv = %v, want nil — the broadcast covers every required cell", err)
	}
}

func TestCheckEnv_RefusesARequiredKeyTheValuesDoNotHold(t *testing.T) {
	s := New("https://api.example.com", "tok", "proj_1", "http://127.0.0.1:0")
	s.UseValues(map[string]string{}, envgate.Scope{Apps: []envgate.App{{Name: "web"}}})
	ts := httptest.NewServer(s.Mux())
	defer ts.Close()

	declareEnv(t, ts.URL, &resourcesv1.VariableDefinition{
		Key: "DATABASE_URL", Class: resourcesv1.VariableClass_VARIABLE_CLASS_PLAIN, Required: true,
	})

	err := s.CheckEnv(context.Background())
	refusal, ok := err.(*envgate.Refusal)
	if !ok {
		t.Fatalf("CheckEnv = %v (%T), want *envgate.Refusal", err, err)
	}
	if len(refusal.Problems) != 1 || refusal.Problems[0].GetKey() != "DATABASE_URL" {
		t.Fatalf("problems = %v, want one naming DATABASE_URL", refusal.Problems)
	}
	if refusal.Problems[0].GetKind() != resourcesv1.VariableProblem_KIND_MISSING {
		t.Errorf("kind = %v, want KIND_MISSING", refusal.Problems[0].GetKind())
	}
}

func TestCheckEnv_KeepsTheProblemsTheDeclaringProcessReports(t *testing.T) {
	s := New("https://api.example.com", "tok", "proj_1", "http://127.0.0.1:0")
	s.UseValues(map[string]string{"PORT": "not-a-number"}, envgate.Scope{})
	ts := httptest.NewServer(s.Mux())
	defer ts.Close()

	declareEnv(t, ts.URL, &resourcesv1.VariableDefinition{
		Key: "PORT", Class: resourcesv1.VariableClass_VARIABLE_CLASS_PLAIN, Required: true,
	})
	reportProblems(t, ts.URL, &resourcesv1.VariableProblem{
		Key: "PORT", Kind: resourcesv1.VariableProblem_KIND_INVALID, Detail: "expected a number",
	})

	err := s.CheckEnv(context.Background())
	if err == nil || !strings.Contains(err.Error(), "PORT") {
		t.Fatalf("CheckEnv = %v, want a refusal naming PORT", err)
	}
}

func TestResetManifest_ForgetsThePriorRunsVerdict(t *testing.T) {
	s := New("https://api.example.com", "tok", "proj_1", "http://127.0.0.1:0")
	s.UseValues(map[string]string{}, envgate.Scope{})
	ts := httptest.NewServer(s.Mux())
	defer ts.Close()

	declareEnv(t, ts.URL, &resourcesv1.VariableDefinition{
		Key: "GONE", Class: resourcesv1.VariableClass_VARIABLE_CLASS_PLAIN, Required: true,
	})
	if err := s.CheckEnv(context.Background()); err == nil {
		t.Fatal("CheckEnv = nil, want a refusal before the reset")
	}

	s.ResetManifest()

	if err := s.CheckEnv(context.Background()); err != nil {
		t.Fatalf("CheckEnv = %v, want nil — the declaration that failed is gone", err)
	}
}

func TestResetManifest_ForgetsThePriorRunsScopes(t *testing.T) {
	s := New("https://api.example.com", "tok", "proj_1", "http://127.0.0.1:0")
	s.UseValues(map[string]string{"API_BASE": "http://localhost:3000"}, envgate.Scope{
		Apps: []envgate.App{{Name: "web"}},
	})
	ts := httptest.NewServer(s.Mux())
	defer ts.Close()

	declareEnv(t, ts.URL, &resourcesv1.VariableDefinition{
		Key: "API_BASE", Class: resourcesv1.VariableClass_VARIABLE_CLASS_PLAIN, Required: true,
		Folders: []string{"/web"},
	})

	s.ResetManifest()

	msg := declareEnv(t, ts.URL, &resourcesv1.VariableDefinition{
		Key: "API_BASE", Class: resourcesv1.VariableClass_VARIABLE_CLASS_PLAIN, Required: true,
	})
	if len(msg.GetCells()) != 1 || msg.GetCells()[0].GetFolder() != "" {
		t.Fatalf("cells = %v, want one root cell: the folder scope belonged to the discovery that is gone", msg.GetCells())
	}
	if err := s.CheckEnv(context.Background()); err != nil {
		t.Fatalf("CheckEnv = %v, want nil", err)
	}
}

func TestCheckEnv_RulesFromTheStoreAsOfTheEndOfDiscovery(t *testing.T) {
	ctx := context.Background()
	s := New("https://api.example.com", "tok", "proj_1", "http://127.0.0.1:0")
	s.UseValues(map[string]string{"API_BASE": "http://localhost:3000"}, envgate.Scope{
		Apps: []envgate.App{{Name: "web", Folder: "/web"}},
	})

	store, gate := s.variables()
	if err := gate.Prefetch(ctx); err != nil {
		t.Fatalf("Prefetch: %v", err)
	}
	definitions := []*resourcesv1.VariableDefinition{{
		Key: "API_BASE", Class: resourcesv1.VariableClass_VARIABLE_CLASS_PLAIN, Required: true,
		Folders: []string{"/web"},
	}}
	store.Declare(definitions)
	if _, err := gate.DeclareEnv(ctx, &resourcesv1.DeclareEnvRequest{Definitions: definitions}); err != nil {
		t.Fatalf("DeclareEnv: %v", err)
	}

	if err := s.CheckEnv(ctx); err != nil {
		t.Fatalf("CheckEnv = %v, want nil: the file holds a value for every folder the declaration names", err)
	}
}

func TestDeclareEnv_KeepsEveryDeclarationsFoldersForOneKey(t *testing.T) {
	s := New("https://api.example.com", "tok", "proj_1", "http://127.0.0.1:0")
	s.UseValues(map[string]string{"API_BASE": "http://localhost:3000"}, envgate.Scope{
		Apps: []envgate.App{{Name: "web", Folder: "/web"}, {Name: "admin", Folder: "/admin"}},
	})
	ts := httptest.NewServer(s.Mux())
	defer ts.Close()

	declareEnv(t, ts.URL, &resourcesv1.VariableDefinition{
		Key: "API_BASE", Class: resourcesv1.VariableClass_VARIABLE_CLASS_PLAIN, Required: true,
		Folders: []string{"/web"},
	})
	msg := declareEnv(t, ts.URL, &resourcesv1.VariableDefinition{
		Key: "API_BASE", Class: resourcesv1.VariableClass_VARIABLE_CLASS_PLAIN, Required: true,
		Folders: []string{"/admin"},
	})

	var folders []string
	for _, cell := range msg.GetCells() {
		folders = append(folders, cell.GetFolder())
	}
	sort.Strings(folders)
	if len(folders) != 2 || folders[0] != "/admin" || folders[1] != "/web" {
		t.Fatalf("folders = %v, want both declarations' folders held", folders)
	}
	if err := s.CheckEnv(context.Background()); err != nil {
		t.Fatalf("CheckEnv = %v, want nil", err)
	}
}

func TestScopedFolders_CarriesEveryFolderEveryDeclarationNames(t *testing.T) {
	s := New("https://api.example.com", "tok", "proj_1", "http://127.0.0.1:0")
	s.UseValues(map[string]string{}, envgate.Scope{})
	ts := httptest.NewServer(s.Mux())
	defer ts.Close()

	declareEnv(t, ts.URL,
		&resourcesv1.VariableDefinition{Key: "API_BASE", Folders: []string{"/web"}},
		&resourcesv1.VariableDefinition{Key: "DATABASE_URL"},
	)
	declareEnv(t, ts.URL, &resourcesv1.VariableDefinition{Key: "API_BASE", Folders: []string{"/admin", "/web"}})
	declareEnv(t, ts.URL, &resourcesv1.VariableDefinition{Key: "API_BASE", Folders: []string{"/admin"}})

	got := s.ScopedFolders()
	if len(got) != 1 {
		t.Fatalf("ScopedFolders = %v, want exactly one key: DATABASE_URL is unscoped", got)
	}
	if want := []string{"/admin", "/web"}; !slices.Equal(got["API_BASE"], want) {
		t.Fatalf("ScopedFolders[API_BASE] = %v, want %v: both declarations' folders, once each", got["API_BASE"], want)
	}
}

func TestCheckEnv_StatesPresenceForALiveKeyItHoldsNoValueFor(t *testing.T) {
	ctx := context.Background()
	s := New("https://api.example.com", "tok", "proj_1", "http://127.0.0.1:0")
	s.UseValues(map[string]string{}, envgate.Scope{Apps: []envgate.App{{Name: "web"}}})
	ts := httptest.NewServer(s.Mux())
	defer ts.Close()

	msg := declareEnv(t, ts.URL, &resourcesv1.VariableDefinition{
		Key: "DB_PASSWORD", Class: resourcesv1.VariableClass_VARIABLE_CLASS_SECRET, Required: true,
	})
	if len(msg.GetCells()) != 1 || msg.GetCells()[0].GetValue() != "" {
		t.Fatalf("cells = %v, want one presence cell with no plaintext", msg.GetCells())
	}

	if err := s.CheckEnv(ctx); err != nil {
		t.Fatalf("CheckEnv = %v, want nil — a live key is resolved at sync, not from the file", err)
	}

	store, _ := s.variables()
	found, err := store.Reveal(ctx, []envgate.Address{{Cell: envgate.Cell{Key: "DB_PASSWORD"}}})
	if err != nil {
		t.Fatalf("Reveal: %v", err)
	}
	if len(found) != 0 {
		t.Errorf("Reveal = %v, want nothing: presence is not a value", found)
	}
}

func TestCheckEnv_StillRefusesAPlainKeyBesideAnExemptLiveOne(t *testing.T) {
	s := New("https://api.example.com", "tok", "proj_1", "http://127.0.0.1:0")
	s.UseValues(map[string]string{}, envgate.Scope{Apps: []envgate.App{{Name: "web"}}})
	ts := httptest.NewServer(s.Mux())
	defer ts.Close()

	declareEnv(t, ts.URL,
		&resourcesv1.VariableDefinition{Key: "DB_PASSWORD", Class: resourcesv1.VariableClass_VARIABLE_CLASS_SECRET, Required: true},
		&resourcesv1.VariableDefinition{Key: "DATABASE_URL", Class: resourcesv1.VariableClass_VARIABLE_CLASS_PLAIN, Required: true},
	)

	err := s.CheckEnv(context.Background())
	refusal, ok := err.(*envgate.Refusal)
	if !ok {
		t.Fatalf("CheckEnv = %v (%T), want *envgate.Refusal", err, err)
	}
	if len(refusal.Problems) != 1 || refusal.Problems[0].GetKey() != "DATABASE_URL" {
		t.Fatalf("problems = %v, want exactly the plain key named", refusal.Problems)
	}
}

func TestCheckEnv_StatesPresenceForEveryFolderALiveKeyIsScopedTo(t *testing.T) {
	s := New("https://api.example.com", "tok", "proj_1", "http://127.0.0.1:0")
	s.UseValues(map[string]string{}, envgate.Scope{
		Apps: []envgate.App{{Name: "web", Folder: "/web"}, {Name: "admin", Folder: "/admin"}},
	})
	ts := httptest.NewServer(s.Mux())
	defer ts.Close()

	msg := declareEnv(t, ts.URL, &resourcesv1.VariableDefinition{
		Key: "DB_PASSWORD", Class: resourcesv1.VariableClass_VARIABLE_CLASS_SECRET, Required: true,
		Folders: []string{"/web", "/admin"},
	})

	var folders []string
	for _, cell := range msg.GetCells() {
		folders = append(folders, cell.GetFolder())
	}
	sort.Strings(folders)
	if len(folders) != 2 || folders[0] != "/admin" || folders[1] != "/web" {
		t.Fatalf("folders = %v, want one presence cell per folder in the scope", folders)
	}
	if err := s.CheckEnv(context.Background()); err != nil {
		t.Fatalf("CheckEnv = %v, want nil", err)
	}
}
