package server

import (
	"context"
	"errors"
	"maps"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ssm"
	ssmtypes "github.com/aws/aws-sdk-go-v2/service/ssm/types"

	deploymentsv1 "github.com/ocelhq/ocel/pkg/proto/deployments/v1"
	resourcesv1 "github.com/ocelhq/ocel/pkg/proto/resources/v1"
	"github.com/ocelhq/ocel/platform/aws/provider/bootstrap"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

func wellFormedManifest() *deploymentsv1.Manifest {
	return &deploymentsv1.Manifest{
		SchemaVersion: "provider.v1",
		Slug:          "proj-123",
		Resources: []*deploymentsv1.ManifestResource{
			{
				LogicalName: "postgres_main",
				Resource: &resourcesv1.ResourceIdentifier{
					Type: resourcesv1.ResourceType_RESOURCE_TYPE_POSTGRES,
					Name: "main",
				},
				Config: &deploymentsv1.ManifestResource_Postgres{
					Postgres: &resourcesv1.PostgresConfig{Version: "17"},
				},
			},
		},
	}
}

func TestValidateManifest(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name    string
		mutate  func(*deploymentsv1.Manifest)
		wantErr bool
	}{
		{name: "a well-formed manifest"},
		{name: "a missing schema_version", mutate: func(m *deploymentsv1.Manifest) { m.SchemaVersion = "" }, wantErr: true},
		{name: "a missing slug", mutate: func(m *deploymentsv1.Manifest) { m.Slug = "" }, wantErr: true},
		{name: "a resource with no logical_name", mutate: func(m *deploymentsv1.Manifest) { m.Resources[0].LogicalName = "" }, wantErr: true},
		{
			name: "a resource of an unspecified type",
			mutate: func(m *deploymentsv1.Manifest) {
				m.Resources[0].Resource.Type = resourcesv1.ResourceType_RESOURCE_TYPE_UNSPECIFIED
			},
			wantErr: true,
		},
		{name: "a resource with no identifier", mutate: func(m *deploymentsv1.Manifest) { m.Resources[0].Resource = nil }, wantErr: true},
		{name: "a resource with no typed config", mutate: func(m *deploymentsv1.Manifest) { m.Resources[0].Config = nil }, wantErr: true},
		{name: "a manifest declaring no resources", mutate: func(m *deploymentsv1.Manifest) { m.Resources = nil }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			m := wellFormedManifest()
			if tc.mutate != nil {
				tc.mutate(m)
			}
			err := validateManifest(m)
			if tc.wantErr != (err != nil) {
				t.Fatalf("validateManifest() error = %v, wantErr = %v", err, tc.wantErr)
			}
		})
	}

	t.Run("a nil manifest", func(t *testing.T) {
		t.Parallel()
		if err := validateManifest(nil); err == nil {
			t.Fatal("validateManifest(nil) error = nil, want error")
		}
	})
}

func TestResourceSummary(t *testing.T) {
	t.Parallel()

	m := wellFormedManifest()
	m.Resources[0].Config = &deploymentsv1.ManifestResource_Postgres{
		Postgres: &resourcesv1.PostgresConfig{Version: "15"},
	}

	got := resourceSummary(m.Resources[0])
	want := "postgres_main: postgres version=15"
	if got != want {
		t.Fatalf("resourceSummary() = %q, want %q", got, want)
	}
}

type stubSSM struct {
	value string
	err   error
}

func (s stubSSM) GetParameter(context.Context, *ssm.GetParameterInput, ...func(*ssm.Options)) (*ssm.GetParameterOutput, error) {
	if s.err != nil {
		return nil, s.err
	}
	return &ssm.GetParameterOutput{Parameter: &ssmtypes.Parameter{Value: aws.String(s.value)}}, nil
}

func (stubSSM) PutParameter(context.Context, *ssm.PutParameterInput, ...func(*ssm.Options)) (*ssm.PutParameterOutput, error) {
	return &ssm.PutParameterOutput{}, nil
}

func (stubSSM) DeleteParameter(context.Context, *ssm.DeleteParameterInput, ...func(*ssm.Options)) (*ssm.DeleteParameterOutput, error) {
	return &ssm.DeleteParameterOutput{}, nil
}

func TestReadEdgeValues(t *testing.T) {
	t.Parallel()

	t.Run("returns the values the edge stored", func(t *testing.T) {
		t.Parallel()
		got := readEdgeValues(context.Background(), stubSSM{value: `{"bucketName":"edge-cache-7f3"}`}, bootstrap.ClassProduction, "ocel bootstrap", func(string) {})
		if len(got) != 1 || got["bucketName"] != "edge-cache-7f3" {
			t.Errorf("readEdgeValues = %v, want the stored values", got)
		}
	})

	t.Run("degrades with a log when the parameter cannot be read", func(t *testing.T) {
		t.Parallel()
		var logged []string
		got := readEdgeValues(
			context.Background(),
			stubSSM{err: errors.New("AccessDeniedException: not authorized to perform ssm:GetParameter")},
			bootstrap.ClassProduction,
			"ocel bootstrap",
			func(m string) { logged = append(logged, m) },
		)
		if got != nil {
			t.Errorf("readEdgeValues = %v, want none", got)
		}
		if len(logged) != 1 || !strings.Contains(logged[0], "AccessDenied") {
			t.Errorf("logged = %v, want one line naming the failure", logged)
		}
	})

	t.Run("stays silent when the parameter is simply absent", func(t *testing.T) {
		t.Parallel()
		var logged []string
		got := readEdgeValues(context.Background(), stubSSM{err: &ssmtypes.ParameterNotFound{}}, bootstrap.ClassProduction, "ocel bootstrap", func(m string) { logged = append(logged, m) })
		if got != nil {
			t.Errorf("readEdgeValues = %v, want none", got)
		}
		if len(logged) != 0 {
			t.Errorf("an edge that stored no values is not a failure to report, got %v", logged)
		}
	})
}

func TestCacheStoreUploader(t *testing.T) {
	t.Parallel()

	t.Run("a zero store is an untyped nil", func(t *testing.T) {
		t.Parallel()
		if up := cacheStoreUploader(bootstrap.CacheStore{}); up != nil {
			t.Errorf("cacheStoreUploader(zero) = %v, want nil", up)
		}
	})

	t.Run("an adopted store is addressable", func(t *testing.T) {
		t.Parallel()
		store := bootstrap.CacheStore{
			Bucket:          "isr",
			Endpoint:        "https://acct.r2.cloudflarestorage.com",
			Region:          "auto",
			AccessKeyID:     "AK",
			SecretAccessKey: "s3cret",
		}
		if up := cacheStoreUploader(store); up == nil {
			t.Error("cacheStoreUploader on an adopted store = nil, want a client")
		}
	})
}

func TestRootStackStateChanged(t *testing.T) {
	t.Parallel()

	reconciled := edge.RootStackState{
		edge.RootStackKeySlug:       "proj-123",
		edge.RootStackKeyEndpoint:   "https://store.workers.dev",
		edge.RootStackKeySecret:     "s3cret",
		edge.RootStackKeyOwnerToken: "owner",
	}

	tests := []struct {
		name       string
		prior      edge.RootStackState
		reconciled edge.RootStackState
		want       bool
	}{
		{
			name:       "a first reconcile has nothing stored yet",
			prior:      nil,
			reconciled: reconciled,
			want:       true,
		},
		{
			name:       "a redeploy that changed nothing writes nothing",
			prior:      maps.Clone(reconciled),
			reconciled: reconciled,
			want:       false,
		},
		{
			name:  "an adopted instance answering with a different secret is persisted",
			prior: maps.Clone(reconciled),
			reconciled: edge.RootStackState{
				edge.RootStackKeySlug:       "proj-123",
				edge.RootStackKeyEndpoint:   "https://store.workers.dev",
				edge.RootStackKeySecret:     "rotated",
				edge.RootStackKeyOwnerToken: "owner",
			},
			want: true,
		},
		{
			name:  "a renamed project names a different instance",
			prior: maps.Clone(reconciled),
			reconciled: edge.RootStackState{
				edge.RootStackKeySlug:       "proj-456",
				edge.RootStackKeyEndpoint:   "https://store.workers.dev",
				edge.RootStackKeySecret:     "s3cret",
				edge.RootStackKeyOwnerToken: "owner",
			},
			want: true,
		},
		{
			name:       "a deploy that failed before reconcile leaves the stored state alone",
			prior:      maps.Clone(reconciled),
			reconciled: nil,
			want:       false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := rootStackStateChanged(tc.prior, tc.reconciled); got != tc.want {
				t.Errorf("rootStackStateChanged() = %v, want %v", got, tc.want)
			}
		})
	}
}
