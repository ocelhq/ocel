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

	"github.com/ocelhq/ocel/platform/aws/provider/bootstrap"
	"github.com/ocelhq/ocel/cloud/edge"
	deploymentsv1 "github.com/ocelhq/ocel/pkg/proto/deployments/v1"
	resourcesv1 "github.com/ocelhq/ocel/pkg/proto/resources/v1"
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

func TestValidateManifest_WellFormed(t *testing.T) {
	if err := validateManifest(wellFormedManifest()); err != nil {
		t.Fatalf("validateManifest() error = %v, want nil", err)
	}
}

func TestValidateManifest_Nil(t *testing.T) {
	if err := validateManifest(nil); err == nil {
		t.Fatal("validateManifest(nil) error = nil, want error")
	}
}

func TestValidateManifest_MissingSchemaVersion(t *testing.T) {
	m := wellFormedManifest()
	m.SchemaVersion = ""
	if err := validateManifest(m); err == nil {
		t.Fatal("validateManifest() error = nil, want error for missing schema_version")
	}
}

func TestValidateManifest_MissingSlug(t *testing.T) {
	m := wellFormedManifest()
	m.Slug = ""
	if err := validateManifest(m); err == nil {
		t.Fatal("validateManifest() error = nil, want error for missing slug")
	}
}

func TestValidateManifest_MissingLogicalName(t *testing.T) {
	m := wellFormedManifest()
	m.Resources[0].LogicalName = ""
	if err := validateManifest(m); err == nil {
		t.Fatal("validateManifest() error = nil, want error for missing logical_name")
	}
}

func TestValidateManifest_UnspecifiedResourceType(t *testing.T) {
	m := wellFormedManifest()
	m.Resources[0].Resource.Type = resourcesv1.ResourceType_RESOURCE_TYPE_UNSPECIFIED
	if err := validateManifest(m); err == nil {
		t.Fatal("validateManifest() error = nil, want error for unspecified resource type")
	}
}

func TestValidateManifest_MissingResourceIdentifier(t *testing.T) {
	m := wellFormedManifest()
	m.Resources[0].Resource = nil
	if err := validateManifest(m); err == nil {
		t.Fatal("validateManifest() error = nil, want error for missing resource identifier")
	}
}

func TestValidateManifest_MissingConfig(t *testing.T) {
	m := wellFormedManifest()
	m.Resources[0].Config = nil
	if err := validateManifest(m); err == nil {
		t.Fatal("validateManifest() error = nil, want error for missing typed config")
	}
}

func TestValidateManifest_EmptyResourcesOK(t *testing.T) {
	m := wellFormedManifest()
	m.Resources = nil
	if err := validateManifest(m); err != nil {
		t.Fatalf("validateManifest() error = %v, want nil for a manifest with no resources", err)
	}
}

func TestResourceSummary_PostgresIncludesTypedVersion(t *testing.T) {
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

func TestReadEdgeValues_ReturnsStoredValues(t *testing.T) {
	got := readEdgeValues(context.Background(), stubSSM{value: `{"bucketName":"edge-cache-7f3"}`}, bootstrap.ClassProduction, "ocel bootstrap", func(string) {})
	if len(got) != 1 || got["bucketName"] != "edge-cache-7f3" {
		t.Errorf("readEdgeValues = %v, want the stored values", got)
	}
}

func TestReadEdgeValues_UnreadableParameterDegradesWithALog(t *testing.T) {
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
}

func TestReadEdgeValues_AbsentParameterIsSilent(t *testing.T) {
	var logged []string
	got := readEdgeValues(context.Background(), stubSSM{err: &ssmtypes.ParameterNotFound{}}, bootstrap.ClassProduction, "ocel bootstrap", func(m string) { logged = append(logged, m) })
	if got != nil {
		t.Errorf("readEdgeValues = %v, want none", got)
	}
	if len(logged) != 0 {
		t.Errorf("an edge that stored no values is not a failure to report, got %v", logged)
	}
}

func TestCacheStoreUploader_ZeroStoreIsAnUntypedNil(t *testing.T) {
	if up := cacheStoreUploader(bootstrap.CacheStore{}); up != nil {
		t.Errorf("cacheStoreUploader(zero) = %v, want nil", up)
	}
}

func TestCacheStoreUploader_AdoptedStoreIsAddressable(t *testing.T) {
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
}

func TestRootStackStateChanged(t *testing.T) {
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
			if got := rootStackStateChanged(tc.prior, tc.reconciled); got != tc.want {
				t.Errorf("rootStackStateChanged() = %v, want %v", got, tc.want)
			}
		})
	}
}
