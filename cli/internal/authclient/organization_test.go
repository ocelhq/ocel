package authclient

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestListOrganizations_ReturnsParsedList(t *testing.T) {
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
		json.NewEncoder(w).Encode([]Organization{
			{ID: "org_1", Name: "Acme Inc", Slug: "acme-inc"},
			{ID: "org_2", Name: "Beta LLC", Slug: "beta-llc"},
		})
	}))
	defer srv.Close()

	client := New(srv.URL)
	orgs, err := client.ListOrganizations(context.Background(), "tok")
	if err != nil {
		t.Fatalf("ListOrganizations err = %v", err)
	}
	if len(orgs) != 2 {
		t.Fatalf("len(orgs) = %d, want 2", len(orgs))
	}
	if orgs[0].Slug != "acme-inc" || orgs[1].Slug != "beta-llc" {
		t.Fatalf("orgs = %+v, want acme-inc and beta-llc", orgs)
	}
}

func TestListOrganizations_EmptyList(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode([]Organization{})
	}))
	defer srv.Close()

	client := New(srv.URL)
	orgs, err := client.ListOrganizations(context.Background(), "tok")
	if err != nil {
		t.Fatalf("ListOrganizations err = %v", err)
	}
	if len(orgs) != 0 {
		t.Fatalf("len(orgs) = %d, want 0", len(orgs))
	}
}

func TestSetActiveOrganization_SendsExpectedRequest(t *testing.T) {
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
}

func TestSetActiveOrganization_ErrorResponse(t *testing.T) {
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
}

func isAPIError(err error, target **APIError) bool {
	apiErr, ok := err.(*APIError)
	if !ok {
		return false
	}
	*target = apiErr
	return true
}
