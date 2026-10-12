package vps_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/edge/previewgate"
	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/keyvalue"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/fake"
	"github.com/ocelhq/ocel/pkg/stackrecords"
	cloudflare "github.com/ocelhq/ocel/platform/edge/cloudflare/deploy"
	vps "github.com/ocelhq/ocel/platform/vps/provider"
)

func recordedPreviewGates(t *testing.T, gates map[string]previewgate.Gate) (keyvalue.Store, *fake.Cipher) {
	t.Helper()
	store, cipher := fake.NewKeyValues(), fake.NewCipher()
	plaintext, err := json.Marshal(gates)
	if err != nil {
		t.Fatal(err)
	}
	sealed, err := cipher.Seal(context.Background(), environment.TierPreview, stackrecords.PreviewGatesAssociatedData, plaintext)
	if err != nil {
		t.Fatal(err)
	}
	value, err := json.Marshal(sealed)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Write(context.Background(), keyvalue.Entry{Key: stackrecords.NewPreviewGatesRecordKey(), Value: value}); err != nil {
		t.Fatal(err)
	}
	return store, cipher
}

func TestThePreviewGateOfAProjectIsTheOneRecordedForItsSlugAndNoOnesElse(t *testing.T) {
	t.Parallel()
	store, cipher := recordedPreviewGates(t, map[string]previewgate.Gate{"shop": {Key: "k"}, "blog": {Key: "j"}})
	read := vps.NewPreviewGateReader(store, cipher)

	shop, err := read(context.Background(), "shop")
	if err != nil || shop == nil || shop.Key != "k" {
		t.Errorf("gate of shop = %+v, %v, want the one recorded for shop", shop, err)
	}
	other, err := read(context.Background(), "docs")
	if err != nil || other != nil {
		t.Errorf("gate of docs = %+v, %v, want none: no gate is recorded for it", other, err)
	}
}

func TestAProviderWithNoPreviewGateRecordedGatesNoProject(t *testing.T) {
	t.Parallel()

	gate, err := vps.NewPreviewGateReader(fake.NewKeyValues(), fake.NewCipher())(context.Background(), "shop")

	if err != nil || gate != nil {
		t.Errorf("gate = %+v, %v, want none", gate, err)
	}
}

func TestAProviderThatCannotOpenTheRecordedPreviewGatesRefusesRatherThanServeAPreviewUngated(t *testing.T) {
	t.Parallel()
	store, _ := recordedPreviewGates(t, map[string]previewgate.Gate{"shop": {Key: "k"}})

	if gate, err := vps.NewPreviewGateReader(store, fake.NewCipher())(context.Background(), "shop"); err == nil {
		t.Errorf("gate = %+v with no error, want the record that cannot be opened to fail the read", gate)
	}
}

func TestABoxEdgeAndTheCloudflareProxyInFrontOfItBothGateTheirPreviews(t *testing.T) {
	t.Setenv(provider.NamespaceEnvVar, "staging")
	p := vps.NewProvider(vps.Options{SSH: vps.Target{Host: "box.invalid", User: "ada"}})

	for _, kind := range []edge.Kind{edge.None, cloudflare.Kind} {
		front, err := p.Edges().Open(kind, nil)
		if err != nil {
			t.Fatalf("Edges().Open(%q) = %v", kind, err)
		}
		if !front.Facts().GatesPreviews {
			t.Errorf("the %q edge declares GatesPreviews = false, want true: previews it fronts are gated by the switchboard behind it", kind)
		}
	}
}
