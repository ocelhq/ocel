package declare

import (
	"testing"

	"github.com/ocelhq/ocel/pkg/naming"
	resourcesv1 "github.com/ocelhq/ocel/pkg/proto/resources/v1"
)

func TestParse(t *testing.T) {
	t.Parallel()

	rejects := []struct {
		name string
		req  *resourcesv1.DeclareRequest
	}{
		{
			name: "rejects an unspecified resource type",
			req: &resourcesv1.DeclareRequest{
				Resource: &resourcesv1.ResourceIdentifier{Name: "main"},
			},
		},
		{
			name: "rejects a missing resource",
			req:  &resourcesv1.DeclareRequest{},
		},
	}
	for _, tc := range rejects {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			if _, err := Parse(tc.req); err == nil {
				t.Fatalf("Parse: expected error, got nil")
			}
		})
	}

	t.Run("returns name and type", func(t *testing.T) {
		t.Parallel()

		res, err := Parse(&resourcesv1.DeclareRequest{
			Resource: &resourcesv1.ResourceIdentifier{Name: "main", Type: naming.TokenPostgres},
		})
		if err != nil {
			t.Fatalf("Parse: %v", err)
		}
		if res.Name != "main" {
			t.Fatalf("Name = %q, want %q", res.Name, "main")
		}
		if res.Type != naming.TokenPostgres {
			t.Fatalf("Type = %v, want %v", res.Type, naming.TokenPostgres)
		}
	})

	t.Run("carries typed postgres config", func(t *testing.T) {
		t.Parallel()

		res, err := Parse(&resourcesv1.DeclareRequest{
			Resource: &resourcesv1.ResourceIdentifier{Name: "main", Type: naming.TokenPostgres},
			Config:   &resourcesv1.DeclareRequest_Postgres{Postgres: &resourcesv1.PostgresConfig{Version: "17"}},
		})
		if err != nil {
			t.Fatalf("Parse: %v", err)
		}
		if res.Postgres == nil || res.Postgres.Version != "17" {
			t.Fatalf("Postgres = %+v, want version 17", res.Postgres)
		}
	})

	t.Run("carries typed bucket config", func(t *testing.T) {
		t.Parallel()

		res, err := Parse(&resourcesv1.DeclareRequest{
			Resource: &resourcesv1.ResourceIdentifier{Name: "storage", Type: naming.TokenBucket},
			Config:   &resourcesv1.DeclareRequest_Bucket{Bucket: &resourcesv1.BucketConfig{AllowedOrigins: []string{"https://app.example.com"}}},
		})
		if err != nil {
			t.Fatalf("Parse: %v", err)
		}
		if res.Bucket == nil || len(res.Bucket.GetAllowedOrigins()) != 1 || res.Bucket.GetAllowedOrigins()[0] != "https://app.example.com" {
			t.Fatalf("Bucket = %+v, want allowed_origins [https://app.example.com]", res.Bucket)
		}
	})

	t.Run("no config leaves postgres nil", func(t *testing.T) {
		t.Parallel()

		res, err := Parse(&resourcesv1.DeclareRequest{
			Resource: &resourcesv1.ResourceIdentifier{Name: "main", Type: naming.TokenPostgres},
		})
		if err != nil {
			t.Fatalf("Parse: %v", err)
		}
		if res.Postgres != nil {
			t.Fatalf("Postgres = %+v, want nil", res.Postgres)
		}
	})
}
