package main

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/platform/gcp/provider/ports"
)

func env(values map[string]string) func(string) string {
	return func(key string) string { return values[key] }
}

func complete() map[string]string {
	return map[string]string{
		provider.NamespaceEnvVar: "ocel",
		ports.ProjectEnvVar:      "acme-prod",
		ports.RegionEnvVar:       "europe-west1",
		ports.ClassEnvVar:        "production",
	}
}

func TestTheSyncRefusesToStartWithoutWhereItReadsAndWrites(t *testing.T) {
	t.Parallel()
	for _, missing := range []string{provider.NamespaceEnvVar, ports.ProjectEnvVar, ports.RegionEnvVar, ports.ClassEnvVar} {
		values := complete()
		delete(values, missing)
		if _, err := newSync(env(values)); err == nil || !strings.Contains(err.Error(), missing) {
			t.Errorf("newSync() without %s = %v, want a refusal naming it", missing, err)
		}
	}
}

func TestTheSyncRefusesAClassNoBootstrapInstalls(t *testing.T) {
	t.Parallel()
	values := complete()
	values[ports.ClassEnvVar] = "staging"
	if _, err := newSync(env(values)); err == nil || !strings.Contains(err.Error(), "production or preview") {
		t.Errorf("newSync() = %v, want the classes it takes named", err)
	}
}

func TestTheSyncRefusesANamespaceNoBootstrapCouldHaveNamed(t *testing.T) {
	t.Parallel()
	values := complete()
	values[provider.NamespaceEnvVar] = "Not A Namespace"
	if _, err := newSync(env(values)); err == nil {
		t.Error("newSync() took a namespace no database is named for")
	}
}

func TestTheSyncReadsItsClassFromThatProjectsRecordsAndLogsInAsItsOwnAccount(t *testing.T) {
	t.Parallel()
	values := complete()
	values[ports.ClassEnvVar] = "preview"
	sync, err := newSync(env(values))
	if err != nil {
		t.Fatalf("newSync() = %v", err)
	}
	if sync.Class != edge.ClassPreview {
		t.Errorf("Class = %q, want preview", sync.Class)
	}
	records, isFirestore := sync.Store.Records.(ports.Records)
	if !isFirestore || records.Clients.Project != "acme-prod" || records.Clients.Region != "europe-west1" || records.Clients.Namespace != "ocel" {
		t.Errorf("Records = %+v, want the ocel database of acme-prod in europe-west1", sync.Store.Records)
	}
	if _, isKMS := sync.Store.Cipher.(ports.Cipher); !isKMS {
		t.Errorf("Cipher = %T, want the class key in KMS", sync.Store.Cipher)
	}
	if sync.Login.ProveIdentity == nil {
		t.Error("Login has no ProveIdentity, so an Infisical env source with identity auth can never log in from here")
	}
	if sync.Login.Client == nil || sync.Login.Client.Timeout == 0 {
		t.Error("Login has no client with a timeout, so one slow env source keeps the request open until Cloud Run cuts it off")
	}
}

type onceCalls struct {
	calls int
	err   error
}

func (o *onceCalls) once(context.Context) error {
	o.calls++
	return o.err
}

func served(once *onceCalls, method, path string) (*httptest.ResponseRecorder, string) {
	var logged strings.Builder
	recorder := httptest.NewRecorder()
	newHandler(once.once, &logged).ServeHTTP(recorder, httptest.NewRequest(method, path, nil))
	return recorder, logged.String()
}

func TestAPostToTheRootSyncsOnceAndAnswers204(t *testing.T) {
	t.Parallel()
	once := &onceCalls{}
	recorder, logged := served(once, http.MethodPost, "/")
	if recorder.Code != http.StatusNoContent {
		t.Errorf("POST / = %d, want 204", recorder.Code)
	}
	if once.calls != 1 {
		t.Errorf("CopyScheduled ran %d times, want once per request", once.calls)
	}
	if logged != "" {
		t.Errorf("logged %q on a sync that went through", logged)
	}
}

func TestAFailedSyncIsLoggedAndStillAnswers204(t *testing.T) {
	t.Parallel()
	once := &onceCalls{err: errors.New("infisical answered 503")}
	recorder, logged := served(once, http.MethodPost, "/")
	if recorder.Code != http.StatusNoContent {
		t.Errorf("POST / = %d, want 204: the status record has the failure and the backoff, and a 5xx would have Cloud Scheduler retry on top of it", recorder.Code)
	}
	if !strings.Contains(logged, "infisical answered 503") {
		t.Errorf("logged %q, want the failure in the service's log", logged)
	}
}

func TestNothingButAPostToTheRootSyncs(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		method, path string
		want         int
	}{
		{http.MethodGet, "/", http.StatusMethodNotAllowed},
		{http.MethodPut, "/", http.StatusMethodNotAllowed},
		{http.MethodPost, "/sync", http.StatusNotFound},
		{http.MethodGet, "/healthz", http.StatusNotFound},
	} {
		once := &onceCalls{}
		recorder, _ := served(once, tc.method, tc.path)
		if recorder.Code != tc.want {
			t.Errorf("%s %s = %d, want %d", tc.method, tc.path, recorder.Code, tc.want)
		}
		if once.calls != 0 {
			t.Errorf("%s %s synced %d times, want none", tc.method, tc.path, once.calls)
		}
	}
}
