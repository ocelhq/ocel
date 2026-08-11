package authclient

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestListOrganizations(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		orgs      []Organization
		wantSlugs []string
	}{
		{
			name: "returns the parsed list",
			orgs: []Organization{
				{ID: "org_1", Name: "Acme Inc", Slug: "acme-inc"},
				{ID: "org_2", Name: "Beta LLC", Slug: "beta-llc"},
			},
			wantSlugs: []string{"acme-inc", "beta-llc"},
		},
		{
			name:      "returns an empty list",
			orgs:      []Organization{},
			wantSlugs: nil,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodGet {
					t.Errorf("method = %s, want GET", r.Method)
				}
				if r.URL.Path != "/api/auth/organization/list" {
					t.Errorf("path = %s, want /api/auth/organization/list", r.URL.Path)
				}
				if got := r.Header.Get("Authorization"); got != "Bearer tok" {
					t.Errorf("Authorization header = %q, want %q", got, "Bearer tok")
				}
				json.NewEncoder(w).Encode(tt.orgs)
			}))
			defer srv.Close()

			client := New(srv.URL)
			orgs, err := client.ListOrganizations(context.Background(), "tok")
			if err != nil {
				t.Fatalf("ListOrganizations err = %v", err)
			}
			if len(orgs) != len(tt.wantSlugs) {
				t.Fatalf("len(orgs) = %d, want %d", len(orgs), len(tt.wantSlugs))
			}
			for i, want := range tt.wantSlugs {
				if orgs[i].Slug != want {
					t.Fatalf("orgs = %+v, want slugs %v", orgs, tt.wantSlugs)
				}
			}
		})
	}
}

func TestSetActiveOrganization(t *testing.T) {
	t.Parallel()

	t.Run("sends the expected request", func(t *testing.T) {
		t.Parallel()

		var gotBody map[string]string
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodPost {
				t.Errorf("method = %s, want POST", r.Method)
			}
			if r.URL.Path != "/api/auth/organization/set-active" {
				t.Errorf("path = %s, want /api/auth/organization/set-active", r.URL.Path)
			}
			if got := r.Header.Get("Authorization"); got != "Bearer tok" {
				t.Errorf("Authorization header = %q, want %q", got, "Bearer tok")
			}
			json.NewDecoder(r.Body).Decode(&gotBody)
			w.WriteHeader(http.StatusOK)
			w.Write([]byte("{}"))
		}))
		defer srv.Close()

		client := New(srv.URL)
		if err := client.SetActiveOrganization(context.Background(), "tok", "org_1"); err != nil {
			t.Fatalf("SetActiveOrganization err = %v", err)
		}
		if gotBody["organizationId"] != "org_1" {
			t.Fatalf("request body organizationId = %q, want org_1", gotBody["organizationId"])
		}
	})

	t.Run("surfaces an error response as an APIError", func(t *testing.T) {
		t.Parallel()

		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusBadRequest)
			json.NewEncoder(w).Encode(apiError{Error: "invalid_request", ErrorDescription: "organization not found"})
		}))
		defer srv.Close()

		client := New(srv.URL)
		err := client.SetActiveOrganization(context.Background(), "tok", "org_missing")
		if err == nil {
			t.Fatal("SetActiveOrganization err = nil, want error")
		}
		var apiErr *APIError
		if !isAPIError(err, &apiErr) {
			t.Fatalf("err = %v (%T), want *APIError", err, err)
		}
		if apiErr.Code != "invalid_request" {
			t.Fatalf("apiErr.Code = %q, want invalid_request", apiErr.Code)
		}
	})
}

func isAPIError(err error, target **APIError) bool {
	apiErr, ok := err.(*APIError)
	if !ok {
		return false
	}
	*target = apiErr
	return true
}
