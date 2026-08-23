package server

import (
	"context"
	"errors"
	"maps"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	connect "connectrpc.com/connect"

	"github.com/aws/aws-sdk-go-v2/aws"

	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"
	envvarsv1 "github.com/ocelhq/ocel/pkg/proto/provider/envvars/v1"
	"github.com/ocelhq/ocel/pkg/proto/provider/envvars/v1/envvarsv1connect"
	"github.com/ocelhq/ocel/pkg/providerkit/values"
	"github.com/ocelhq/ocel/platform/aws/provider/bootstrap"
	awsports "github.com/ocelhq/ocel/platform/aws/provider/ports"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

func newTestVarsClient(t *testing.T, token string) envvarsv1connect.EnvVarsServiceClient {
	t.Helper()
	srv := httptest.NewServer(NewMux(testToken, "1.0.0"))
	t.Cleanup(srv.Close)

	var opts []connect.ClientOption
	if token != "" {
		opts = append(opts, connect.WithInterceptors(authHeaderInterceptor{token: token}))
	}
	return envvarsv1connect.NewEnvVarsServiceClient(srv.Client(), srv.URL, opts...)
}

func TestListValues(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name  string
		token string
	}{
		{name: "a caller with no session token is unauthenticated"},
		{name: "a caller holding another session's token is unauthenticated", token: "wrong-token"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, err := newTestVarsClient(t, tc.token).ListValues(context.Background(), &envvarsv1.ListValuesRequest{Slug: "shop"})

			var connectErr *connect.Error
			if !errors.As(err, &connectErr) || connectErr.Code() != connect.CodeUnauthenticated {
				t.Fatalf("ListValues err = %v, want CodeUnauthenticated", err)
			}
		})
	}
}

func TestVarsError(t *testing.T) {
	t.Parallel()

	t.Run("classifies what the store refused", func(t *testing.T) {
		t.Parallel()
		for _, tc := range []struct {
			name string
			err  error
			want connect.Code
		}{
			{name: "a write that lost a race", err: values.ErrStaleVersion, want: connect.CodeFailedPrecondition},
			{name: "a cell that was never set", err: values.ErrNotFound, want: connect.CodeNotFound},
			{name: "a reference to a reference", err: values.ErrWouldDeepen, want: connect.CodeInvalidArgument},
			{name: "a value written over a reference", err: values.ErrIsReference, want: connect.CodeInvalidArgument},
		} {
			t.Run(tc.name, func(t *testing.T) {
				t.Parallel()
				var connectErr *connect.Error
				if !errors.As(varsError(tc.err), &connectErr) || connectErr.Code() != tc.want {
					t.Errorf("varsError(%v) = %v, want %v", tc.err, varsError(tc.err), tc.want)
				}
			})
		}
	})

	t.Run("leaves anything else unclassified", func(t *testing.T) {
		t.Parallel()
		other := errors.New("the table is on fire")
		var connectErr *connect.Error
		if errors.As(varsError(other), &connectErr) {
			t.Errorf("varsError(%v) = %v, want it left unclassified", other, varsError(other))
		}
	})
}

func TestVarsServerStoreLookup(t *testing.T) {
	t.Parallel()

	t.Run("finds the store once per session", func(t *testing.T) {
		t.Parallel()
		cfn, ddb, crypto := &countingCFN{}, newFakeDynamo(), &fakeKMS{}
		s := testAccount(cfn, ddb, crypto)
		ctx := context.Background()

		for range 3 {
			if _, err := s.ListValues(ctx, &envvarsv1.ListValuesRequest{Slug: "shop"}); err != nil {
				t.Fatalf("ListValues: %v", err)
			}
			if _, err := s.GetValue(ctx, &envvarsv1.GetValueRequest{
				Coordinate: &envvarsv1.Coordinate{Slug: "shop", Key: "STRIPE_API_KEY"},
				Reveal:     true,
			}); err != nil {
				t.Fatalf("GetValue: %v", err)
			}
		}

		if got := cfn.bootstraps(); len(got) != 1 {
			t.Errorf("six reads cost %d bootstrap describes, want 1", len(got))
		}
	})

	t.Run("finds each bootstrap's own store", func(t *testing.T) {
		t.Parallel()
		cfn := &countingCFN{}
		s := testAccount(cfn, newFakeDynamo(), &fakeKMS{})
		ctx := context.Background()

		for _, tier := range []environmentv1.Tier{
			environmentv1.Tier_TIER_PRODUCTION,
			environmentv1.Tier_TIER_PREVIEW,
			environmentv1.Tier_TIER_PRODUCTION,
			environmentv1.Tier_TIER_PREVIEW,
		} {
			if _, err := s.ListValues(ctx, &envvarsv1.ListValuesRequest{Slug: "shop", Tier: tier}); err != nil {
				t.Fatalf("ListValues(%v): %v", tier, err)
			}
		}

		got := cfn.bootstraps()
		if len(got) != 2 {
			t.Errorf("reads across both bootstraps cost %d bootstrap describes, want 2 (one each)", len(got))
		}
		want := []string{bootstrap.StackName, bootstrap.PreviewStackName}
		if !slices.Equal(got, want) {
			t.Errorf("described %v, want %v", got, want)
		}
	})
}

func TestRevealValues(t *testing.T) {
	t.Parallel()
	t.Run("reads each named cell once and decrypts only what is set", func(t *testing.T) {
		cfn, ddb, crypto := &countingCFN{}, newFakeDynamo(), &fakeKMS{}
		s := testAccount(cfn, ddb, crypto)
		ctx := context.Background()

		want := map[string]string{"STRIPE_API_KEY": "sk-live", "WEBHOOK_URL": "https://hooks.example", "SESSION_KEY": "sess"}
		for key, value := range want {
			if _, err := s.SetValue(ctx, &envvarsv1.SetValueRequest{
				Coordinate: &envvarsv1.Coordinate{Slug: "shop", Key: key},
				Value:      value,
			}); err != nil {
				t.Fatalf("SetValue %s: %v", key, err)
			}
		}

		_, readsBefore := ddb.counts()
		decryptsBefore := crypto.count()

		resp, err := s.RevealValues(ctx, &envvarsv1.RevealValuesRequest{
			Slug: "shop",
			Cells: []*envvarsv1.Coordinate{
				{Slug: "shop", Key: "STRIPE_API_KEY"},
				{Slug: "shop", Key: "WEBHOOK_URL"},
				{Slug: "shop", Key: "SESSION_KEY"},
				{Slug: "shop", Key: "NEVER_SET"},
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

		_, readsAfter := ddb.counts()
		if reads := readsAfter - readsBefore; reads != 4 {
			t.Errorf("revealing 4 cells cost %d reads, want one per cell", reads)
		}
		if decrypts := crypto.count() - decryptsBefore; decrypts != len(want) {
			t.Errorf("revealing 4 cells cost %d decrypts, want %d (KMS has no batch decrypt, and an unset cell has nothing to decrypt)", decrypts, len(want))
		}
	})
}

func atVersion(n int64) *int64 { return &n }

func guardedCell(t *testing.T, seeded bool) (*VarsServer, *envvarsv1.Coordinate) {
	t.Helper()
	s := testAccount(&countingCFN{}, newFakeDynamo(), &fakeKMS{})
	coordinate := &envvarsv1.Coordinate{Slug: "shop", Key: "STRIPE_API_KEY"}
	if seeded {
		if _, err := s.SetValue(context.Background(), &envvarsv1.SetValueRequest{
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

func TestSetValue(t *testing.T) {
	t.Parallel()

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
			t.Parallel()
			s, coordinate := guardedCell(t, tc.seeded)

			resp, err := s.SetValue(context.Background(), &envvarsv1.SetValueRequest{
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

func TestDeleteValue(t *testing.T) {
	t.Parallel()

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
			t.Parallel()
			s, coordinate := guardedCell(t, tc.seeded)

			resp, err := s.DeleteValue(context.Background(), &envvarsv1.DeleteValueRequest{
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

func TestSetValueAtAnEnvironmentOverride(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name        string
		tier        environmentv1.Tier
		environment string
		exists      []string
		wantCode    connect.Code
		wantIn      string
	}{
		{
			name: "an environment that exists", tier: environmentv1.Tier_TIER_PREVIEW,
			environment: "staging", exists: []string{"pr-7", "staging"},
		},
		{
			name: "a misspelt environment", tier: environmentv1.Tier_TIER_PREVIEW,
			environment: "stagng", exists: []string{"pr-7", "staging"},
			wantCode: connect.CodeFailedPrecondition, wantIn: "staging",
		},
		{
			name: "a project with no environments at all", tier: environmentv1.Tier_TIER_PREVIEW,
			environment: "staging",
			wantCode:    connect.CodeFailedPrecondition, wantIn: "ocel preview",
		},
		{
			name: "any environment on production", tier: environmentv1.Tier_TIER_PRODUCTION,
			environment: "staging", exists: []string{"staging"},
			wantCode: connect.CodeInvalidArgument, wantIn: "single environment",
		},
		{
			name: "the class-wide value, which names no environment", tier: environmentv1.Tier_TIER_PRODUCTION,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s := testAccount(&countingCFN{}, newFakeDynamo(), &fakeKMS{})
			s.listEnvironments = func(context.Context, string, string) ([]string, error) { return tc.exists, nil }

			_, err := s.SetValue(context.Background(), &envvarsv1.SetValueRequest{
				Tier:       tc.tier,
				Coordinate: &envvarsv1.Coordinate{Slug: "shop", Key: "STRIPE_API_KEY", Environment: tc.environment},
				Value:      "sk-live",
			})

			if tc.wantCode == 0 {
				if err != nil {
					t.Fatalf("SetValue: %v", err)
				}
				return
			}
			var connectErr *connect.Error
			if !errors.As(err, &connectErr) || connectErr.Code() != tc.wantCode {
				t.Fatalf("SetValue err = %v, want %v", err, tc.wantCode)
			}
			if !strings.Contains(err.Error(), tc.wantIn) {
				t.Errorf("err = %v, want it to name %q", err, tc.wantIn)
			}
			got, getErr := s.GetValue(context.Background(), &envvarsv1.GetValueRequest{
				Tier:       tc.tier,
				Coordinate: &envvarsv1.Coordinate{Slug: "shop", Key: "STRIPE_API_KEY", Environment: tc.environment},
				Reveal:     true,
			})
			if getErr != nil {
				t.Fatalf("GetValue after a refused write: %v", getErr)
			}
			if got.GetFound() {
				t.Errorf("the refused write landed anyway: %q", got.GetValue())
			}
		})
	}
}

func TestSetReference(t *testing.T) {
	t.Parallel()

	t.Run("reads a value owned by another project", func(t *testing.T) {
		t.Parallel()
		s := testAccount(&countingCFN{}, newFakeDynamo(), &fakeKMS{})
		ctx := context.Background()

		if _, err := s.SetValue(ctx, &envvarsv1.SetValueRequest{
			Coordinate: &envvarsv1.Coordinate{Slug: "platform", Key: "STRIPE_API_KEY"},
			Value:      "sk-shared",
		}); err != nil {
			t.Fatalf("SetValue: %v", err)
		}

		at := &envvarsv1.Coordinate{Slug: "shop", Folder: "/checkout", Key: "STRIPE_API_KEY"}
		target := &envvarsv1.Coordinate{Slug: "platform", Key: "STRIPE_API_KEY"}
		written, err := s.SetReference(ctx, &envvarsv1.SetReferenceRequest{Coordinate: at, Target: target})
		if err != nil {
			t.Fatalf("SetReference: %v", err)
		}
		if got := written.GetMetadata().GetTarget(); got.GetSlug() != "platform" || got.GetKey() != "STRIPE_API_KEY" {
			t.Errorf("written target = %v, want the cell the reference points at", got)
		}

		got, err := s.GetValue(ctx, &envvarsv1.GetValueRequest{Coordinate: at, Reveal: true})
		if err != nil {
			t.Fatalf("GetValue: %v", err)
		}
		if got.GetValue() != "sk-shared" {
			t.Errorf("resolved value = %q, want the other project's", got.GetValue())
		}
		if got.GetMetadata().GetTarget().GetSlug() != "platform" {
			t.Errorf("read target = %v, want the wire to say the cell is a reference", got.GetMetadata().GetTarget())
		}

		found, err := s.ListReferences(ctx, &envvarsv1.ListReferencesRequest{Coordinate: target})
		if err != nil {
			t.Fatalf("ListReferences: %v", err)
		}
		if len(found.GetReferences()) != 1 || found.GetReferences()[0].GetSlug() != "shop" {
			t.Errorf("ListReferences = %v, want the one cell in another project reading this value", found.GetReferences())
		}
	})

	t.Run("refuses an address no runtime would ever ask for", func(t *testing.T) {
		t.Parallel()
		s := testAccount(&countingCFN{}, newFakeDynamo(), &fakeKMS{})

		_, err := s.SetReference(context.Background(), &envvarsv1.SetReferenceRequest{
			Tier:       environmentv1.Tier_TIER_PRODUCTION,
			Coordinate: &envvarsv1.Coordinate{Slug: "shop", Key: "STRIPE_API_KEY", Environment: "staging"},
			Target:     &envvarsv1.Coordinate{Slug: "platform", Key: "STRIPE_API_KEY"},
		})

		var connectErr *connect.Error
		if !errors.As(err, &connectErr) || connectErr.Code() != connect.CodeInvalidArgument {
			t.Fatalf("SetReference at a production override err = %v, want CodeInvalidArgument", err)
		}
		if !strings.Contains(err.Error(), "single environment") {
			t.Errorf("err = %v, want the same refusal a value written there gets", err)
		}
	})
}

func TestCoordinate(t *testing.T) {
	t.Parallel()

	t.Run("round trips through the wire", func(t *testing.T) {
		t.Parallel()
		want := values.Coordinate{Cell: values.Cell{Folder: "/checkout", Key: "STRIPE_API_KEY"}, Environment: "staging"}
		if got := coordinateOf(coordinateProto("shop", want)); got != want {
			t.Errorf("coordinate round trip = %+v, want %+v", got, want)
		}
	})
}

func TestTeardownValues(t *testing.T) {
	t.Parallel()

	t.Run("is absent until the bootstrap has a store", func(t *testing.T) {
		t.Parallel()
		if store := teardownValues(aws.Config{}, bootstrap.Deployed{Present: true}, bootstrap.ClassProduction); store != nil {
			t.Errorf("teardownValues = %v, want none for a bootstrap with no value store", store)
		}
	})

	t.Run("opens the bootstrap's own table under the bootstrap's class", func(t *testing.T) {
		t.Parallel()
		deployed := bootstrap.Deployed{Present: true, StateTable: "ocel-state", VarsKeyARN: "arn:aws:kms:us-east-1:111122223333:key/abcd"}
		purger, ok := teardownValues(aws.Config{}, deployed, bootstrap.ClassPreview).(projectValues)
		if !ok {
			t.Fatal("teardownValues returned no store for a bootstrapped account")
		}
		records, ok := purger.store.Records.(awsports.Records)
		if !ok || records.Table != deployed.StateTable {
			t.Errorf("records = %+v, want the bootstrap's own table", purger.store.Records)
		}
		sealer, ok := purger.store.Sealer.(awsports.Sealer)
		if !ok || sealer.KeyARN != deployed.VarsKeyARN {
			t.Errorf("sealer = %+v, want the bootstrap's own key", purger.store.Sealer)
		}
		if purger.class != edge.ClassPreview {
			t.Errorf("store class = %q, want the bootstrap's own class", purger.class)
		}
	})
}
