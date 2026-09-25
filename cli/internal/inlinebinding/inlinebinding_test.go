package inlinebinding

import (
	"context"
	"errors"
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

func TestVerify(t *testing.T) {
	props := &bindingsv1.PostgresProperties{Host: "db", Port: 5432, Database: "orders", Username: "app", Password: "hunter2"}

	t.Run("a server of the declared major version passes", func(t *testing.T) {
		probe := func(context.Context, *bindingsv1.PostgresProperties) (int, error) { return 170004, nil }
		if err := Verify(context.Background(), []Record{record(props)}, map[string]string{"orders": "17"}, probe); err != nil {
			t.Fatalf("Verify = %v", err)
		}
	})

	t.Run("a server of another major version is refused, naming both", func(t *testing.T) {
		probe := func(context.Context, *bindingsv1.PostgresProperties) (int, error) { return 150008, nil }
		err := Verify(context.Background(), []Record{record(props)}, map[string]string{"orders": "17"}, probe)
		if err == nil {
			t.Fatal("Verify = nil, want a version mismatch refused")
		}
		for _, want := range []string{"bindings.postgres.orders", "17", "15"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("Verify = %v, want it to name %s", err, want)
			}
		}
	})

	t.Run("a server that cannot be reached is refused without repeating the password", func(t *testing.T) {
		probe := func(context.Context, *bindingsv1.PostgresProperties) (int, error) {
			return 0, errors.New("failed to connect to user=app database=orders password=hunter2: connection refused")
		}
		err := Verify(context.Background(), []Record{record(props)}, map[string]string{"orders": "17"}, probe)
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
		err := Verify(context.Background(), []Record{record(byURL)}, nil, probe)
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
