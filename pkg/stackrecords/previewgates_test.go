package stackrecords_test

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"

	"github.com/ocelhq/ocel/pkg/edge/previewgate"
	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/keyvalue"
	"github.com/ocelhq/ocel/pkg/provider/fake"
	"github.com/ocelhq/ocel/pkg/stackrecords"
)

func recordGates(t *testing.T, store keyvalue.Store, cipher *fake.Cipher, gates map[string]previewgate.Gate) {
	t.Helper()
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
}

func TestReadPreviewGatesOpensTheSealedRecordIntoAMapByProjectSlug(t *testing.T) {
	t.Parallel()
	store, cipher := fake.NewKeyValues(), fake.NewCipher()
	want := map[string]previewgate.Gate{
		"shop": {Key: "k", PasswordMAC: "mac", BypassSecrets: []string{"s"}, AllowOptions: []string{"/api"}},
		"blog": {Key: "j", BypassSecrets: []string{}, AllowOptions: []string{}},
	}
	recordGates(t, store, cipher, want)

	got, err := stackrecords.ReadPreviewGates(context.Background(), store, cipher)

	if err != nil || !reflect.DeepEqual(got, want) {
		t.Errorf("ReadPreviewGates() = %+v, %v, want %+v", got, err, want)
	}
}

func TestReadPreviewGatesOfAProviderWithNoRecordIsNoGates(t *testing.T) {
	t.Parallel()

	got, err := stackrecords.ReadPreviewGates(context.Background(), fake.NewKeyValues(), fake.NewCipher())

	if err != nil || len(got) != 0 {
		t.Errorf("ReadPreviewGates() = %+v, %v, want none", got, err)
	}
}

func TestReadPreviewGatesRefusesARecordSealedUnderAnotherTierOrContext(t *testing.T) {
	t.Parallel()
	store, cipher := fake.NewKeyValues(), fake.NewCipher()
	sealed, err := cipher.Seal(context.Background(), environment.TierPreview, nil, []byte(`{"shop":{"key":"k"}}`))
	if err != nil {
		t.Fatal(err)
	}
	value, _ := json.Marshal(sealed)
	if _, err := store.Write(context.Background(), keyvalue.Entry{Key: stackrecords.NewPreviewGatesRecordKey(), Value: value}); err != nil {
		t.Fatal(err)
	}

	if _, err := stackrecords.ReadPreviewGates(context.Background(), store, cipher); err == nil {
		t.Error("ReadPreviewGates() opened a record sealed without its association, want it refused")
	}
}
