package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/internal/credentials"
)

func TestSlugify(t *testing.T) {
	cases := []struct {
		name string
		want string
	}{
		{"My Cool App", "my-cool-app"},
		{"  leading/trailing -- spaces  ", "leading-trailing-spaces"},
		{"Already-slugged-123", "already-slugged-123"},
		{"!!!", ""},
		{strings.Repeat("a", 100), strings.Repeat("a", 63)},
	}
	for _, tc := range cases {
		if got := slugify(tc.name); got != tc.want {
			t.Errorf("slugify(%q) = %q, want %q", tc.name, got, tc.want)
		}
	}
}

func TestRunInit_NotLoggedIn_ReturnsExitErrorWithLoginInstruction(t *testing.T) {
	prev := loadCredentials
	loadCredentials = func() (credentials.Credentials, error) {
		return credentials.Credentials{}, credentials.ErrNotLoggedIn
	}
	defer func() { loadCredentials = prev }()

	var stderr bytes.Buffer
	err := runInit(context.Background(), t.TempDir(), "my-app", initOptions{yes: true}, &bytes.Buffer{}, &stderr, strings.NewReader(""))

	var exitErr *ExitError
	if !errors.As(err, &exitErr) {
		t.Fatalf("runInit err = %v (%T), want *ExitError", err, err)
	}
	if !strings.Contains(stderr.String(), "ocel login") {
		t.Fatalf("stderr = %q, want it to mention `ocel login`", stderr.String())
	}
}

func TestRunInit_ConfigAlreadyExists_ErrorsWithoutAnyAPICalls(t *testing.T) {
	setLoggedIn(t)

	apiCalls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		apiCalls++
		http.NotFound(w, r)
	}))
	defer srv.Close()

	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "ocel.config.ts"), []byte("existing"), 0o644); err != nil {
		t.Fatalf("write existing config: %v", err)
	}

	opts := initOptions{yes: true, apiURL: srv.URL}
	err := runInit(context.Background(), root, "my-app", opts, &bytes.Buffer{}, &bytes.Buffer{}, strings.NewReader(""))
	if err == nil {
		t.Fatal("runInit err = nil, want error")
	}
	if !strings.Contains(err.Error(), "ocel.config.ts") {
		t.Fatalf("err = %v, want it to mention ocel.config.ts", err)
	}
	if apiCalls != 0 {
		t.Fatalf("apiCalls = %d, want 0 (should fail before any network calls)", apiCalls)
	}
}

func TestRunInit_DeclineConfirmation_ReturnsNilWithoutAPICallsOrConfigWrite(t *testing.T) {
	setLoggedIn(t)

	apiCalls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		apiCalls++
		http.NotFound(w, r)
	}))
	defer srv.Close()

	root := t.TempDir()
	opts := initOptions{apiURL: srv.URL}
	var stdout bytes.Buffer
	err := runInit(context.Background(), root, "my-app", opts, &stdout, &bytes.Buffer{}, strings.NewReader("n\n"))
	if err != nil {
		t.Fatalf("runInit err = %v, want nil", err)
	}
	if !strings.Contains(stdout.String(), "Aborted") {
		t.Fatalf("stdout = %q, want it to mention Aborted", stdout.String())
	}
	if apiCalls != 0 {
		t.Fatalf("apiCalls = %d, want 0", apiCalls)
	}
	if _, err := os.Stat(filepath.Join(root, "ocel.config.ts")); !os.IsNotExist(err) {
		t.Fatalf("ocel.config.ts should not have been written")
	}
}

func TestRunInit_NonInteractiveWithoutYes_ErrorsAboutFlag(t *testing.T) {
	setLoggedIn(t)

	root := t.TempDir()
	opts := initOptions{apiURL: "http://unused.invalid"}
	err := runInit(context.Background(), root, "my-app", opts, &bytes.Buffer{}, &bytes.Buffer{}, strings.NewReader(""))
	if err == nil {
		t.Fatal("runInit err = nil, want error")
	}
	if !strings.Contains(err.Error(), "--yes") {
		t.Fatalf("err = %v, want it to mention --yes", err)
	}
}

func TestRunInit_HappyPath_SingleOrg_WritesConfig(t *testing.T) {
	setLoggedIn(t)

	var setActiveCalls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/auth/organization/list" && r.Method == http.MethodGet:
			json.NewEncoder(w).Encode([]map[string]string{
				{"id": "org_1", "name": "Acme Inc", "slug": "acme-inc"},
			})
		case r.URL.Path == "/api/auth/organization/set-active" && r.Method == http.MethodPost:
			setActiveCalls++
			w.WriteHeader(http.StatusOK)
			w.Write([]byte("{}"))
		case r.URL.Path == "/api/projects" && r.Method == http.MethodPost:
			w.WriteHeader(http.StatusCreated)
			json.NewEncoder(w).Encode(map[string]any{
				"id":             "proj_abc123",
				"organizationId": "org_1",
				"name":           "my-app",
				"slug":           "my-app",
				"description":    nil,
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	root := t.TempDir()
	opts := initOptions{yes: true, apiURL: srv.URL}
	var stdout bytes.Buffer
	err := runInit(context.Background(), root, "my-app", opts, &stdout, &bytes.Buffer{}, strings.NewReader(""))
	if err != nil {
		t.Fatalf("runInit err = %v; stdout=%s", err, stdout.String())
	}
	if setActiveCalls != 1 {
		t.Fatalf("setActiveCalls = %d, want 1", setActiveCalls)
	}

	data, err := os.ReadFile(filepath.Join(root, "ocel.config.ts"))
	if err != nil {
		t.Fatalf("read ocel.config.ts: %v", err)
	}
	content := string(data)
	if !strings.Contains(content, `import { defineConfig } from "ocel";`) {
		t.Fatalf("config content = %q, want defineConfig import", content)
	}
	if !strings.Contains(content, "proj_abc123") {
		t.Fatalf("config content = %q, want it to contain project id", content)
	}
}

func TestRunInit_MultiOrgWithOrgFlag_SelectsMatchingOrg(t *testing.T) {
	setLoggedIn(t)

	var setActiveBody map[string]string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/auth/organization/list" && r.Method == http.MethodGet:
			json.NewEncoder(w).Encode([]map[string]string{
				{"id": "org_1", "name": "Acme Inc", "slug": "acme-inc"},
				{"id": "org_2", "name": "Beta LLC", "slug": "beta-llc"},
			})
		case r.URL.Path == "/api/auth/organization/set-active" && r.Method == http.MethodPost:
			json.NewDecoder(r.Body).Decode(&setActiveBody)
			w.WriteHeader(http.StatusOK)
			w.Write([]byte("{}"))
		case r.URL.Path == "/api/projects" && r.Method == http.MethodPost:
			w.WriteHeader(http.StatusCreated)
			json.NewEncoder(w).Encode(map[string]any{
				"id":             "proj_xyz",
				"organizationId": "org_2",
				"name":           "my-app",
				"slug":           "my-app",
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	root := t.TempDir()
	opts := initOptions{yes: true, apiURL: srv.URL, org: "beta-llc"}
	var stdout bytes.Buffer
	err := runInit(context.Background(), root, "my-app", opts, &stdout, &bytes.Buffer{}, strings.NewReader(""))
	if err != nil {
		t.Fatalf("runInit err = %v; stdout=%s", err, stdout.String())
	}
	if setActiveBody["organizationId"] != "org_2" {
		t.Fatalf("SetActiveOrganization called with organizationId = %q, want org_2", setActiveBody["organizationId"])
	}
	if !strings.Contains(stdout.String(), "Beta LLC") {
		t.Fatalf("stdout = %q, want it to mention Beta LLC", stdout.String())
	}
}

func TestRunInit_MultiOrgNoFlagNonInteractive_ErrorsAboutOrgFlag(t *testing.T) {
	setLoggedIn(t)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/auth/organization/list":
			json.NewEncoder(w).Encode([]map[string]string{
				{"id": "org_1", "name": "Acme Inc", "slug": "acme-inc"},
				{"id": "org_2", "name": "Beta LLC", "slug": "beta-llc"},
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	root := t.TempDir()
	opts := initOptions{yes: true, apiURL: srv.URL}
	err := runInit(context.Background(), root, "my-app", opts, &bytes.Buffer{}, &bytes.Buffer{}, strings.NewReader(""))
	if err == nil {
		t.Fatal("runInit err = nil, want error")
	}
	if !strings.Contains(err.Error(), "--org") {
		t.Fatalf("err = %v, want it to mention --org", err)
	}
}

func TestRunInit_CreateProjectConflict_ErrorsAndDoesNotWriteConfig(t *testing.T) {
	setLoggedIn(t)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/auth/organization/list":
			json.NewEncoder(w).Encode([]map[string]string{
				{"id": "org_1", "name": "Acme Inc", "slug": "acme-inc"},
			})
		case r.URL.Path == "/api/auth/organization/set-active":
			w.WriteHeader(http.StatusOK)
			w.Write([]byte("{}"))
		case r.URL.Path == "/api/projects":
			w.WriteHeader(http.StatusConflict)
			json.NewEncoder(w).Encode(map[string]string{
				"error": "A project with this slug already exists in this organization",
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	root := t.TempDir()
	opts := initOptions{yes: true, apiURL: srv.URL}
	err := runInit(context.Background(), root, "my-app", opts, &bytes.Buffer{}, &bytes.Buffer{}, strings.NewReader(""))
	if err == nil {
		t.Fatal("runInit err = nil, want error")
	}
	if !strings.Contains(err.Error(), "Acme Inc") || !strings.Contains(err.Error(), "different name") {
		t.Fatalf("err = %v, want it to name the org and suggest a different name", err)
	}
	if _, statErr := os.Stat(filepath.Join(root, "ocel.config.ts")); !os.IsNotExist(statErr) {
		t.Fatalf("ocel.config.ts should not have been written")
	}
}

// setLoggedIn overrides the loadCredentials seam for the duration of t so
// runInit sees a logged-in user, restoring the previous value on cleanup.
func setLoggedIn(t *testing.T) {
	t.Helper()
	prev := loadCredentials
	loadCredentials = func() (credentials.Credentials, error) {
		return credentials.Credentials{APIURL: "https://api.example.com", AccessToken: "tok"}, nil
	}
	t.Cleanup(func() { loadCredentials = prev })
}
