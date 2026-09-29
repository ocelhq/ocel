package ports_test

import (
	"context"
	"errors"
	"maps"
	"slices"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/envsource"
	"github.com/ocelhq/ocel/pkg/keyvalue"
	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/pkg/seal"
	"github.com/ocelhq/ocel/pkg/stackrecords"
	"github.com/ocelhq/ocel/pkg/variablestore"
	awsports "github.com/ocelhq/ocel/platform/aws/provider/ports"
)

const (
	fakeStateTable     = "ocel-state"
	fakeVariablesTable = "ocel-variables"
)

type splitTables struct{}

func (splitTables) Table(context.Context, environment.Tier) (string, error) {
	return fakeStateTable, nil
}

func (splitTables) ValuesTable(context.Context, environment.Tier) (string, error) {
	return fakeVariablesTable, nil
}

func newSplitRecords() (awsports.KeyValues, *fakeDynamo) {
	ddb := newFakeDynamo()
	return awsports.KeyValues{Dynamo: ddb, Tables: splitTables{}}, ddb
}

func TestASetValueOnlyEverTouchesTheVariablesTable(t *testing.T) {
	table, ddb := newSplitRecords()
	store := variablestore.Store{KeyValues: table, Cipher: newCipherIgnoringKMSCalls()}
	scope := variablestore.Scope{Project: "shop", Tier: environment.TierProduction}

	if _, err := store.Set(context.Background(), scope, variablestore.Coordinate{Cell: variablestore.Cell{Key: "STRIPE_API_KEY"}}, "sk_live_secret", nil); err != nil {
		t.Fatalf("Set err = %v", err)
	}

	if got := ddb.tablesUsed(); !slices.Equal(got, []string{fakeVariablesTable}) {
		t.Errorf("a set value reached tables %v, want %v alone", got, []string{fakeVariablesTable})
	}
}

func TestAnEnvSourceRegistrationItsSyncStatusAndItsDigestKeyLiveBesideTheValues(t *testing.T) {
	table, ddb := newSplitRecords()
	ctx := context.Background()
	registration := envsource.Registration{Project: "shop", Descriptor: envsource.Descriptor{Kind: envsource.Exec, Exec: &envsource.ExecOptions{Command: []string{"op"}}}, Folders: []string{""}}
	registration, err := envsource.Register(ctx, variablestore.Store{KeyValues: table, Cipher: newCipherIgnoringKMSCalls()}, environment.TierProduction, registration)
	if err != nil {
		t.Fatalf("Register err = %v", err)
	}
	listed, err := envsource.Registrations(ctx, table, environment.TierProduction)
	if err != nil || len(listed) != 1 || listed[0].Project != "shop" {
		t.Fatalf("Registrations = %+v, %v", listed, err)
	}
	sync := &envsource.Sync{Store: variablestore.Store{KeyValues: table, Cipher: newCipherIgnoringKMSCalls()}, Tier: environment.TierProduction}
	if _, err := sync.CopyProjectFrom(ctx, registration, envsource.NewFixed("exec", map[variablestore.Cell]envsource.Value{{Key: "K"}: {Plaintext: []byte("v")}})); err != nil {
		t.Fatalf("CopyProjectFrom err = %v", err)
	}

	if got := ddb.tablesUsed(); !slices.Equal(got, []string{fakeVariablesTable}) {
		t.Errorf("an env source reached tables %v, want %v alone: a scheduled sync's role reaches the variables table and nothing else", got, []string{fakeVariablesTable})
	}
}

func TestDeployStateStaysInTheStateTable(t *testing.T) {
	store, ddb := newSplitRecords()

	if _, err := store.Write(context.Background(), keyvalue.Entry{
		Key:   stackrecords.StackKey(environment.TierProduction, "shop", naming.InfraStack("shop")),
		Value: []byte("{}"),
	}); err != nil {
		t.Fatalf("Write err = %v", err)
	}

	if got := ddb.tablesUsed(); !slices.Equal(got, []string{fakeStateTable}) {
		t.Errorf("a stack record reached tables %v, want %v alone", got, []string{fakeStateTable})
	}
}

type keylessBootstrap struct{}

func (keylessBootstrap) Key(context.Context, environment.Tier) (string, error) { return "", nil }

var bound = seal.AssociatedData{{Name: "project", Value: "shop"}, {Name: "key", Value: "STRIPE_API_KEY"}}

func TestSealingWithoutAKeyNamesTheFeature(t *testing.T) {
	cipher := awsports.Cipher{Keys: keylessBootstrap{}}
	_, err := cipher.Seal(context.Background(), environment.TierProduction, bound, []byte("sk_live_secret"))
	if err == nil {
		t.Fatal("Seal = nil, want a refusal when this bootstrap made no key")
	}
	var refused refusal.Refusal
	if !errors.As(err, &refused) || refused.Code != refusal.CodeNotReady {
		t.Fatalf("Seal err = %v, want a CodeNotReady refusal", err)
	}
	if want := "ocel bootstrap production --features variables-key"; !strings.Contains(err.Error(), want) {
		t.Errorf("Seal err = %v, want it to name `%s`", err, want)
	}
}

func TestSealingBeforeAnyKeyIsWiredRefusesRatherThanPanics(t *testing.T) {
	cipher := awsports.Cipher{}
	_, err := cipher.Seal(context.Background(), environment.TierProduction, bound, []byte("sk_live_secret"))
	if err == nil {
		t.Fatal("Seal = nil, want a refusal where nothing has a key at all")
	}
	var refused refusal.Refusal
	if !errors.As(err, &refused) || refused.Code != refusal.CodeNotReady {
		t.Fatalf("Seal err = %v, want a CodeNotReady refusal", err)
	}
	if want := "ocel bootstrap production --features variables-key"; !strings.Contains(err.Error(), want) {
		t.Errorf("Seal err = %v, want it to name `%s`", err, want)
	}
}

func TestTheDigestKeyIsSealedUnderAnEncryptionContextNamingEveryProjectItsClassAndTheEnvSourceBinding(t *testing.T) {
	table, _ := newSplitRecords()
	cipher, crypto := newCipher()

	if _, err := envsource.EnsureDigestKey(context.Background(), variablestore.Store{KeyValues: table, Cipher: cipher}, environment.TierProduction); err != nil {
		t.Fatalf("EnsureDigestKey err = %v", err)
	}

	want := map[string]string{
		"project":     "*",
		"class":       "production",
		"environment": "*",
		"folder":      "/",
		"binding":     "envsource",
		"key":         "digestkey",
	}
	if len(crypto.contexts) == 0 || !maps.Equal(crypto.contexts[0], want) {
		t.Fatalf("encryption contexts = %v, want the digest key sealed under %v: every digest key already stored is bound to it", crypto.contexts, want)
	}
}
