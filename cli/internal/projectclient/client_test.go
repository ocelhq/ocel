package projectclient

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func newTestServer(t *testing.T, handler http.HandlerFunc) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	return srv
}

func statusServer(t *testing.T, status int, body string) *httptest.Server {
	t.Helper()
	return newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(status)
		w.Write([]byte(body))
	})
}

func TestCreateProject(t *testing.T) {
	t.Parallel()

	t.Run("posts the name and slug to the projects endpoint with a bearer token", func(t *testing.T) {
		t.Parallel()

		var gotBody map[string]string
		var gotMethod, gotPath, gotAuth string
		srv := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
			gotMethod, gotPath, gotAuth = r.Method, r.URL.Path, r.Header.Get("Authorization")
			json.NewDecoder(r.Body).Decode(&gotBody)
			w.WriteHeader(http.StatusCreated)
			json.NewEncoder(w).Encode(Project{
				ID:             "proj_1",
				OrganizationID: "org_1",
				Name:           "My App",
				Slug:           "my-app",
			})
		})

		client := New(srv.URL)
		project, err := client.CreateProject(context.Background(), "tok", "My App", "my-app")
		if err != nil {
			t.Fatalf("CreateProject err = %v", err)
		}
		if gotMethod != http.MethodPost {
			t.Errorf("method = %s, want POST", gotMethod)
		}
		if gotPath != "/api/projects" {
			t.Errorf("path = %s, want /api/projects", gotPath)
		}
		if gotAuth != "Bearer tok" {
			t.Errorf("Authorization header = %q, want %q", gotAuth, "Bearer tok")
		}
		if project.ID != "proj_1" {
			t.Fatalf("project.ID = %q, want proj_1", project.ID)
		}
		if gotBody["name"] != "My App" || gotBody["slug"] != "my-app" {
			t.Fatalf("request body = %+v, want name=My App slug=my-app", gotBody)
		}
	})

	cases := []struct {
		name             string
		status           int
		body             string
		wantConflict     bool
		wantUnauthorized bool
	}{
		{
			name:         "a taken slug is classified as a conflict, not as unauthorized",
			status:       http.StatusConflict,
			body:         `{"error":"A project with this slug already exists in this organization"}`,
			wantConflict: true,
		},
		{
			name:             "a rejected token is classified as unauthorized",
			status:           http.StatusUnauthorized,
			body:             `{"error":"Unauthorized"}`,
			wantUnauthorized: true,
		},
		{
			name:   "any other failure is neither a conflict nor unauthorized",
			status: http.StatusInternalServerError,
			body:   "boom",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			srv := statusServer(t, tc.status, tc.body)

			client := New(srv.URL)
			_, err := client.CreateProject(context.Background(), "tok", "My App", "my-app")
			if err == nil {
				t.Fatal("CreateProject err = nil, want error")
			}
			if got := IsConflict(err); got != tc.wantConflict {
				t.Fatalf("IsConflict(%v) = %t, want %t", err, got, tc.wantConflict)
			}
			if got := IsUnauthorized(err); got != tc.wantUnauthorized {
				t.Fatalf("IsUnauthorized(%v) = %t, want %t", err, got, tc.wantUnauthorized)
			}
		})
	}
}

func TestListProjects(t *testing.T) {
	t.Parallel()

	t.Run("gets the projects endpoint with a bearer token and decodes every row", func(t *testing.T) {
		t.Parallel()

		var gotAuth, gotMethod, gotPath string
		srv := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
			gotAuth, gotMethod, gotPath = r.Header.Get("Authorization"), r.Method, r.URL.Path
			json.NewEncoder(w).Encode([]map[string]string{
				{"id": "p1", "organizationId": "org_1", "name": "My App", "slug": "my-app"},
				{"id": "p2", "organizationId": "org_1", "name": "Other", "slug": "other"},
			})
		})

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
	})

	t.Run("a rejected token is classified as unauthorized", func(t *testing.T) {
		t.Parallel()

		srv := statusServer(t, http.StatusUnauthorized, `{"error":"Unauthorized"}`)

		_, err := New(srv.URL).ListProjects(context.Background(), "tok")
		if err == nil || !IsUnauthorized(err) {
			t.Fatalf("ListProjects err = %v, want an unauthorized error", err)
		}
	})
}
