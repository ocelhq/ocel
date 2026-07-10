package main

import (
	"testing"

	providerv1 "github.com/ocelhq/ocel/pkg/proto/provider/v1"
	resourcesv1 "github.com/ocelhq/ocel/pkg/proto/resources/v1"
)

func wellFormedManifest() *providerv1.Manifest {
	return &providerv1.Manifest{
		SchemaVersion: "provider.v1",
		ProjectId:     "proj_123",
		Resources: []*providerv1.ManifestResource{
			{
				LogicalName: "postgres_main",
				Resource: &resourcesv1.ResourceIdentifier{
					Type: resourcesv1.ResourceType_RESOURCE_TYPE_POSTGRES,
					Name: "main",
				},
				Config: &providerv1.ManifestResource_Postgres{
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
