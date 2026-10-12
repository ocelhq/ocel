package stackrecords_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"

	"github.com/ocelhq/ocel/pkg/edge/previewgate"
	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/keyvalue"
	"github.com/ocelhq/ocel/pkg/provider/fake"
	"github.com/ocelhq/ocel/pkg/stackrecords"
)

func recordGates(t *testing.T, store keyvalue.Store, cipher *fake.Cipher, records map[string]stackrecords.PreviewGateRecord) {
	t.Helper()
	plaintext, err := json.Marshal(records)
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
	want := map[string]stackrecords.PreviewGateRecord{
		"shop": {Gate: previewgate.Gate{Key: "k", PasswordMAC: "mac", BypassSecrets: []string{"s"}, AllowOptions: []string{"/api"}}, Password: "correct horse battery"},
		"blog": {Gate: previewgate.Gate{Key: "j", BypassSecrets: []string{}, AllowOptions: []string{}}, Pending: true},
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
	sealed, err := cipher.Seal(context.Background(), environment.TierPreview, nil, []byte(`{"shop":{"gate":{"key":"k"}}}`))
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

func TestChangePreviewGatesRecordsWhatReadPreviewGatesOpens(t *testing.T) {
	t.Parallel()
	store, cipher := fake.NewKeyValues(), fake.NewCipher()
	blog := stackrecords.PreviewGateRecord{Gate: previewgate.Gate{Key: "j", BypassSecrets: []string{}, AllowOptions: []string{}}}
	recordGates(t, store, cipher, map[string]stackrecords.PreviewGateRecord{"blog": blog})
	shop := stackrecords.PreviewGateRecord{Gate: previewgate.Gate{Key: "k", PasswordMAC: "mac", BypassSecrets: []string{"s"}, AllowOptions: []string{}}, Password: "pw"}

	err := stackrecords.ChangePreviewGates(context.Background(), store, cipher, func(records map[string]stackrecords.PreviewGateRecord) error {
		records["shop"] = shop
		return nil
	})

	got, readErr := stackrecords.ReadPreviewGates(context.Background(), store, cipher)
	want := map[string]stackrecords.PreviewGateRecord{"blog": blog, "shop": shop}
	if err != nil || readErr != nil || !reflect.DeepEqual(got, want) {
		t.Errorf("ReadPreviewGates() after ChangePreviewGates() = %+v, %v (change error %v), want %+v", got, readErr, err, want)
	}
}

func TestChangePreviewGatesThatRefusesLeavesTheRecordUnwritten(t *testing.T) {
	t.Parallel()
	store, cipher := fake.NewKeyValues(), fake.NewCipher()

	err := stackrecords.ChangePreviewGates(context.Background(), store, cipher, func(records map[string]stackrecords.PreviewGateRecord) error {
		records["shop"] = stackrecords.PreviewGateRecord{Gate: previewgate.Gate{Key: "k"}}
		return errors.New("refused")
	})

	recorded, readErr := keyvalue.ReadOrEmpty(context.Background(), store, stackrecords.NewPreviewGatesRecordKey())
	if err == nil || readErr != nil || len(recorded.Value) != 0 {
		t.Errorf("ChangePreviewGates() = %v and the record holds %q, want the refusal and no record", err, recorded.Value)
	}
}

func TestChangePreviewGatesThatChangesNothingWritesNothing(t *testing.T) {
	t.Parallel()
	store, cipher := fake.NewKeyValues(), fake.NewCipher()
	recordGates(t, store, cipher, map[string]stackrecords.PreviewGateRecord{"shop": {Gate: previewgate.Gate{Key: "k", BypassSecrets: []string{}, AllowOptions: []string{}}, Password: "pw"}})
	before, err := keyvalue.ReadOrEmpty(context.Background(), store, stackrecords.NewPreviewGatesRecordKey())
	if err != nil {
		t.Fatal(err)
	}

	err = stackrecords.ChangePreviewGates(context.Background(), store, cipher, func(map[string]stackrecords.PreviewGateRecord) error { return nil })

	after, readErr := keyvalue.ReadOrEmpty(context.Background(), store, stackrecords.NewPreviewGatesRecordKey())
	if err != nil || readErr != nil || after.Revision != before.Revision {
		t.Errorf("ChangePreviewGates() = %v moved the record from revision %v to %v, want it left alone: a deploy that changes nothing must not contend for a record every project shares", err, before.Revision, after.Revision)
	}
}

func TestChangePreviewGatesWithNoRecordThatChangesNothingWritesNothing(t *testing.T) {
	t.Parallel()
	store, cipher := fake.NewKeyValues(), fake.NewCipher()

	err := stackrecords.ChangePreviewGates(context.Background(), store, cipher, func(map[string]stackrecords.PreviewGateRecord) error { return nil })

	recorded, readErr := keyvalue.ReadOrEmpty(context.Background(), store, stackrecords.NewPreviewGatesRecordKey())
	if err != nil || readErr != nil || len(recorded.Value) != 0 {
		t.Errorf("ChangePreviewGates() = %v and the record holds %q, want no record written: a sync of a project with no gate changes nothing", err, recorded.Value)
	}
}

func TestReadPreviewGateIsTheGateOfAnEnabledRecordAndNoneOfADisabledOrMissingOne(t *testing.T) {
	t.Parallel()
	store, cipher := fake.NewKeyValues(), fake.NewCipher()
	shop := previewgate.Gate{Key: "k", PasswordMAC: "mac", BypassSecrets: []string{}, AllowOptions: []string{}}
	recordGates(t, store, cipher, map[string]stackrecords.PreviewGateRecord{
		"shop": {Gate: shop, Password: "pw", Enabled: true},
		"blog": {Gate: previewgate.Gate{Key: "j"}, Password: "pw"},
	})

	for slug, want := range map[string]*previewgate.Gate{"shop": &shop, "blog": nil, "docs": nil} {
		got, err := stackrecords.ReadPreviewGate(context.Background(), store, cipher, slug)
		if err != nil || !reflect.DeepEqual(got, want) {
			t.Errorf("ReadPreviewGate(%s) = %+v, %v, want %+v", slug, got, err, want)
		}
	}
}

func TestTheRecordSealsThePasswordAndKeyAndNeverStoresThemInThePlain(t *testing.T) {
	t.Parallel()
	store, cipher := fake.NewKeyValues(), fake.NewCipher()

	err := stackrecords.ChangePreviewGates(context.Background(), store, cipher, func(records map[string]stackrecords.PreviewGateRecord) error {
		records["shop"] = stackrecords.PreviewGateRecord{Gate: previewgate.Gate{Key: "a-very-recognisable-key"}, Password: "a-very-recognisable-password"}
		return nil
	})

	recorded, readErr := keyvalue.ReadOrEmpty(context.Background(), store, stackrecords.NewPreviewGatesRecordKey())
	if err != nil || readErr != nil {
		t.Fatalf("ChangePreviewGates() = %v, %v", err, readErr)
	}
	for _, secret := range []string{"a-very-recognisable-key", "a-very-recognisable-password"} {
		if bytes.Contains(recorded.Value, []byte(secret)) {
			t.Errorf("the stored record contains %q in the plain", secret)
		}
	}
}

func TestAPreviewGateRecordNeverLetsItsPasswordReachAnEdgeCopy(t *testing.T) {
	t.Parallel()
	record := stackrecords.PreviewGateRecord{Gate: previewgate.Gate{Key: "k", PasswordMAC: "mac"}, Password: "the-plain-password"}

	copied, err := json.Marshal(record.Gate)

	if err != nil || bytes.Contains(copied, []byte("the-plain-password")) {
		t.Errorf("the edge copy of the gate is %s, %v, want only the MAC of the password", copied, err)
	}
}
