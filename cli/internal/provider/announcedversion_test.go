package provider

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/cli/internal/version"
)

func TestAProviderOfAnotherVersionIsRefusedBeforeAnyRPC(t *testing.T) {
	t.Parallel()

	runner, _ := spawnFake(t, context.Background(), "", Config{
		Env: []string{fakeProviderVersionEnvVar + "=9.9.9-from-another-release"},
	})

	err := runner.Ready(context.Background())
	if err == nil {
		t.Fatal("Ready() error = nil, want the mismatched version refused")
	}

	var mismatch *VersionMismatchError
	if !errors.As(err, &mismatch) {
		t.Fatalf("Ready() error = %v (%T), want a VersionMismatchError", err, err)
	}
	if mismatch.Announced != "9.9.9-from-another-release" || mismatch.Expected != version.Version {
		t.Fatalf("VersionMismatchError = %+v, want %q announced against %q", mismatch, "9.9.9-from-another-release", version.Version)
	}
	for _, want := range []string{"9.9.9-from-another-release", version.Version} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not name %q", err.Error(), want)
		}
	}

	if _, err := runner.Client(); !errors.Is(err, ErrClientUnavailable) {
		t.Fatalf("Client() error = %v, want %v — the refusal must land before the channel is opened", err, ErrClientUnavailable)
	}
}

func TestAProviderOfThisVersionIsAccepted(t *testing.T) {
	t.Parallel()

	runner, _ := spawnFake(t, context.Background(), "", Config{})
	if err := runner.Ready(context.Background()); err != nil {
		t.Fatalf("Ready() error = %v, want the provider of this CLI's own version accepted", err)
	}
}
