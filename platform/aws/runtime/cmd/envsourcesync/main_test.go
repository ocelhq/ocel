package main

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/environment"
)

func env(values map[string]string) func(string) string {
	return func(key string) string { return values[key] }
}

func complete() map[string]string {
	return map[string]string{
		"OCEL_VARS_TABLE": "ocel-bootstrap-VarsTable-1",
		"OCEL_VARS_KEY":   "arn:aws:kms:us-east-1:111122223333:key/vars",
		"OCEL_INFRA_TIER": "production",
	}
}

func TestTheSyncStartsOnlyWithWhereItReadsAndWritesAndLogsInAsItsOwnRole(t *testing.T) {
	t.Setenv("AWS_REGION", "us-east-1")

	for _, missing := range []string{"OCEL_VARS_TABLE", "OCEL_VARS_KEY", "OCEL_INFRA_TIER"} {
		t.Run("refuses to start without "+missing, func(t *testing.T) {
			values := complete()
			delete(values, missing)
			_, err := newSync(context.Background(), env(values))
			if err == nil || !strings.Contains(err.Error(), missing) {
				t.Fatalf("newSync = %v, want %s named", err, missing)
			}
		})
	}

	t.Run("refuses a tier no bootstrap makes", func(t *testing.T) {
		values := complete()
		values["OCEL_INFRA_TIER"] = "staging"
		_, err := newSync(context.Background(), env(values))
		if err == nil || !strings.Contains(err.Error(), "production or preview") {
			t.Fatalf("newSync = %v, want the tiers it takes named", err)
		}
	})

	t.Run("syncs the tier it was made for and proves its identity as its own role", func(t *testing.T) {
		values := complete()
		values["OCEL_INFRA_TIER"] = "preview"
		sync, err := newSync(context.Background(), env(values))
		if err != nil {
			t.Fatalf("newSync = %v", err)
		}
		if sync.Tier != environment.TierPreview {
			t.Errorf("Tier = %q, want preview", sync.Tier)
		}
		if sync.Login.ProveIdentity == nil {
			t.Error("Login has no ProveIdentity, so an Infisical env source with identity auth can never log in from here")
		}
		if sync.Login.Client == nil || sync.Login.Client.Timeout <= 0 {
			t.Error("Login.Client has no timeout, so one Infisical that never answers keeps the invocation running until Lambda kills it")
		}
		if sync.Store.KeyValues == nil || sync.Store.Cipher == nil {
			t.Error("Store has no key values or no cipher, so nothing read could be written")
		}
	})
}

func TestAnInvocationSyncsOnce(t *testing.T) {
	t.Run("and logs a failure without handing it to lambda", func(t *testing.T) {
		calls := 0
		var logged strings.Builder
		once := func(context.Context) error {
			calls++
			return errors.New("the vars table answered 500")
		}
		if err := (invocation{copyScheduled: once, log: &logged}).handle(context.Background()); err != nil {
			t.Errorf("handle = %v, want nil: lambda retries a failed async invocation, and the status record already has the backoff", err)
		}
		if calls != 1 {
			t.Errorf("CopyScheduled ran %d times, want once per invocation", calls)
		}
		if !strings.Contains(logged.String(), "the vars table answered 500") {
			t.Errorf("logged %q, want the failure in the function's log", logged.String())
		}
	})

	t.Run("and logs nothing when every env source is current", func(t *testing.T) {
		var logged strings.Builder
		once := func(context.Context) error { return nil }
		if err := (invocation{copyScheduled: once, log: &logged}).handle(context.Background()); err != nil {
			t.Errorf("handle = %v", err)
		}
		if logged.Len() != 0 {
			t.Errorf("logged %q on a clean sync", logged.String())
		}
	})
}
