package gcp

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/refusal"
)

func TestASigningKeyIsMintedOnceAndKeptInItsOwnSecret(t *testing.T) {
	t.Parallel()

	h := newRealtimeHarness(t)
	first, again := h.provision(t, "app"), h.provision(t, "app")
	if first.Properties[provider.PropertySigningKey] != again.Properties[provider.PropertySigningKey] {
		t.Error("a second deploy handed the app another signing key, and every token the first minted would stop verifying")
	}
	if !h.secrets.has(h.env.clients.RealtimeSigningSecret("shop", "prod", "app")) {
		t.Error("the signing key is kept in no secret of its own")
	}
}

func TestOnlyTheGatewaysAccountMayReadTheKeysSecret(t *testing.T) {
	t.Parallel()

	h := newRealtimeHarness(t)
	h.provision(t, "app")
	readers := h.secrets.readers(h.env.clients.RealtimeKeysSecret("shop", "prod"), "roles/secretmanager.secretAccessor")
	if want := []string{"serviceAccount:" + h.env.clients.RealtimeAccountEmail(environment.TierProduction)}; !slices.Equal(readers, want) {
		t.Errorf("the keys secret may be read by %v, want %v", readers, want)
	}
}

func TestAnotherRealtimeIsTrustedThroughANewKeysVersionWithoutANewRevision(t *testing.T) {
	t.Parallel()

	h := newRealtimeHarness(t)
	app := h.provision(t, "app")
	revisions := h.run.standing()
	chat := h.provision(t, "chat")

	if got := h.keys(t); got["app"] != app.Properties[provider.PropertyVerifyKey] || got["chat"] != chat.Properties[provider.PropertyVerifyKey] || len(got) != 2 {
		t.Errorf("the keys are %v, want app's and chat's alone", got)
	}
	if got := h.run.standing(); !slices.Equal(got, revisions) {
		t.Errorf("revisions went from %v to %v, and a new revision leaves the old one's sockets on an instance publishes no longer reach", revisions, got)
	}
	if app.Properties[provider.PropertyHost] != chat.Properties[provider.PropertyHost] {
		t.Error("two realtimes of one environment are bound to two gateways")
	}
}

func TestTheKeysSecretKeepsOnlyTheVersionTheGatewayReads(t *testing.T) {
	t.Parallel()

	h := newRealtimeHarness(t)
	h.provision(t, "app")
	h.remove(t, h.provision(t, "chat"))

	if got := h.secrets.enabledVersions(h.env.clients.RealtimeKeysSecret("shop", "prod")); len(got) != 1 {
		t.Errorf("the keys secret holds versions %v readable, want its latest alone: each one superseded is billed and read by nothing", got)
	}
}

func TestASigningSecretWhoseFirstVersionWasDestroyedIsRefusedNamingTheSecretToDelete(t *testing.T) {
	t.Parallel()

	h := newRealtimeHarness(t)
	h.provision(t, "app")
	secret := h.env.clients.RealtimeSigningSecret("shop", "prod", "app")
	h.secrets.destroy(secret, 1)

	_, err := h.env.provision(context.Background(), aRealtime("app"), nil)
	var refused refusal.Refusal
	if !errors.As(err, &refused) || refused.Code != refusal.CodeNotReady || !strings.Contains(err.Error(), "Delete the secret") || !strings.Contains(err.Error(), secret) {
		t.Errorf("provision() = %v, want a not-ready refusal naming %s to delete", err, secret)
	}
}
