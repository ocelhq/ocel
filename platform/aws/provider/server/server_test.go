package server

import (
	"maps"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"

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

func TestStackIndexFor(t *testing.T) {
	t.Parallel()

	t.Run("an unindexed substrate is refused up front", func(t *testing.T) {
		t.Parallel()

		_, err := stackIndexFor(aws.Config{Region: "us-east-1"}, bootstrap.Deployed{Present: true}, "ocel bootstrap")
		if err == nil {
			t.Fatal("stackIndexFor err = nil, want a teardown refused before it destroys anything")
		}
		if !strings.Contains(err.Error(), "ocel bootstrap") {
			t.Errorf("err = %v, want it to name the command that fixes it", err)
		}
	})

	t.Run("an indexed substrate yields its table", func(t *testing.T) {
		t.Parallel()

		index, err := stackIndexFor(aws.Config{Region: "us-east-1"}, bootstrap.Deployed{Present: true, StateTable: "ocel-state"}, "ocel bootstrap")
		if err != nil {
			t.Fatalf("stackIndexFor: %v", err)
		}
		if index == nil {
			t.Fatal("stackIndexFor = nil with no error")
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
