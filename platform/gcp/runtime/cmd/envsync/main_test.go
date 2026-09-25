package main

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/providerkit"
	"github.com/ocelhq/ocel/platform/gcp/provider/ports"
)

func env(values map[string]string) func(string) string {
	return func(key string) string { return values[key] }
}

func complete() map[string]string {
	return map[string]string{
		providerkit.NamespaceEnvVar: "ocel",
		ports.ProjectEnvVar:         "acme-prod",
		ports.RegionEnvVar:          "europe-west1",
		ports.ClassEnvVar:           "production",
	}
}

func TestNewSyncer(t *testing.T) {
	for _, missing := range []string{providerkit.NamespaceEnvVar, ports.ProjectEnvVar, ports.RegionEnvVar, ports.ClassEnvVar} {
		t.Run("refuses to start without "+missing, func(t *testing.T) {
			values := complete()
			delete(values, missing)
			_, err := newSyncer(env(values))
			if err == nil || !strings.Contains(err.Error(), missing) {
				t.Fatalf("newSyncer = %v, want %s named", err, missing)
			}
		})
	}

	t.Run("refuses a class no bootstrap stands", func(t *testing.T) {
		values := complete()
		values[ports.ClassEnvVar] = "staging"
		_, err := newSyncer(env(values))
		if err == nil || !strings.Contains(err.Error(), "production or preview") {
			t.Fatalf("newSyncer = %v, want the classes it takes named", err)
		}
	})

	t.Run("refuses a namespace no bootstrap could have named", func(t *testing.T) {
		values := complete()
		values[providerkit.NamespaceEnvVar] = "Not A Namespace"
		if _, err := newSyncer(env(values)); err == nil {
			t.Fatal("newSyncer took a namespace no database is named for")
		}
	})

	t.Run("polls the class it was stood for over that project's records and key", func(t *testing.T) {
		values := complete()
		values[ports.ClassEnvVar] = "preview"
		syncer, err := newSyncer(env(values))
		if err != nil {
			t.Fatalf("newSyncer = %v", err)
		}
		if syncer.Class != "preview" {
			t.Errorf("Class = %q, want preview", syncer.Class)
		}
		records, reads := syncer.Store.Records.(ports.Records)
		if !reads || records.Clients.Project != "acme-prod" || records.Clients.Region != "europe-west1" || records.Clients.Namespace != "ocel" {
			t.Errorf("Records = %+v, want the ocel database of acme-prod in europe-west1", syncer.Store.Records)
		}
		if syncer.Target.Issuer == nil {
			t.Error("Target carries no issuer, so an Infisical source with gcp auth can never sign in from here")
		}
	})
}

type poll struct {
	calls int
	err   error
}

func (p *poll) Poll(context.Context) error {
	p.calls++
	return p.err
}

func asked(t *testing.T, syncer poller, method, path string) (*httptest.ResponseRecorder, string) {
	t.Helper()
	var said strings.Builder
	answered := httptest.NewRecorder()
	serving(syncer, &said).ServeHTTP(answered, httptest.NewRequest(method, path, nil))
	return answered, said.String()
}

func TestAPostPollsOnce(t *testing.T) {
	t.Run("and answers 204 when every source is in step", func(t *testing.T) {
		source := &poll{}
		answered, said := asked(t, source, http.MethodPost, "/")
		if answered.Code != http.StatusNoContent {
			t.Errorf("POST / = %d, want 204", answered.Code)
		}
		if source.calls != 1 {
			t.Errorf("Poll ran %d times, want once per request", source.calls)
		}
		if said != "" {
			t.Errorf("logged %q on a clean poll", said)
		}
	})

	t.Run("and reports a failure without failing the request", func(t *testing.T) {
		source := &poll{err: errors.New("infisical answered 503")}
		answered, said := asked(t, source, http.MethodPost, "/")
		if answered.Code != http.StatusNoContent {
			t.Errorf("POST / = %d, want 204: the status record already holds the failure and the backoff, and a 5xx would have Cloud Scheduler retry on top of it", answered.Code)
		}
		if source.calls != 1 {
			t.Errorf("Poll ran %d times, want once per request", source.calls)
		}
		if !strings.Contains(said, "infisical answered 503") {
			t.Errorf("logged %q, want the failure in the service's log", said)
		}
	})
}

func TestNothingButAPostToTheRootPolls(t *testing.T) {
	for _, tc := range []struct {
		method, path string
		want         int
	}{
		{http.MethodGet, "/", http.StatusMethodNotAllowed},
		{http.MethodPut, "/", http.StatusMethodNotAllowed},
		{http.MethodPost, "/sync", http.StatusNotFound},
		{http.MethodGet, "/healthz", http.StatusNotFound},
	} {
		source := &poll{}
		answered, _ := asked(t, source, tc.method, tc.path)
		if answered.Code != tc.want {
			t.Errorf("%s %s = %d, want %d", tc.method, tc.path, answered.Code, tc.want)
		}
		if source.calls != 0 {
			t.Errorf("%s %s polled %d times, want none", tc.method, tc.path, source.calls)
		}
	}
}
