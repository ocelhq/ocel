package ports_test

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/providerkit/envvars"
	"github.com/ocelhq/ocel/pkg/providerkit/records"
	"github.com/ocelhq/ocel/pkg/providerkit/refusal"
	awsports "github.com/ocelhq/ocel/platform/aws/provider/ports"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

const (
	fakeStateTable = "ocel-state"
	fakeVarsTable  = "ocel-vars"
)

type splitTables struct{}

func (splitTables) Table(context.Context, edge.Class) (string, error) { return fakeStateTable, nil }

func (splitTables) ValuesTable(context.Context, edge.Class) (string, error) {
	return fakeVarsTable, nil
}

func newSplitRecords() (awsports.Records, *fakeDynamo) {
	ddb := newFakeDynamo()
	return awsports.Records{Dynamo: ddb, Tables: splitTables{}}, ddb
}

func TestASetValueOnlyEverTouchesTheVarsTable(t *testing.T) {
	table, ddb := newSplitRecords()
	store := envvars.Store{Records: table, Cipher: mustSealer()}
	scope := envvars.Scope{Project: "shop", Class: edge.ClassProduction}

	if _, err := store.Set(context.Background(), scope, envvars.Coordinate{Cell: envvars.Cell{Key: "STRIPE_API_KEY"}}, "sk_live_secret", nil); err != nil {
		t.Fatalf("Set err = %v", err)
	}

	if got := ddb.tablesUsed(); !slices.Equal(got, []string{fakeVarsTable}) {
		t.Errorf("a set value reached tables %v, want %v alone", got, []string{fakeVarsTable})
	}
}

func TestDeployStateStaysInTheStateTable(t *testing.T) {
	store, ddb := newSplitRecords()

	if _, err := store.Write(context.Background(), records.Record{
		Name:  records.Name{records.RootStacks, string(edge.ClassProduction), "shop"},
		Bytes: []byte("{}"),
	}); err != nil {
		t.Fatalf("Write err = %v", err)
	}

	if got := ddb.tablesUsed(); !slices.Equal(got, []string{fakeStateTable}) {
		t.Errorf("a stack record reached tables %v, want %v alone", got, []string{fakeStateTable})
	}
}

type keylessBootstrap struct{}

func (keylessBootstrap) Key(context.Context, edge.Class) (string, error) { return "", nil }

func TestSealingWithoutAKeyNamesTheFeature(t *testing.T) {
	sealer := awsports.Cipher{Keys: keylessBootstrap{}}
	at := records.SealScope{Project: "shop", Class: edge.ClassProduction, Env: "*", Folder: "/", Name: "STRIPE_API_KEY"}

	_, err := sealer.Seal(context.Background(), at, []byte("sk_live_secret"))
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
	sealer := awsports.Cipher{}
	at := records.SealScope{Project: "shop", Class: edge.ClassProduction, Env: "*", Folder: "/", Name: "STRIPE_API_KEY"}

	_, err := sealer.Seal(context.Background(), at, []byte("sk_live_secret"))
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
