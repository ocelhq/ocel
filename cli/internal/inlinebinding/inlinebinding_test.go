package inlinebinding

import (
	"strings"
	"testing"

	"google.golang.org/protobuf/proto"

	"github.com/ocelhq/ocel/cli/internal/project"
	resourcesv1 "github.com/ocelhq/ocel/pkg/proto/app/resources/v1"
	bindingsv1 "github.com/ocelhq/ocel/pkg/proto/common/bindings/v1"
)

const postgres = resourcesv1.ResourceType_RESOURCE_TYPE_POSTGRES

func inline(p project.PostgresInline) project.Binding {
	return project.Binding{Type: postgres, Name: "orders", Inline: &project.Inline{Postgres: &p}}
}

func TestBuild(t *testing.T) {
	t.Run("a url binding keeps the url whole", func(t *testing.T) {
		records, err := Build([]project.Binding{inline(project.PostgresInline{URL: "ORDERS_URL"})},
			map[string]string{"ORDERS_URL": "postgres://u:p@ep-cool.neon.tech/orders?sslmode=require&options=endpoint%3Dep-cool"}, "ocel.json")
		if err != nil {
			t.Fatalf("Build: %v", err)
		}
		want := &bindingsv1.Binding{
			Name:   "ocel:postgres.orders",
			Source: "ocel.json",
			Properties: &bindingsv1.Binding_Postgres{Postgres: &bindingsv1.PostgresProperties{
				Url: "postgres://u:p@ep-cool.neon.tech/orders?sslmode=require&options=endpoint%3Dep-cool",
			}},
		}
		if len(records) != 1 || !proto.Equal(records[0], want) {
			t.Fatalf("Build = %d records, want one binding %s by its url", len(records), want.GetName())
		}
	})

	t.Run("a host binding reads each field from its literal or its variable", func(t *testing.T) {
		records, err := Build([]project.Binding{inline(project.PostgresInline{
			Host:     project.Text{Literal: "db.example.com"},
			Database: project.Text{Variable: "ORDERS_DB"},
			Username: project.Text{Literal: "app"},
			Password: "ORDERS_PASSWORD",
			TLS:      &project.PostgresTLS{Mode: "verify-full", CA: "ORDERS_CA"},
		})}, map[string]string{"ORDERS_DB": "orders", "ORDERS_PASSWORD": "hunter2", "ORDERS_CA": "-----BEGIN CERTIFICATE-----"}, "ocel.json")
		if err != nil {
			t.Fatalf("Build: %v", err)
		}
		want := &bindingsv1.PostgresProperties{
			Host: "db.example.com", Port: 5432, Database: "orders", Username: "app", Password: "hunter2",
			TlsMode: bindingsv1.PostgresTlsMode_POSTGRES_TLS_MODE_VERIFY_FULL, TlsCa: "-----BEGIN CERTIFICATE-----",
		}
		if got := records[0].GetPostgres(); !proto.Equal(got, want) {
			t.Errorf("properties = %s:%d/%s as %s, tls %v, want %s:%d/%s as %s, tls %v",
				got.GetHost(), got.GetPort(), got.GetDatabase(), got.GetUsername(), got.GetTlsMode(),
				want.GetHost(), want.GetPort(), want.GetDatabase(), want.GetUsername(), want.GetTlsMode())
		}
	})

	t.Run("a published binding is no record ocel writes", func(t *testing.T) {
		records, err := Build([]project.Binding{{Type: postgres, Name: "analytics", External: "warehouse"}}, nil, "ocel.json")
		if err != nil || len(records) != 0 {
			t.Fatalf("Build = %d records, %v, want nothing", len(records), err)
		}
	})

	t.Run("a variable with no value is refused naming it", func(t *testing.T) {
		_, err := Build([]project.Binding{inline(project.PostgresInline{URL: "ORDERS_URL"})}, map[string]string{}, "ocel.json")
		if err == nil || !strings.Contains(err.Error(), "ORDERS_URL") {
			t.Fatalf("Build = %v, want ORDERS_URL named", err)
		}
	})
}

func TestBuildABucket(t *testing.T) {
	records, err := Build([]project.Binding{{
		Type: resourcesv1.ResourceType_RESOURCE_TYPE_BUCKET, Name: "uploads",
		Inline: &project.Inline{Bucket: &project.BucketInline{
			Endpoint:        project.Text{Literal: "https://abc.r2.cloudflarestorage.com"},
			Region:          project.Text{Literal: "auto"},
			Bucket:          project.Text{Variable: "UPLOADS_BUCKET"},
			Prefix:          project.Text{Literal: "uploads/"},
			AccessKeyID:     "R2_KEY",
			SecretAccessKey: "R2_SECRET",
			PublicBaseURL:   project.Text{Literal: "https://cdn.acme.com/uploads"},
		}},
	}}, map[string]string{"UPLOADS_BUCKET": "acme", "R2_KEY": "AKID", "R2_SECRET": "s3cr3t"}, "ocel.json")
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	want := &bindingsv1.BucketProperties{
		Bucket: "acme", Endpoint: "https://abc.r2.cloudflarestorage.com", Region: "auto", Prefix: "uploads/",
		AccessKeyId: "AKID", SecretAccessKey: "s3cr3t", PublicBaseUrl: "https://cdn.acme.com/uploads",
	}
	if got := records[0].GetBucket(); !proto.Equal(got, want) {
		t.Errorf("bucket = %s at %s under %q, public base url %q, want %s at %s under %q, public base url %q: the public base url is the one the binding names, as written",
			got.GetBucket(), got.GetEndpoint(), got.GetPrefix(), got.GetPublicBaseUrl(), want.GetBucket(), want.GetEndpoint(), want.GetPrefix(), want.GetPublicBaseUrl())
	}
}
