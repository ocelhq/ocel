package gcp

import (
	"slices"
	"testing"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/provider"
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
