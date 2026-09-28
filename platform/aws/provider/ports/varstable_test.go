package ports_test

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/envsource"
	"github.com/ocelhq/ocel/pkg/envvars"
	"github.com/ocelhq/ocel/pkg/records"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/pkg/seal"
	awsports "github.com/ocelhq/ocel/platform/aws/provider/ports"
)

const (
	fakeStateTable = "ocel-state"
	fakeVarsTable  = "ocel-vars"
)

type splitTables struct{}

func (splitTables) Table(context.Context, environment.Tier) (string, error) {
	return fakeStateTable, nil
}

func (splitTables) ValuesTable(context.Context, environment.Tier) (string, error) {
	return fakeVarsTable, nil
}

func newSplitRecords() (awsports.Records, *fakeDynamo) {
	ddb := newFakeDynamo()
	return awsports.Records{Dynamo: ddb, Tables: splitTables{}}, ddb
}

func TestASetValueOnlyEverTouchesTheVarsTable(t *testing.T) {
	table, ddb := newSplitRecords()
	store := envvars.Store{Records: table, Cipher: newCipherIgnoringKMSCalls()}
	scope := envvars.Scope{Project: "shop", Tier: environment.TierProduction}

	if _, err := store.Set(context.Background(), scope, envvars.Coordinate{Cell: envvars.Cell{Key: "STRIPE_API_KEY"}}, "sk_live_secret", nil); err != nil {
		t.Fatalf("Set err = %v", err)
	}

	if got := ddb.tablesUsed(); !slices.Equal(got, []string{fakeVarsTable}) {
		t.Errorf("a set value reached tables %v, want %v alone", got, []string{fakeVarsTable})
	}
}

func TestAnEnvSourceRegistrationItsSyncStatusAndItsDigestKeyLiveBesideTheValues(t *testing.T) {
	table, ddb := newSplitRecords()
	ctx := context.Background()
	registration := envsource.Registration{Project: "shop", Descriptor: envsource.Descriptor{Kind: envsource.Exec, Exec: &envsource.ExecOptions{Command: []string{"op"}}}, Folders: []string{""}}
	registration, err := envsource.Register(ctx, envvars.Store{Records: table, Cipher: newCipherIgnoringKMSCalls()}, environment.TierProduction, registration)
	if err != nil {
		t.Fatalf("Register err = %v", err)
	}
	listed, err := envsource.Registrations(ctx, table, environment.TierProduction)
	if err != nil || len(listed) != 1 || listed[0].Project != "shop" {
		t.Fatalf("Registrations = %+v, %v", listed, err)
	}
	sync := &envsource.Sync{Store: envvars.Store{Records: table, Cipher: newCipherIgnoringKMSCalls()}, Tier: environment.TierProduction}
	if _, err := sync.CopyProjectFrom(ctx, registration, envsource.NewFixed("exec", map[envvars.Cell]envsource.Value{{Key: "K"}: {Plaintext: []byte("v")}})); err != nil {
		t.Fatalf("CopyProjectFrom err = %v", err)
	}

	if got := ddb.tablesUsed(); !slices.Equal(got, []string{fakeVarsTable}) {
		t.Errorf("an env source reached tables %v, want %v alone: a scheduled sync's role reaches the vars table and nothing else", got, []string{fakeVarsTable})
	}
}

func TestDeployStateStaysInTheStateTable(t *testing.T) {
	store, ddb := newSplitRecords()

	if _, err := store.Write(context.Background(), records.Record{
		Name:  records.Name{records.RootStacks, string(environment.TierProduction), "shop"},
		Bytes: []byte("{}"),
	}); err != nil {
		t.Fatalf("Write err = %v", err)
	}

	if got := ddb.tablesUsed(); !slices.Equal(got, []string{fakeStateTable}) {
		t.Errorf("a stack record reached tables %v, want %v alone", got, []string{fakeStateTable})
	}
}

type keylessBootstrap struct{}

func (keylessBootstrap) Key(context.Context, environment.Tier) (string, error) { return "", nil }

var sealedAt = seal.AssociatedData{{Name: "project", Value: "shop"}, {Name: "key", Value: "STRIPE_API_KEY"}}

func TestSealingWithoutAKeyNamesTheFeature(t *testing.T) {
	cipher := awsports.Cipher{Keys: keylessBootstrap{}}
	_, err := cipher.Seal(context.Background(), environment.TierProduction, sealedAt, []byte("sk_live_secret"))
	if err == nil {
		t.Fatal("Seal = nil, want a refusal when this bootstrap made no key")
	}
	var refused refusal.Refusal
	if !errors.As(err, &refused) || refused.Code != refusal.CodeNotReady {
		t.Fatalf("Seal err = %v, want a CodeNotReady refusal", err)
	}
	if want := "ocel bootstrap production --features vars-key"; !strings.Contains(err.Error(), want) {
		t.Errorf("Seal err = %v, want it to name `%s`", err, want)
	}
}

func TestSealingBeforeAnyKeyIsWiredRefusesRatherThanPanics(t *testing.T) {
	cipher := awsports.Cipher{}
	_, err := cipher.Seal(context.Background(), environment.TierProduction, sealedAt, []byte("sk_live_secret"))
	if err == nil {
		t.Fatal("Seal = nil, want a refusal where nothing has a key at all")
	}
	var refused refusal.Refusal
	if !errors.As(err, &refused) || refused.Code != refusal.CodeNotReady {
		t.Fatalf("Seal err = %v, want a CodeNotReady refusal", err)
	}
	if want := "ocel bootstrap production --features vars-key"; !strings.Contains(err.Error(), want) {
		t.Errorf("Seal err = %v, want it to name `%s`", err, want)
	}
}
