package vps_test

import (
	"context"
	"errors"
	"testing"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/edge/gate"
	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/keyvalue"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/fake"
	"github.com/ocelhq/ocel/pkg/stackrecords"
	cloudflare "github.com/ocelhq/ocel/platform/edge/cloudflare/deploy"
	vps "github.com/ocelhq/ocel/platform/vps/provider"
	"github.com/ocelhq/ocel/platform/vps/provider/host"
)

var gateTiers = []environment.Tier{environment.TierPreview, environment.TierProduction}

func recordGates(t *testing.T, store keyvalue.Store, cipher *fake.Cipher, tier environment.Tier, gates map[string]gate.Gate) {
	t.Helper()
	err := stackrecords.ChangeGates(context.Background(), store, cipher, tier, func(records map[string]stackrecords.GateRecord) error {
		for slug, held := range gates {
			records[slug] = stackrecords.GateRecord{Gate: held, Password: "a password", Enabled: true}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func recordedGates(t *testing.T, tier environment.Tier, gates map[string]gate.Gate) (keyvalue.Store, *fake.Cipher) {
	t.Helper()
	store, cipher := fake.NewKeyValues(), fake.NewCipher()
	recordGates(t, store, cipher, tier, gates)
	return store, cipher
}

func TestTheGateOfAProjectIsTheOneRecordedForItsSlugAndNoOnesElse(t *testing.T) {
	t.Parallel()
	for _, tier := range gateTiers {
		store, cipher := recordedGates(t, tier, map[string]gate.Gate{"shop": {Key: "k"}, "blog": {Key: "j"}})
		read := vps.NewRecordedGate(store, cipher)

		shop, err := read(context.Background(), tier, "shop")
		if err != nil || shop == nil || shop.Key != "k" {
			t.Errorf("%s gate of shop = %+v, %v, want the one recorded for shop", tier, shop, err)
		}
		other, err := read(context.Background(), tier, "docs")
		if err != nil || other != nil {
			t.Errorf("%s gate of docs = %+v, %v, want none: no gate is recorded for it", tier, other, err)
		}
	}
}

func TestTheGateOfATierIsTheOneRecordedForThatTierAndNeverTheOthers(t *testing.T) {
	t.Parallel()
	store, cipher := fake.NewKeyValues(), fake.NewCipher()
	recordGates(t, store, cipher, environment.TierPreview, map[string]gate.Gate{"shop": {Key: "preview"}, "blog": {Key: "blog-preview"}})
	recordGates(t, store, cipher, environment.TierProduction, map[string]gate.Gate{"shop": {Key: "production"}})
	read := vps.NewRecordedGate(store, cipher)

	for tier, want := range map[environment.Tier]string{environment.TierPreview: "preview", environment.TierProduction: "production"} {
		held, err := read(context.Background(), tier, "shop")
		if err != nil || held == nil || held.Key != want {
			t.Errorf("%s gate of shop = %+v, %v, want key %q", tier, held, err, want)
		}
	}
	if held, err := read(context.Background(), environment.TierProduction, "blog"); err != nil || held != nil {
		t.Errorf("production gate of blog = %+v, %v, want none: only its previews are protected", held, err)
	}
}

func TestAProjectWhoseProtectionTurnedOffIsNotGatedThoughItsCredentialsAreKept(t *testing.T) {
	t.Parallel()
	for _, tier := range gateTiers {
		store, cipher := recordedGates(t, tier, map[string]gate.Gate{"shop": {Key: "k"}})
		err := stackrecords.ChangeGates(context.Background(), store, cipher, tier, func(records map[string]stackrecords.GateRecord) error {
			record := records["shop"]
			record.Enabled = false
			records["shop"] = record
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}

		held, err := vps.NewRecordedGate(store, cipher)(context.Background(), tier, "shop")

		if err != nil || held != nil {
			t.Errorf("%s gate of shop = %+v, %v, want none: its record is kept but disabled", tier, held, err)
		}
	}
}

func TestAProviderWithNoGateRecordedGatesNoProject(t *testing.T) {
	t.Parallel()
	for _, tier := range gateTiers {
		held, err := vps.NewRecordedGate(fake.NewKeyValues(), fake.NewCipher())(context.Background(), tier, "shop")

		if err != nil || held != nil {
			t.Errorf("%s gate = %+v, %v, want none", tier, held, err)
		}
	}
}

func TestAProviderThatCannotOpenTheRecordedGatesRefusesRatherThanServeUngated(t *testing.T) {
	t.Parallel()
	for _, tier := range gateTiers {
		store, _ := recordedGates(t, tier, map[string]gate.Gate{"shop": {Key: "k"}})

		if held, err := vps.NewRecordedGate(store, fake.NewCipher())(context.Background(), tier, "shop"); err == nil {
			t.Errorf("%s gate = %+v with no error, want the record that cannot be opened to fail the read", tier, held)
		}
	}
}

func TestABoxEdgeAndTheCloudflareProxyInFrontOfItBothGate(t *testing.T) {
	t.Setenv(provider.NamespaceEnvVar, "staging")
	p := vps.NewProvider(vps.Options{SSH: vps.Target{Host: "box.invalid", User: "ada"}})

	for _, kind := range []edge.Kind{edge.None, cloudflare.Kind} {
		front, err := p.Edges().Open(kind, nil)
		if err != nil {
			t.Fatalf("Edges().Open(%q) = %v", kind, err)
		}
		if !front.Facts().Gates {
			t.Errorf("the %q edge declares Gates = false, want true: what it fronts is gated by the switchboard behind it", kind)
		}
		if front.Hooks().PublishGate == nil {
			t.Errorf("the %q edge publishes no gate, and the switchboard is where the gate lives", kind)
		}
	}
}

func TestTheCloudflareProxyInFrontOfABoxHandsTheBoxTheGateAtTheEdgeStepOfEachTier(t *testing.T) {
	t.Setenv(provider.NamespaceEnvVar, "staging")
	unreached := errors.New("no box is reached to hold the gate")
	p := vps.ProviderOver(
		vps.Options{SSH: vps.Target{Host: "box.invalid", User: "ada"}},
		func(context.Context) (host.Conn, error) { return nil, unreached },
	)
	front, err := p.Edges().Open(cloudflare.Kind, nil)
	if err != nil {
		t.Fatal(err)
	}

	for _, tier := range gateTiers {
		_, err := front.Reconcile(context.Background(), edge.StackSpec{Version: "test", Tier: tier, Slug: "shop", Gate: &gate.Gate{Key: "k"}}, edge.StackState{})
		if !errors.Is(err, unreached) {
			t.Errorf("a protected %s Reconcile through the proxy = %v, want it to reach the box for the gate: a proxy step that leaves the box ungated serves what it fronts to anyone", tier, err)
		}
		err = front.Hooks().PublishGate(context.Background(), edge.GatePublication{State: edge.StackState{Slug: "shop", Tier: tier}, Gate: &gate.Gate{Key: "k"}})
		if !errors.Is(err, unreached) {
			t.Errorf("publishing the %s gate through the proxy = %v, want it to reach the box", tier, err)
		}
	}
}
