package ports_test

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	kit "github.com/ocelhq/ocel/pkg/providerkit/ports"
	"github.com/ocelhq/ocel/pkg/providerkit/values"
	awsports "github.com/ocelhq/ocel/platform/aws/provider/ports"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

const (
	fakeStateTable = "ocel-state"
	fakeVarsTable  = "ocel-vars"
)

type splitTables struct{}

func (splitTables) Table(context.Context, kit.Class) (string, error) { return fakeStateTable, nil }

func (splitTables) ValuesTable(context.Context, kit.Class) (string, error) {
	return fakeVarsTable, nil
}

func newSplitRecords() (awsports.Records, *fakeDynamo) {
	ddb := newFakeDynamo()
	return awsports.Records{Dynamo: ddb, Tables: splitTables{}}, ddb
}

func TestASetValueOnlyEverTouchesTheVarsTable(t *testing.T) {
	records, ddb := newSplitRecords()
	store := values.Store{Records: records, Sealer: mustSealer()}
	scope := values.Scope{Project: "shop", Class: edge.ClassProduction}

	if _, err := store.Set(context.Background(), scope, values.Coordinate{Cell: values.Cell{Key: "STRIPE_API_KEY"}}, "sk_live_secret", nil); err != nil {
		t.Fatalf("Set err = %v", err)
	}

	if got := ddb.tablesUsed(); !slices.Equal(got, []string{fakeVarsTable}) {
		t.Errorf("a set value reached tables %v, want %v alone", got, []string{fakeVarsTable})
	}
}

func TestDeployStateStaysInTheStateTable(t *testing.T) {
	records, ddb := newSplitRecords()

	if _, err := records.Write(context.Background(), kit.Record{
		Name:  kit.RecordName{kit.RootStacks, string(edge.ClassProduction), "shop"},
		Bytes: []byte("{}"),
	}); err != nil {
		t.Fatalf("Write err = %v", err)
	}

	if got := ddb.tablesUsed(); !slices.Equal(got, []string{fakeStateTable}) {
		t.Errorf("a stack record reached tables %v, want %v alone", got, []string{fakeStateTable})
	}
}

type keylessBootstrap struct{}

func (keylessBootstrap) Key(context.Context, kit.Class) (string, error) { return "", nil }

func TestSealingWithoutAKeyNamesTheFeature(t *testing.T) {
	sealer := awsports.Sealer{Keys: keylessBootstrap{}}
	at := kit.Coordinate{Project: "shop", Class: edge.ClassProduction, Env: "*", Folder: "/", Name: "STRIPE_API_KEY"}

	_, err := sealer.Seal(context.Background(), at, []byte("sk_live_secret"))
	if err == nil {
		t.Fatal("Seal = nil, want a refusal when this bootstrap made no key")
	}
	var refusal kit.Refusal
	if !errors.As(err, &refusal) || refusal.Code != kit.CodeNotReady {
		t.Fatalf("Seal err = %v, want a CodeNotReady refusal", err)
	}
	if want := "ocel bootstrap production --features vars-key"; !strings.Contains(err.Error(), want) {
		t.Errorf("Seal err = %v, want it to name `%s`", err, want)
	}
}

func TestSealingBeforeAnyKeyIsWiredRefusesRatherThanPanics(t *testing.T) {
	sealer := awsports.Sealer{}
	at := kit.Coordinate{Project: "shop", Class: edge.ClassProduction, Env: "*", Folder: "/", Name: "STRIPE_API_KEY"}

	_, err := sealer.Seal(context.Background(), at, []byte("sk_live_secret"))
	if err == nil {
		t.Fatal("Seal = nil, want a refusal where nothing holds a key at all")
	}
	var refusal kit.Refusal
	if !errors.As(err, &refusal) || refusal.Code != kit.CodeNotReady {
		t.Fatalf("Seal err = %v, want a CodeNotReady refusal", err)
	}
	if want := "ocel bootstrap production --features vars-key"; !strings.Contains(err.Error(), want) {
		t.Errorf("Seal err = %v, want it to name `%s`", err, want)
	}
}
