package server

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ssm"
	ssmtypes "github.com/aws/aws-sdk-go-v2/service/ssm/types"

	"github.com/ocelhq/ocel/cloud/aws/bootstrap"
	deploymentsv1 "github.com/ocelhq/ocel/pkg/proto/deployments/v1"
	resourcesv1 "github.com/ocelhq/ocel/pkg/proto/resources/v1"
)

func wellFormedManifest() *deploymentsv1.Manifest {
	return &deploymentsv1.Manifest{
		SchemaVersion: "provider.v1",
		ProjectId:     "proj_123",
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

func TestValidateManifest_MissingProjectID(t *testing.T) {
	m := wellFormedManifest()
	m.ProjectId = ""
	if err := validateManifest(m); err == nil {
		t.Fatal("validateManifest() error = nil, want error for missing project_id")
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

// stubSSM answers GetParameter with a fixed value or a fixed error, standing in
// for a substrate whose edge-values parameter is present, absent, or unreadable.
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

// A denied read must degrade, exactly as the edge credentials read above it
// does: it is the edge's own state, not something a deploy cannot proceed
// without.
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

// TestCacheStoreUploader_ZeroStoreIsAnUntypedNil pins the rollback path at its
// one fragile point: the deploy reads a nil uploader as "no store adopted", and
// a typed nil returned into the interface would read as adopted and seed every
// entry into a bucket named "".
func TestCacheStoreUploader_ZeroStoreIsAnUntypedNil(t *testing.T) {
	if up := cacheStoreUploader(bootstrap.CacheStore{}); up != nil {
		t.Errorf("cacheStoreUploader(zero) = %v, want nil", up)
	}
}

// TestCacheStoreUploader_AdoptedStoreIsAddressable proves an adopted store yields
// a client, which is what routes seeding away from the provider's own bucket.
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
