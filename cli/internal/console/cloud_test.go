package console

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestReadCloudOfferNamesWhereTheConsolesOcelCloudIs(t *testing.T) {
	t.Parallel()

	var bearer string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/api/cloud" {
			http.NotFound(w, r)
			return
		}
		bearer = r.Header.Get("Authorization")
		_ = json.NewEncoder(w).Encode(map[string]string{"url": "https://cloud.example.test", "audience": "ocel-cloud"})
	}))
	t.Cleanup(srv.Close)

	offer, err := New(srv.URL).ReadCloudOffer(context.Background(), "session-token")
	if err != nil {
		t.Fatalf("ReadCloudOffer: %v", err)
	}
	if offer != (CloudOffer{URL: "https://cloud.example.test"}) {
		t.Errorf("ReadCloudOffer = %+v, want the console's url", offer)
	}
	if bearer != "Bearer session-token" {
		t.Errorf("Authorization = %q, want the session", bearer)
	}
}

func TestReadCloudOfferIsErrCloudNotOfferedWhenTheConsoleAnswersNotFound(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "not_offered", "error_description": "this console offers no Ocel Cloud"})
	}))
	t.Cleanup(srv.Close)

	if _, err := New(srv.URL).ReadCloudOffer(context.Background(), "session-token"); !errors.Is(err, ErrCloudNotOffered) {
		t.Fatalf("ReadCloudOffer = %v, want ErrCloudNotOffered", err)
	}
}

func TestReadCloudOfferPassesOnAnyOtherRefusal(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	t.Cleanup(srv.Close)

	_, err := New(srv.URL).ReadCloudOffer(context.Background(), "session-token")
	var refused *Error
	if !errors.As(err, &refused) || refused.StatusCode != http.StatusUnauthorized || errors.Is(err, ErrCloudNotOffered) {
		t.Fatalf("ReadCloudOffer = %v, want the console's 401", err)
	}
}

func TestIssueCloudTokenSwapsTheSessionForAnOcelCloudToken(t *testing.T) {
	t.Parallel()

	var bearer string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/cloud/token" {
			http.NotFound(w, r)
			return
		}
		bearer = r.Header.Get("Authorization")
		_ = json.NewEncoder(w).Encode(map[string]string{"token": "cloud-token", "expiresAt": "2026-10-17T12:05:00Z"})
	}))
	t.Cleanup(srv.Close)

	issued, err := New(srv.URL).IssueCloudToken(context.Background(), "session-token")
	if err != nil {
		t.Fatalf("IssueCloudToken: %v", err)
	}
	want := CloudToken{Token: "cloud-token", ExpiresAt: time.Date(2026, 10, 17, 12, 5, 0, 0, time.UTC)}
	if !issued.ExpiresAt.Equal(want.ExpiresAt) || issued.Token != want.Token || bearer != "Bearer session-token" {
		t.Errorf("IssueCloudToken = %+v with Authorization %q, want %+v for the session", issued, bearer, want)
	}
}
