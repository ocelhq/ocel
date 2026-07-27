package projectclient

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestCreateProject_Success(t *testing.T) {
	var gotBody map[string]string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method = %s, want POST", r.Method)
		}
		if r.URL.Path != "/api/projects" {
			t.Errorf("path = %s, want /api/projects", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer tok" {
			t.Errorf("Authorization header = %q, want %q", got, "Bearer tok")
		}
		json.NewDecoder(r.Body).Decode(&gotBody)
		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(Project{
			ID:             "proj_1",
			OrganizationID: "org_1",
			Name:           "My App",
			Slug:           "my-app",
		})
	}))
	defer srv.Close()

	client := New(srv.URL)
	project, err := client.CreateProject(context.Background(), "tok", "My App", "my-app")
	if err != nil {
		t.Fatalf("CreateProject err = %v", err)
	}
	if project.ID != "proj_1" {
		t.Fatalf("project.ID = %q, want proj_1", project.ID)
	}
	if gotBody["name"] != "My App" || gotBody["slug"] != "my-app" {
		t.Fatalf("request body = %+v, want name=My App slug=my-app", gotBody)
	}
}

func TestCreateProject_Conflict(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusConflict)
		json.NewEncoder(w).Encode(map[string]string{
			"error": "A project with this slug already exists in this organization",
		})
	}))
	defer srv.Close()

	client := New(srv.URL)
	_, err := client.CreateProject(context.Background(), "tok", "My App", "my-app")
	if err == nil {
		t.Fatal("CreateProject err = nil, want error")
	}
	if !IsConflict(err) {
		t.Fatalf("IsConflict(%v) = false, want true", err)
	}
	if IsUnauthorized(err) {
		t.Fatalf("IsUnauthorized(%v) = true, want false", err)
	}
}

func TestCreateProject_Unauthorized(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		json.NewEncoder(w).Encode(map[string]string{"error": "Unauthorized"})
	}))
	defer srv.Close()

	client := New(srv.URL)
	_, err := client.CreateProject(context.Background(), "tok", "My App", "my-app")
	if err == nil {
		t.Fatal("CreateProject err = nil, want error")
	}
	if !IsUnauthorized(err) {
		t.Fatalf("IsUnauthorized(%v) = false, want true", err)
	}
}

func TestCreateProject_GenericError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte("boom"))
	}))
	defer srv.Close()

	client := New(srv.URL)
	_, err := client.CreateProject(context.Background(), "tok", "My App", "my-app")
	if err == nil {
		t.Fatal("CreateProject err = nil, want error")
	}
	if IsConflict(err) || IsUnauthorized(err) {
		t.Fatalf("err = %v, want neither conflict nor unauthorized", err)
	}
}

func TestListProjects_Success(t *testing.T) {
	var gotAuth, gotMethod, gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth, gotMethod, gotPath = r.Header.Get("Authorization"), r.Method, r.URL.Path
		json.NewEncoder(w).Encode([]map[string]string{
			{"id": "p1", "organizationId": "org_1", "name": "My App", "slug": "my-app"},
			{"id": "p2", "organizationId": "org_1", "name": "Other", "slug": "other"},
		})
	}))
	defer srv.Close()

	projects, err := New(srv.URL).ListProjects(context.Background(), "tok")
	if err != nil {
		t.Fatalf("ListProjects err = %v", err)
	}
	if gotMethod != http.MethodGet || gotPath != "/api/projects" {
		t.Fatalf("request = %s %s, want GET /api/projects", gotMethod, gotPath)
	}
	if gotAuth != "Bearer tok" {
		t.Fatalf("Authorization = %q, want %q", gotAuth, "Bearer tok")
	}
	if len(projects) != 2 || projects[0].Slug != "my-app" || projects[1].ID != "p2" {
		t.Fatalf("projects = %+v, want the two encoded rows", projects)
	}
}

func TestListProjects_Unauthorized(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		json.NewEncoder(w).Encode(map[string]string{"error": "Unauthorized"})
	}))
	defer srv.Close()

	_, err := New(srv.URL).ListProjects(context.Background(), "tok")
	if err == nil || !IsUnauthorized(err) {
		t.Fatalf("ListProjects err = %v, want an unauthorized error", err)
	}
}
