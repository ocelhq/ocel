package declare

import (
	"testing"

	resourcesv1 "github.com/ocelhq/ocel/pkg/proto/resources/v1"
)

func TestParse_RejectsUnspecifiedResourceType(t *testing.T) {
	_, err := Parse(&resourcesv1.DeclareRequest{
		Resource: &resourcesv1.ResourceIdentifier{Name: "main"},
	})
	if err == nil {
		t.Fatal("Parse: expected error for unspecified resource type, got nil")
	}
}

func TestParse_RejectsMissingResource(t *testing.T) {
	_, err := Parse(&resourcesv1.DeclareRequest{})
	if err == nil {
		t.Fatal("Parse: expected error for missing resource, got nil")
	}
}

func TestParse_ReturnsNameAndType(t *testing.T) {
	res, err := Parse(&resourcesv1.DeclareRequest{
		Resource: &resourcesv1.ResourceIdentifier{Name: "main", Type: resourcesv1.ResourceType_RESOURCE_TYPE_POSTGRES},
	})
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if res.Name != "main" {
		t.Fatalf("Name = %q, want %q", res.Name, "main")
	}
	if res.Type != resourcesv1.ResourceType_RESOURCE_TYPE_POSTGRES {
		t.Fatalf("Type = %v, want %v", res.Type, resourcesv1.ResourceType_RESOURCE_TYPE_POSTGRES)
	}
}

func TestParse_CarriesTypedPostgresConfig(t *testing.T) {
	res, err := Parse(&resourcesv1.DeclareRequest{
		Resource: &resourcesv1.ResourceIdentifier{Name: "main", Type: resourcesv1.ResourceType_RESOURCE_TYPE_POSTGRES},
		Config:   &resourcesv1.DeclareRequest_Postgres{Postgres: &resourcesv1.PostgresConfig{Version: "17"}},
	})
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if res.Postgres == nil || res.Postgres.Version != "17" {
		t.Fatalf("Postgres = %+v, want version 17", res.Postgres)
	}
}

func TestParse_CarriesTypedBucketConfig(t *testing.T) {
	res, err := Parse(&resourcesv1.DeclareRequest{
		Resource: &resourcesv1.ResourceIdentifier{Name: "storage", Type: resourcesv1.ResourceType_RESOURCE_TYPE_BUCKET},
		Config:   &resourcesv1.DeclareRequest_Bucket{Bucket: &resourcesv1.BucketConfig{AllowedOrigins: []string{"https://app.example.com"}}},
	})
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if res.Bucket == nil || len(res.Bucket.GetAllowedOrigins()) != 1 || res.Bucket.GetAllowedOrigins()[0] != "https://app.example.com" {
		t.Fatalf("Bucket = %+v, want allowed_origins [https://app.example.com]", res.Bucket)
	}
}

func TestParse_NoConfigLeavesPostgresNil(t *testing.T) {
	res, err := Parse(&resourcesv1.DeclareRequest{
		Resource: &resourcesv1.ResourceIdentifier{Name: "main", Type: resourcesv1.ResourceType_RESOURCE_TYPE_POSTGRES},
	})
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if res.Postgres != nil {
		t.Fatalf("Postgres = %+v, want nil", res.Postgres)
	}
}
