package stackrecords_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"

	"github.com/ocelhq/ocel/pkg/edge/gate"
	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/keyvalue"
	"github.com/ocelhq/ocel/pkg/provider/fake"
	"github.com/ocelhq/ocel/pkg/stackrecords"
)

func recordGates(t *testing.T, store keyvalue.Store, cipher *fake.Cipher, records map[string]stackrecords.GateRecord) {
	t.Helper()
	plaintext, err := json.Marshal(records)
	if err != nil {
		t.Fatal(err)
	}
	sealed, err := cipher.Seal(context.Background(), environment.TierPreview, stackrecords.GatesAssociatedData, plaintext)
	if err != nil {
		t.Fatal(err)
	}
	value, err := json.Marshal(sealed)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Write(context.Background(), keyvalue.Entry{Key: stackrecords.NewGatesRecordKey(environment.TierPreview), Value: value}); err != nil {
		t.Fatal(err)
	}
}

func TestReadGatesOpensTheSealedRecordIntoAMapByProjectSlug(t *testing.T) {
	t.Parallel()
	store, cipher := fake.NewKeyValues(), fake.NewCipher()
	want := map[string]stackrecords.GateRecord{
		"shop": {Gate: gate.Gate{Key: "k", PasswordMAC: "mac", BypassSecrets: []string{"s"}, AllowOptions: []string{"/api"}}, Password: "correct horse battery"},
		"blog": {Gate: gate.Gate{Key: "j", BypassSecrets: []string{}, AllowOptions: []string{}}, Pending: true},
	}
	recordGates(t, store, cipher, want)

	got, err := stackrecords.ReadGates(context.Background(), store, cipher, environment.TierPreview)

	if err != nil || !reflect.DeepEqual(got, want) {
		t.Errorf("ReadGates() = %+v, %v, want %+v", got, err, want)
	}
}

func TestReadGatesOfAProviderWithNoRecordIsNoGates(t *testing.T) {
	t.Parallel()

	got, err := stackrecords.ReadGates(context.Background(), fake.NewKeyValues(), fake.NewCipher(), environment.TierPreview)

	if err != nil || len(got) != 0 {
		t.Errorf("ReadGates() = %+v, %v, want none", got, err)
	}
}

func TestReadGatesRefusesARecordSealedUnderAnotherTierOrContext(t *testing.T) {
	t.Parallel()
	store, cipher := fake.NewKeyValues(), fake.NewCipher()
	sealed, err := cipher.Seal(context.Background(), environment.TierPreview, nil, []byte(`{"shop":{"gate":{"key":"k"}}}`))
	if err != nil {
		t.Fatal(err)
	}
	value, _ := json.Marshal(sealed)
	if _, err := store.Write(context.Background(), keyvalue.Entry{Key: stackrecords.NewGatesRecordKey(environment.TierPreview), Value: value}); err != nil {
		t.Fatal(err)
	}

	if _, err := stackrecords.ReadGates(context.Background(), store, cipher, environment.TierPreview); err == nil {
		t.Error("ReadGates() opened a record sealed without its association, want it refused")
	}
}

func TestChangeGatesRecordsWhatReadGatesOpens(t *testing.T) {
	t.Parallel()
	store, cipher := fake.NewKeyValues(), fake.NewCipher()
	blog := stackrecords.GateRecord{Gate: gate.Gate{Key: "j", BypassSecrets: []string{}, AllowOptions: []string{}}}
	recordGates(t, store, cipher, map[string]stackrecords.GateRecord{"blog": blog})
	shop := stackrecords.GateRecord{Gate: gate.Gate{Key: "k", PasswordMAC: "mac", BypassSecrets: []string{"s"}, AllowOptions: []string{}}, Password: "pw"}

	err := stackrecords.ChangeGates(context.Background(), store, cipher, environment.TierPreview, func(records map[string]stackrecords.GateRecord) error {
		records["shop"] = shop
		return nil
	})

	got, readErr := stackrecords.ReadGates(context.Background(), store, cipher, environment.TierPreview)
	want := map[string]stackrecords.GateRecord{"blog": blog, "shop": shop}
	if err != nil || readErr != nil || !reflect.DeepEqual(got, want) {
		t.Errorf("ReadGates() after ChangeGates() = %+v, %v (change error %v), want %+v", got, readErr, err, want)
	}
}

func TestChangeGatesThatRefusesLeavesTheRecordUnwritten(t *testing.T) {
	t.Parallel()
	store, cipher := fake.NewKeyValues(), fake.NewCipher()

	err := stackrecords.ChangeGates(context.Background(), store, cipher, environment.TierPreview, func(records map[string]stackrecords.GateRecord) error {
		records["shop"] = stackrecords.GateRecord{Gate: gate.Gate{Key: "k"}}
		return errors.New("refused")
	})

	recorded, readErr := keyvalue.ReadOrEmpty(context.Background(), store, stackrecords.NewGatesRecordKey(environment.TierPreview))
	if err == nil || readErr != nil || len(recorded.Value) != 0 {
		t.Errorf("ChangeGates() = %v and the record holds %q, want the refusal and no record", err, recorded.Value)
	}
}

func TestChangeGatesThatChangesNothingWritesNothing(t *testing.T) {
	t.Parallel()
	store, cipher := fake.NewKeyValues(), fake.NewCipher()
	recordGates(t, store, cipher, map[string]stackrecords.GateRecord{"shop": {Gate: gate.Gate{Key: "k", BypassSecrets: []string{}, AllowOptions: []string{}}, Password: "pw"}})
	before, err := keyvalue.ReadOrEmpty(context.Background(), store, stackrecords.NewGatesRecordKey(environment.TierPreview))
	if err != nil {
		t.Fatal(err)
	}

	err = stackrecords.ChangeGates(context.Background(), store, cipher, environment.TierPreview, func(map[string]stackrecords.GateRecord) error { return nil })

	after, readErr := keyvalue.ReadOrEmpty(context.Background(), store, stackrecords.NewGatesRecordKey(environment.TierPreview))
	if err != nil || readErr != nil || after.Revision != before.Revision {
		t.Errorf("ChangeGates() = %v moved the record from revision %v to %v, want it left alone: a deploy that changes nothing must not contend for a record every project shares", err, before.Revision, after.Revision)
	}
}

func TestChangeGatesWithNoRecordThatChangesNothingWritesNothing(t *testing.T) {
	t.Parallel()
	store, cipher := fake.NewKeyValues(), fake.NewCipher()

	err := stackrecords.ChangeGates(context.Background(), store, cipher, environment.TierPreview, func(map[string]stackrecords.GateRecord) error { return nil })

	recorded, readErr := keyvalue.ReadOrEmpty(context.Background(), store, stackrecords.NewGatesRecordKey(environment.TierPreview))
	if err != nil || readErr != nil || len(recorded.Value) != 0 {
		t.Errorf("ChangeGates() = %v and the record holds %q, want no record written: a sync of a project with no gate changes nothing", err, recorded.Value)
	}
}

func TestReadGateIsTheGateOfAnEnabledRecordAndNoneOfADisabledOrMissingOne(t *testing.T) {
	t.Parallel()
	store, cipher := fake.NewKeyValues(), fake.NewCipher()
	shop := gate.Gate{Key: "k", PasswordMAC: "mac", BypassSecrets: []string{}, AllowOptions: []string{}}
	recordGates(t, store, cipher, map[string]stackrecords.GateRecord{
		"shop": {Gate: shop, Password: "pw", Enabled: true},
		"blog": {Gate: gate.Gate{Key: "j"}, Password: "pw"},
	})

	for slug, want := range map[string]*gate.Gate{"shop": &shop, "blog": nil, "docs": nil} {
		got, err := stackrecords.ReadGate(context.Background(), store, cipher, environment.TierPreview, slug)
		if err != nil || !reflect.DeepEqual(got, want) {
			t.Errorf("ReadGate(%s) = %+v, %v, want %+v", slug, got, err, want)
		}
	}
}

func TestTheRecordSealsThePasswordAndKeyAndNeverStoresThemInThePlain(t *testing.T) {
	t.Parallel()
	store, cipher := fake.NewKeyValues(), fake.NewCipher()

	err := stackrecords.ChangeGates(context.Background(), store, cipher, environment.TierPreview, func(records map[string]stackrecords.GateRecord) error {
		records["shop"] = stackrecords.GateRecord{Gate: gate.Gate{Key: "a-very-recognisable-key"}, Password: "a-very-recognisable-password"}
		return nil
	})

	recorded, readErr := keyvalue.ReadOrEmpty(context.Background(), store, stackrecords.NewGatesRecordKey(environment.TierPreview))
	if err != nil || readErr != nil {
		t.Fatalf("ChangeGates() = %v, %v", err, readErr)
	}
	for _, secret := range []string{"a-very-recognisable-key", "a-very-recognisable-password"} {
		if bytes.Contains(recorded.Value, []byte(secret)) {
			t.Errorf("the stored record contains %q in the plain", secret)
		}
	}
}

func TestAGateRecordNeverLetsItsPasswordReachAnEdgeCopy(t *testing.T) {
	t.Parallel()
	record := stackrecords.GateRecord{Gate: gate.Gate{Key: "k", PasswordMAC: "mac"}, Password: "the-plain-password"}

	copied, err := json.Marshal(record.Gate)

	if err != nil || bytes.Contains(copied, []byte("the-plain-password")) {
		t.Errorf("the edge copy of the gate is %s, %v, want only the MAC of the password", copied, err)
	}
}

func TestAGateRecordedForOneTierIsNeverReadForTheOther(t *testing.T) {
	t.Parallel()
	store, cipher := fake.NewKeyValues(), fake.NewCipher()
	preview := stackrecords.GateRecord{Gate: gate.Gate{Key: "preview-key", PasswordMAC: "preview-mac", BypassSecrets: []string{}, AllowOptions: []string{}}, Password: "preview-pw", Enabled: true}
	err := stackrecords.ChangeGates(context.Background(), store, cipher, environment.TierPreview, func(records map[string]stackrecords.GateRecord) error {
		records["shop"] = preview
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	production, err := stackrecords.ReadGates(context.Background(), store, cipher, environment.TierProduction)
	held, heldErr := stackrecords.ReadGate(context.Background(), store, cipher, environment.TierProduction, "shop")

	if err != nil || heldErr != nil || len(production) != 0 || held != nil {
		t.Errorf("production reads %+v and gate %+v (%v, %v), want none: a preview password must never open production", production, held, err, heldErr)
	}
}

func TestAProductionGateRecordCannotBeOpenedAsAPreviewOne(t *testing.T) {
	t.Parallel()
	store, cipher := fake.NewKeyValues(), fake.NewCipher()
	err := stackrecords.ChangeGates(context.Background(), store, cipher, environment.TierProduction, func(records map[string]stackrecords.GateRecord) error {
		records["shop"] = stackrecords.GateRecord{Gate: gate.Gate{Key: "k"}, Password: "pw"}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	sealed, err := keyvalue.ReadOrEmpty(context.Background(), store, stackrecords.NewGatesRecordKey(environment.TierProduction))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Write(context.Background(), keyvalue.Entry{Key: stackrecords.NewGatesRecordKey(environment.TierPreview), Value: sealed.Value}); err != nil {
		t.Fatal(err)
	}

	if _, err := stackrecords.ReadGates(context.Background(), store, cipher, environment.TierPreview); err == nil {
		t.Error("ReadGates opened a record sealed for production as a preview one, want it refused")
	}
}
