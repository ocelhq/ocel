package main

import (
	"context"
	"errors"
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

func TestAnExecutionPollsOnce(t *testing.T) {
	t.Run("and reports a failure without failing the execution", func(t *testing.T) {
		source := &poll{err: errors.New("infisical answered 503")}
		var said strings.Builder
		if code := (execution{syncer: source, errs: &said}).run(context.Background()); code != 0 {
			t.Errorf("run = %d, want 0: the status record already holds the failure and the backoff, and the next minute's execution is the retry", code)
		}
		if source.calls != 1 {
			t.Errorf("Poll ran %d times, want once per execution", source.calls)
		}
		if !strings.Contains(said.String(), "infisical answered 503") {
			t.Errorf("logged %q, want the failure in the job's log", said.String())
		}
	})

	t.Run("and says nothing when every source is in step", func(t *testing.T) {
		source := &poll{}
		var said strings.Builder
		if code := (execution{syncer: source, errs: &said}).run(context.Background()); code != 0 {
			t.Errorf("run = %d", code)
		}
		if said.Len() != 0 {
			t.Errorf("logged %q on a clean poll", said.String())
		}
	})
}
