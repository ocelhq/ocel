package env_test

import (
	"errors"
	"testing"

	"ocel.dev/env"
)

func TestDefineReturnsAMissingValueAsAnErrorInsteadOfPanicking(t *testing.T) {
	t.Setenv("OCEL_PHASE", "")

	_, err := env.Define[struct {
		Key string `ocel:"DEFINE_RETURNS_MISSING"`
	}]()

	var value *env.ValueError
	if !errors.As(err, &value) {
		t.Fatalf("Define() error = %v, want a *ValueError", err)
	}
}

func TestDefineReturnsTheStructWhenEveryValueIsDelivered(t *testing.T) {
	t.Setenv("OCEL_PHASE", "")
	t.Setenv("DEFINE_RETURNS_SET", "set")

	got, err := env.Define[struct {
		Key string `ocel:"DEFINE_RETURNS_SET"`
	}]()
	if err != nil {
		t.Fatalf("Define() error = %v", err)
	}
	if got.Key != "set" {
		t.Errorf("Key = %q, want %q", got.Key, "set")
	}
}
