package inlinebinding

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"google.golang.org/protobuf/proto"

	"github.com/ocelhq/ocel/cli/internal/projectconfig"
	resourcesv1 "github.com/ocelhq/ocel/pkg/proto/app/resources/v1"
	bindingsv1 "github.com/ocelhq/ocel/pkg/proto/common/bindings/v1"
)

const postgres = resourcesv1.ResourceType_RESOURCE_TYPE_POSTGRES

func inline(p projectconfig.PostgresInline) projectconfig.TierBinding {
	return projectconfig.TierBinding{Type: postgres, Name: "orders", Inline: &projectconfig.Inline{Postgres: &p}}
}

func TestBuild(t *testing.T) {
	t.Run("a url binding carries the url whole", func(t *testing.T) {
		records, err := Build([]projectconfig.TierBinding{inline(projectconfig.PostgresInline{URL: "ORDERS_URL"})},
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
		if len(records) != 1 || !proto.Equal(records[0].Binding, want) {
			t.Fatalf("Build = %v, want %v", records, want)
		}
		if records[0].Declared != "orders" || records[0].Site != "bindings.postgres.orders" {
			t.Errorf("record = %+v, want it to name the declared resource and the config site", records[0])
		}
	})

	t.Run("a host binding reads each field from its literal or its variable", func(t *testing.T) {
		records, err := Build([]projectconfig.TierBinding{inline(projectconfig.PostgresInline{
			Host:     projectconfig.Value{Literal: "db.example.com"},
			Database: projectconfig.Value{Variable: "ORDERS_DB"},
			Username: projectconfig.Value{Literal: "app"},
			Password: "ORDERS_PASSWORD",
			TLS:      &projectconfig.PostgresTLS{Mode: "verify-full", CA: "ORDERS_CA"},
		})}, map[string]string{"ORDERS_DB": "orders", "ORDERS_PASSWORD": "hunter2", "ORDERS_CA": "-----BEGIN CERTIFICATE-----"}, "ocel.json")
		if err != nil {
			t.Fatalf("Build: %v", err)
		}
		want := &bindingsv1.PostgresProperties{
			Host: "db.example.com", Port: 5432, Database: "orders", Username: "app", Password: "hunter2",
			TlsMode: "verify-full", TlsCa: "-----BEGIN CERTIFICATE-----",
		}
		if got := records[0].Binding.GetPostgres(); !proto.Equal(got, want) {
			t.Errorf("properties = %v, want %v", got, want)
		}
	})

	t.Run("a published binding is no record ocel writes", func(t *testing.T) {
		records, err := Build([]projectconfig.TierBinding{{Type: postgres, Name: "analytics", External: "warehouse"}}, nil, "ocel.json")
		if err != nil || len(records) != 0 {
			t.Fatalf("Build = %v, %v, want nothing", records, err)
		}
	})

	t.Run("a variable with no value is refused naming it", func(t *testing.T) {
		_, err := Build([]projectconfig.TierBinding{inline(projectconfig.PostgresInline{URL: "ORDERS_URL"})}, map[string]string{}, "ocel.json")
		if err == nil || !strings.Contains(err.Error(), "ORDERS_URL") {
			t.Fatalf("Build = %v, want ORDERS_URL named", err)
		}
	})
}

func record(props *bindingsv1.PostgresProperties) Record {
	return Record{
		Declared: "orders",
		Site:     "bindings.postgres.orders",
		Type:     postgres,
		Binding:  &bindingsv1.Binding{Name: "ocel:postgres.orders", Properties: &bindingsv1.Binding_Postgres{Postgres: props}},
	}
}

func versions(version string) Declared {
	return Declared{Postgres: map[string]string{"orders": version}}
}

func TestVerify(t *testing.T) {
	props := &bindingsv1.PostgresProperties{Host: "db", Port: 5432, Database: "orders", Username: "app", Password: "hunter2"}

	t.Run("a server of the declared major version passes", func(t *testing.T) {
		probe := func(context.Context, *bindingsv1.PostgresProperties) (int, error) { return 170004, nil }
		if _, err := Verify(context.Background(), []Record{record(props)}, versions("17"), Probes{Postgres: probe}); err != nil {
			t.Fatalf("Verify = %v", err)
		}
	})

	t.Run("a server of another major version is refused, naming both", func(t *testing.T) {
		probe := func(context.Context, *bindingsv1.PostgresProperties) (int, error) { return 150008, nil }
		_, err := Verify(context.Background(), []Record{record(props)}, versions("17"), Probes{Postgres: probe})
		if err == nil {
			t.Fatal("Verify = nil, want a version mismatch refused")
		}
		for _, want := range []string{"bindings.postgres.orders", "17", "15"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("Verify = %v, want it to name %s", err, want)
			}
		}
	})

	t.Run("a server numbered before postgres 10 is read as the two-part major it is", func(t *testing.T) {
		probe := func(context.Context, *bindingsv1.PostgresProperties) (int, error) { return 90624, nil }
		if _, err := Verify(context.Background(), []Record{record(props)}, versions("9.6"), Probes{Postgres: probe}); err != nil {
			t.Fatalf("Verify(9.6 against 9.6.24) = %v", err)
		}
		_, err := Verify(context.Background(), []Record{record(props)}, versions("9.5"), Probes{Postgres: probe})
		if err == nil || !strings.Contains(err.Error(), "serves postgres 9.6") {
			t.Fatalf("Verify(9.5 against 9.6.24) = %v, want it refused naming 9.6", err)
		}
		_, err = Verify(context.Background(), []Record{record(props)}, versions("17"), Probes{Postgres: probe})
		if err == nil || !strings.Contains(err.Error(), "serves postgres 9.6") {
			t.Fatalf("Verify(17 against 9.6.24) = %v, want it refused naming 9.6", err)
		}
	})

	t.Run("a server that cannot be reached is refused without repeating the password", func(t *testing.T) {
		probe := func(context.Context, *bindingsv1.PostgresProperties) (int, error) {
			return 0, errors.New("failed to connect to user=app database=orders password=hunter2: connection refused")
		}
		_, err := Verify(context.Background(), []Record{record(props)}, versions("17"), Probes{Postgres: probe})
		if err == nil {
			t.Fatal("Verify = nil, want an unreachable server refused")
		}
		if strings.Contains(err.Error(), "hunter2") {
			t.Errorf("Verify = %v, repeats the password", err)
		}
		if !strings.Contains(err.Error(), "bindings.postgres.orders") || !strings.Contains(err.Error(), "connection refused") {
			t.Errorf("Verify = %v, want the binding and the cause named", err)
		}
	})

	t.Run("a url's password is kept out of the refusal too", func(t *testing.T) {
		byURL := &bindingsv1.PostgresProperties{Url: "postgres://app:s3cr%40t@db/orders"}
		probe := func(context.Context, *bindingsv1.PostgresProperties) (int, error) {
			return 0, errors.New("cannot parse postgres://app:s3cr%40t@db/orders: s3cr@t is wrong")
		}
		_, err := Verify(context.Background(), []Record{record(byURL)}, Declared{}, Probes{Postgres: probe})
		if err == nil {
			t.Fatal("Verify = nil")
		}
		for _, secret := range []string{"s3cr%40t", "s3cr@t"} {
			if strings.Contains(err.Error(), secret) {
				t.Errorf("Verify = %v, repeats %q", err, secret)
			}
		}
	})
}

func bucketRecord(props *bindingsv1.BucketProperties) Record {
	return Record{
		Declared: "uploads",
		Site:     "bindings.bucket.uploads",
		Type:     resourcesv1.ResourceType_RESOURCE_TYPE_BUCKET,
		Binding:  &bindingsv1.Binding{Name: "ocel:bucket.uploads", Properties: &bindingsv1.Binding_Bucket{Bucket: props}},
	}
}

func TestBuildABucket(t *testing.T) {
	records, err := Build([]projectconfig.TierBinding{{
		Type: resourcesv1.ResourceType_RESOURCE_TYPE_BUCKET, Name: "uploads",
		Inline: &projectconfig.Inline{Bucket: &projectconfig.BucketInline{
			Endpoint:        projectconfig.Value{Literal: "https://abc.r2.cloudflarestorage.com"},
			Region:          projectconfig.Value{Literal: "auto"},
			Bucket:          projectconfig.Value{Variable: "UPLOADS_BUCKET"},
			Prefix:          projectconfig.Value{Literal: "uploads/"},
			AccessKeyID:     "R2_KEY",
			SecretAccessKey: "R2_SECRET",
			PublicBaseURL:   projectconfig.Value{Literal: "https://cdn.acme.com/"},
		}},
	}}, map[string]string{"UPLOADS_BUCKET": "acme", "R2_KEY": "AKID", "R2_SECRET": "s3cr3t"}, "ocel.json")
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	want := &bindingsv1.BucketProperties{
		Bucket: "acme", Endpoint: "https://abc.r2.cloudflarestorage.com", Region: "auto", Prefix: "uploads/",
		AccessKeyId: "AKID", SecretAccessKey: "s3cr3t", PublicBaseUrl: "https://cdn.acme.com/uploads",
	}
	if got := records[0].Binding.GetBucket(); !proto.Equal(got, want) {
		t.Errorf("bucket = %v, want %v: an object's public address carries the prefix it is kept under", got, want)
	}
}

func TestVerifyABucket(t *testing.T) {
	props := &bindingsv1.BucketProperties{Bucket: "acme", Endpoint: "https://s3.example.com", PublicBaseUrl: "https://cdn.acme.com"}

	t.Run("hands the probe what the code declares, and passes its warnings on", func(t *testing.T) {
		var asked []string
		probe := func(_ context.Context, _ *bindingsv1.BucketProperties, public bool, origins []string) ([]string, error) {
			if public {
				asked = append(asked, "public")
			}
			asked = append(asked, origins...)
			return []string{"could not read the policy"}, nil
		}
		declared := Declared{Buckets: map[string]*resourcesv1.BucketConfig{"uploads": {Public: true, AllowedOrigins: []string{"https://acme.com"}}}}
		warnings, err := Verify(context.Background(), []Record{bucketRecord(props)}, declared, Probes{Bucket: probe})
		if err != nil {
			t.Fatalf("Verify = %v", err)
		}
		if !slices.Equal(asked, []string{"public", "https://acme.com"}) {
			t.Errorf("probe asked for %v", asked)
		}
		if len(warnings) != 1 || !strings.Contains(warnings[0], "bindings.bucket.uploads") {
			t.Errorf("warnings = %v, want the probe's warning, naming the binding", warnings)
		}
	})

	t.Run("a public bucket with no public address is refused before the store is asked", func(t *testing.T) {
		probe := func(context.Context, *bindingsv1.BucketProperties, bool, []string) ([]string, error) {
			t.Fatal("the store was asked about a binding the config already rules out")
			return nil, nil
		}
		declared := Declared{Buckets: map[string]*resourcesv1.BucketConfig{"uploads": {Public: true}}}
		_, err := Verify(context.Background(), []Record{bucketRecord(&bindingsv1.BucketProperties{Bucket: "acme", Endpoint: "https://s3.example.com"})}, declared, Probes{Bucket: probe})
		if err == nil || !strings.Contains(err.Error(), "publicBaseUrl") {
			t.Fatalf("Verify = %v, want the missing publicBaseUrl named", err)
		}
	})

	t.Run("a refusal from the store names the binding", func(t *testing.T) {
		probe := func(context.Context, *bindingsv1.BucketProperties, bool, []string) ([]string, error) {
			return nil, errors.New("bucket acme did not answer: NoSuchBucket")
		}
		_, err := Verify(context.Background(), []Record{bucketRecord(props)}, Declared{}, Probes{Bucket: probe})
		if err == nil || !strings.Contains(err.Error(), "bindings.bucket.uploads") || !strings.Contains(err.Error(), "NoSuchBucket") {
			t.Fatalf("Verify = %v", err)
		}
	})
}
