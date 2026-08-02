package server

import (
	"context"
	"errors"
	"maps"
	"net/http/httptest"
	"slices"
	"testing"

	connect "connectrpc.com/connect"

	"github.com/ocelhq/ocel/cloud/aws/bootstrap"
	"github.com/ocelhq/ocel/cloud/aws/vars"
	deploymentsv1 "github.com/ocelhq/ocel/pkg/proto/deployments/v1"
	envv1 "github.com/ocelhq/ocel/pkg/proto/env/v1"
	"github.com/ocelhq/ocel/pkg/proto/env/v1/envv1connect"
)

func newTestVarsClient(t *testing.T, token string) envv1connect.EnvVarsServiceClient {
	t.Helper()
	srv := httptest.NewServer(NewMux(testToken))
	t.Cleanup(srv.Close)

	var opts []connect.ClientOption
	if token != "" {
		opts = append(opts, connect.WithInterceptors(authHeaderInterceptor{token: token}))
	}
	return envv1connect.NewEnvVarsServiceClient(srv.Client(), srv.URL, opts...)
}

// The vars service must be served on the provider's own channel, behind the
// same session-token handshake: it reaches values in the user's cloud account,
// so an unauthenticated caller must not get past the interceptor to the store.
func TestListValues_RejectsMissingToken(t *testing.T) {
	_, err := newTestVarsClient(t, "").ListValues(context.Background(), &envv1.ListValuesRequest{Slug: "shop"})

	var connectErr *connect.Error
	if !errors.As(err, &connectErr) || connectErr.Code() != connect.CodeUnauthenticated {
		t.Fatalf("ListValues with no token err = %v, want CodeUnauthenticated", err)
	}
}

func TestListValues_RejectsWrongToken(t *testing.T) {
	_, err := newTestVarsClient(t, "wrong-token").ListValues(context.Background(), &envv1.ListValuesRequest{Slug: "shop"})

	var connectErr *connect.Error
	if !errors.As(err, &connectErr) || connectErr.Code() != connect.CodeUnauthenticated {
		t.Fatalf("ListValues with wrong token err = %v, want CodeUnauthenticated", err)
	}
}

// A lost race has to be distinguishable from a broken request by code, so the
// CLI can tell the operator to re-read and retry without matching on text.
func TestVarsError_ClassifiesStoreFailures(t *testing.T) {
	for _, tc := range []struct {
		err  error
		want connect.Code
	}{
		{vars.ErrStaleVersion, connect.CodeFailedPrecondition},
		{vars.ErrNotFound, connect.CodeNotFound},
	} {
		var connectErr *connect.Error
		if !errors.As(varsError(tc.err), &connectErr) || connectErr.Code() != tc.want {
			t.Errorf("varsError(%v) = %v, want %v", tc.err, varsError(tc.err), tc.want)
		}
	}

	other := errors.New("the table is on fire")
	var connectErr *connect.Error
	if errors.As(varsError(other), &connectErr) {
		t.Errorf("varsError(%v) = %v, want it left unclassified", other, varsError(other))
	}
}

// A deploy reads many values through one provider session, and every read used
// to re-derive the table from the account's bootstrap stack. The coordinates do
// not change while the provider is running, so the whole session must cost one
// describe however many values it reads.
func TestVarsServer_FindsTheStoreOncePerSession(t *testing.T) {
	cfn, ddb, crypto := &countingCFN{}, newFakeDynamo(), &fakeKMS{}
	s := testAccount(cfn, ddb, crypto)
	ctx := context.Background()

	for i := 0; i < 3; i++ {
		if _, err := s.ListValues(ctx, &envv1.ListValuesRequest{Slug: "shop"}); err != nil {
			t.Fatalf("ListValues: %v", err)
		}
		if _, err := s.GetValue(ctx, &envv1.GetValueRequest{
			Coordinate: &envv1.Coordinate{Slug: "shop", Key: "STRIPE_API_KEY"},
			Reveal:     true,
		}); err != nil {
			t.Fatalf("GetValue: %v", err)
		}
	}

	if got := cfn.count(); got != 1 {
		t.Errorf("six reads cost %d bootstrap describes, want 1", got)
	}
}

// The two substrates keep separate tables and separate keys, so reusing one
// store for both would answer a preview read out of production's values. Each
// has to be found on its own.
func TestVarsServer_FindsEachSubstratesOwnStore(t *testing.T) {
	cfn := &countingCFN{}
	s := testAccount(cfn, newFakeDynamo(), &fakeKMS{})
	ctx := context.Background()

	for _, class := range []deploymentsv1.Environment_Class{
		deploymentsv1.Environment_CLASS_PRODUCTION,
		deploymentsv1.Environment_CLASS_PREVIEW,
		deploymentsv1.Environment_CLASS_PRODUCTION,
		deploymentsv1.Environment_CLASS_PREVIEW,
	} {
		if _, err := s.ListValues(ctx, &envv1.ListValuesRequest{Slug: "shop", Class: class}); err != nil {
			t.Fatalf("ListValues(%v): %v", class, err)
		}
	}

	if got := cfn.count(); got != 2 {
		t.Errorf("reads across both substrates cost %d bootstrap describes, want 2 (one each)", got)
	}
	want := []string{bootstrap.StackName, bootstrap.PreviewStackName}
	if !slices.Equal(cfn.stacks, want) {
		t.Errorf("described %v, want %v", cfn.stacks, want)
	}
}

// A deploy reads a project's plaintext a whole set at a time, and reading it
// one cell per RPC is what made a gate cost a round trip per variable. The
// batch has to cost one query however many cells it names — and a cell that
// holds nothing has to come back absent, not empty.
func TestRevealValues_ReadsEveryNamedCellInOneQuery(t *testing.T) {
	cfn, ddb, crypto := &countingCFN{}, newFakeDynamo(), &fakeKMS{}
	s := testAccount(cfn, ddb, crypto)
	ctx := context.Background()

	want := map[string]string{"STRIPE_API_KEY": "sk-live", "WEBHOOK_URL": "https://hooks.example", "SESSION_KEY": "sess"}
	for key, value := range want {
		if _, err := s.SetValue(ctx, &envv1.SetValueRequest{
			Coordinate: &envv1.Coordinate{Slug: "shop", Key: key},
			Value:      value,
		}); err != nil {
			t.Fatalf("SetValue %s: %v", key, err)
		}
	}

	queriesBefore, _ := ddb.counts()
	decryptsBefore := crypto.count()

	resp, err := s.RevealValues(ctx, &envv1.RevealValuesRequest{
		Slug: "shop",
		Cells: []*envv1.Cell{
			{Key: "STRIPE_API_KEY"},
			{Key: "WEBHOOK_URL"},
			{Key: "SESSION_KEY"},
			{Key: "NEVER_SET"},
		},
	})
	if err != nil {
		t.Fatalf("RevealValues: %v", err)
	}

	got := map[string]string{}
	for _, value := range resp.GetValues() {
		got[value.GetMetadata().GetCoordinate().GetKey()] = value.GetValue()
	}
	if !maps.Equal(got, want) {
		t.Errorf("RevealValues gave %v, want %v (an unset cell is absent, not empty)", got, want)
	}

	queriesAfter, _ := ddb.counts()
	if queries := queriesAfter - queriesBefore; queries != 1 {
		t.Errorf("revealing 4 cells cost %d queries, want 1", queries)
	}
	if decrypts := crypto.count() - decryptsBefore; decrypts != len(want) {
		t.Errorf("revealing 4 cells cost %d decrypts, want %d (KMS has no batch decrypt, and an unset cell has nothing to decrypt)", decrypts, len(want))
	}
}

func atVersion(n int64) *int64 { return &n }

// guardedCell is one cell reached through the provider, optionally already
// holding a value at version 1, so a write's expectation has something to be
// checked against.
func guardedCell(t *testing.T, seeded bool) (*VarsServer, *envv1.Coordinate) {
	t.Helper()
	s := testAccount(&countingCFN{}, newFakeDynamo(), &fakeKMS{})
	coordinate := &envv1.Coordinate{Slug: "shop", Key: "STRIPE_API_KEY"}
	if seeded {
		if _, err := s.SetValue(context.Background(), &envv1.SetValueRequest{
			Coordinate: coordinate,
			Value:      "sk-seed",
		}); err != nil {
			t.Fatalf("seeding the cell: %v", err)
		}
	}
	return s, coordinate
}

func wantsFailedPrecondition(t *testing.T, err error) {
	t.Helper()
	var connectErr *connect.Error
	if !errors.As(err, &connectErr) || connectErr.Code() != connect.CodeFailedPrecondition {
		t.Fatalf("err = %v, want CodeFailedPrecondition (the guard the caller asked for)", err)
	}
}

// The version the caller believes is current has to reach the store unchanged,
// because each of its three states means something different: absent writes
// blind, zero demands that no live value is set, and N demands that exact live
// version. Dropping the field here would turn every guarded write the UI makes
// into a blind one that silently overwrites a concurrent edit.
func TestSetValue_ForwardsTheCallersExpectedVersion(t *testing.T) {
	for _, tc := range []struct {
		name        string
		seeded      bool
		expected    *int64
		wantStale   bool
		wantVersion int64
	}{
		{name: "absent writes blind over a live value", seeded: true, wantVersion: 2},
		{name: "absent writes blind into an unset cell", seeded: false, wantVersion: 1},
		{name: "zero loses against a live value", seeded: true, expected: atVersion(0), wantStale: true},
		{name: "zero writes into an unset cell", seeded: false, expected: atVersion(0), wantVersion: 1},
		{name: "the live version writes", seeded: true, expected: atVersion(1), wantVersion: 2},
		{name: "that same version loses against an unset cell", seeded: false, expected: atVersion(1), wantStale: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, coordinate := guardedCell(t, tc.seeded)

			resp, err := s.SetValue(context.Background(), &envv1.SetValueRequest{
				Coordinate:      coordinate,
				Value:           "sk-live",
				ExpectedVersion: tc.expected,
			})

			if tc.wantStale {
				wantsFailedPrecondition(t, err)
				return
			}
			if err != nil {
				t.Fatalf("SetValue: %v", err)
			}
			if got := resp.GetMetadata().GetVersion(); got != tc.wantVersion {
				t.Errorf("wrote version %d, want %d", got, tc.wantVersion)
			}
		})
	}
}

// A delete is a write of a tombstone, so it carries the same expectation and
// must forward it the same way — and zero keeps a landed delete idempotent
// rather than turning a repeat into a lost race.
func TestDeleteValue_ForwardsTheCallersExpectedVersion(t *testing.T) {
	for _, tc := range []struct {
		name        string
		seeded      bool
		expected    *int64
		wantStale   bool
		wantDeleted bool
	}{
		{name: "absent deletes blind", seeded: true, wantDeleted: true},
		{name: "absent against an unset cell deletes nothing", seeded: false},
		{name: "zero loses against a live value", seeded: true, expected: atVersion(0), wantStale: true},
		{name: "zero on an unset cell is a landed delete repeated", seeded: false, expected: atVersion(0)},
		{name: "the live version deletes", seeded: true, expected: atVersion(1), wantDeleted: true},
		{name: "that same version loses against an unset cell", seeded: false, expected: atVersion(1), wantStale: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, coordinate := guardedCell(t, tc.seeded)

			resp, err := s.DeleteValue(context.Background(), &envv1.DeleteValueRequest{
				Coordinate:      coordinate,
				ExpectedVersion: tc.expected,
			})

			if tc.wantStale {
				wantsFailedPrecondition(t, err)
				return
			}
			if err != nil {
				t.Fatalf("DeleteValue: %v", err)
			}
			if resp.GetDeleted() != tc.wantDeleted {
				t.Errorf("deleted = %v, want %v", resp.GetDeleted(), tc.wantDeleted)
			}
		})
	}
}

func TestCoordinateRoundTripsThroughTheWire(t *testing.T) {
	want := vars.Coordinate{Slug: "shop", Folder: "/checkout", Key: "STRIPE_API_KEY", Environment: "staging"}
	if got := toCoordinate(toCoordinateProto(want)); got != want {
		t.Errorf("coordinate round trip = %+v, want %+v", got, want)
	}
}
