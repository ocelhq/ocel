package run

import (
	"encoding/json"
	"strings"
	"testing"

	runv2 "google.golang.org/api/run/v2"

	"github.com/ocelhq/ocel/platform/gcp/provider/internal/names"
)

func pinnedServing() Serving {
	return Serving{
		Service: "ocel-app-shop-pr-7-web",
		Image:   "europe-west1-docker.pkg.dev/acme/ocel/web@sha256:abc",
		Env:     map[string]string{"MODE": "preview", "STRIPE_KEY": "sk_live_hidden"},
		Pinned: PinnedValues{
			Owner:    "ocel-app-0123456789",
			Versions: map[string]SecretVersion{"STRIPE_KEY": {Secret: "ocel_values-shop-pr-7-web-stripe-key-abc123", Version: "3"}},
		},
	}
}

func envNamed(service *runv2.GoogleCloudRunV2Service, name string) *runv2.GoogleCloudRunV2EnvVar {
	for _, entry := range service.Template.Containers[0].Env {
		if entry.Name == name {
			return entry
		}
	}
	return nil
}

func TestAPinnedValueIsRenderedAsAReferenceToTheSecretVersionAndNeverAsALiteral(t *testing.T) {
	service, err := ServiceOf(pinnedServing())
	if err != nil {
		t.Fatal(err)
	}

	pinned := envNamed(service, "STRIPE_KEY")
	if pinned == nil || pinned.Value != "" || pinned.ValueSource == nil || pinned.ValueSource.SecretKeyRef == nil ||
		pinned.ValueSource.SecretKeyRef.Secret != "ocel_values-shop-pr-7-web-stripe-key-abc123" || pinned.ValueSource.SecretKeyRef.Version != "3" {
		t.Fatalf("STRIPE_KEY = %+v, want a reference to version 3 of its secret and no value", pinned)
	}
	if plain := envNamed(service, "MODE"); plain == nil || plain.Value != "preview" || plain.ValueSource != nil {
		t.Errorf("MODE = %+v, want the plain value written as it is", plain)
	}
	encoded, err := json.Marshal(service)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "sk_live_hidden") {
		t.Errorf("the service spec holds the sensitive value itself, and anyone who may list services reads it: %s", encoded)
	}
}

func TestARevisionPinningValuesIsLabelledWithTheAccountThatOwnsThem(t *testing.T) {
	service, err := ServiceOf(pinnedServing())
	if err != nil {
		t.Fatal(err)
	}
	if got := service.Template.Labels[names.ValueOwnerLabel]; got != "ocel-app-0123456789" {
		t.Errorf("revision label %s = %q, want the app account, so a retirement finds every revision still pinning its values", names.ValueOwnerLabel, got)
	}

	plain := pinnedServing()
	plain.Pinned = PinnedValues{}
	unpinned, err := ServiceOf(plain)
	if err != nil {
		t.Fatal(err)
	}
	if _, labelled := unpinned.Template.Labels[names.ValueOwnerLabel]; labelled {
		t.Errorf("a revision pinning no value is labelled %s", names.ValueOwnerLabel)
	}
}

func TestAPinnedValueMovingToAnotherVersionIsDrift(t *testing.T) {
	current, err := ServiceOf(pinnedServing())
	if err != nil {
		t.Fatal(err)
	}
	moved := pinnedServing()
	moved.Pinned.Versions = map[string]SecretVersion{"STRIPE_KEY": {Secret: "ocel_values-shop-pr-7-web-stripe-key-abc123", Version: "4"}}
	desired, err := ServiceOf(moved)
	if err != nil {
		t.Fatal(err)
	}
	if !Drifted(current, desired) {
		t.Error("a service pinning version 3 reads as current against one pinning version 4, so the new value would never reach it")
	}
}
