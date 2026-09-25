package main

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func env(values map[string]string) func(string) string {
	return func(key string) string { return values[key] }
}

func complete() map[string]string {
	return map[string]string{
		varsTableEnvVar: "ocel-bootstrap-VarsTable-1",
		varsKeyEnvVar:   "arn:aws:kms:us-east-1:111122223333:key/vars",
		classEnvVar:     "production",
	}
}

func TestNewSyncer(t *testing.T) {
	for _, missing := range []string{varsTableEnvVar, varsKeyEnvVar, classEnvVar} {
		t.Run("refuses to start without "+missing, func(t *testing.T) {
			values := complete()
			delete(values, missing)
			_, err := newSyncer(context.Background(), env(values))
			if err == nil || !strings.Contains(err.Error(), missing) {
				t.Fatalf("newSyncer = %v, want %s named", err, missing)
			}
		})
	}

	t.Run("refuses a class no bootstrap stands", func(t *testing.T) {
		values := complete()
		values[classEnvVar] = "staging"
		_, err := newSyncer(context.Background(), env(values))
		if err == nil || !strings.Contains(err.Error(), "production or preview") {
			t.Fatalf("newSyncer = %v, want the classes it takes named", err)
		}
	})

	t.Run("polls the class it was stood for", func(t *testing.T) {
		values := complete()
		values[classEnvVar] = "preview"
		syncer, err := newSyncer(context.Background(), env(values))
		if err != nil {
			t.Fatalf("newSyncer = %v", err)
		}
		if syncer.Class != "preview" {
			t.Errorf("Class = %q, want preview", syncer.Class)
		}
		if syncer.Target.Signer == nil {
			t.Error("Target carries no signer, so an Infisical source with aws auth can never sign in from here")
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

func TestAnInvocationPollsOnce(t *testing.T) {
	t.Run("and reports a failure without handing it to lambda", func(t *testing.T) {
		source := &poll{err: errors.New("infisical answered 503")}
		var said strings.Builder
		if err := (invocation{syncer: source, errs: &said}).handle(context.Background()); err != nil {
			t.Errorf("handle = %v, want nil: lambda retries a failed async invocation, and the status record already holds the backoff", err)
		}
		if source.calls != 1 {
			t.Errorf("Poll ran %d times, want once per invocation", source.calls)
		}
		if !strings.Contains(said.String(), "infisical answered 503") {
			t.Errorf("logged %q, want the failure in the function's log", said.String())
		}
	})

	t.Run("and says nothing when every source is in step", func(t *testing.T) {
		source := &poll{}
		var said strings.Builder
		if err := (invocation{syncer: source, errs: &said}).handle(context.Background()); err != nil {
			t.Errorf("handle = %v", err)
		}
		if said.Len() != 0 {
			t.Errorf("logged %q on a clean poll", said.String())
		}
	})
}
