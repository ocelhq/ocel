package variablestoreserver_test

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"

	bindingsv1 "github.com/ocelhq/ocel/pkg/proto/common/bindings/v1"
	variablestorev1 "github.com/ocelhq/ocel/pkg/proto/provider/variablestore/v1"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/variablestoreserver"
)

func TestAGrantOfEveryActionOfAServiceOrOneActionOfEveryServiceIsUnscoped(t *testing.T) {
	t.Parallel()

	for _, action := range []string{"s3:*", "*:GetObject", "*"} {
		binding := &variablestorev1.PublishedBinding{Binding: &bindingsv1.Binding{Name: "uploads"}, Grants: []*variablestorev1.Grant{{
			Label:     "read",
			Actions:   []string{"s3:GetObject", action},
			Resources: []string{"arn:aws:s3:::uploads/*"},
		}}}
		if err := variablestoreserver.VerifyGrantScope(binding); !errors.Is(err, provider.ErrUnscopedGrant) {
			t.Errorf("VerifyGrantScope(%q) = %v, want it refused as unscoped: a binding grants an app what it needs of the resource it binds", action, err)
		}
	}
}

func TestAGrantNamingItsActionsAndResourcesIsInScope(t *testing.T) {
	t.Parallel()

	binding := &variablestorev1.PublishedBinding{Binding: &bindingsv1.Binding{Name: "uploads"}, Grants: []*variablestorev1.Grant{{
		Label:     "read",
		Actions:   []string{"s3:GetObject", "storage.objects.get"},
		Resources: []string{"arn:aws:s3:::uploads/*"},
	}}}
	if err := variablestoreserver.VerifyGrantScope(binding); err != nil {
		t.Errorf("VerifyGrantScope() = %v, want a grant naming its actions and resources accepted", err)
	}
}

func TestAPublishedFixtureIsAcceptedAndCarriesTheRecordAnAppReads(t *testing.T) {
	t.Parallel()

	published := filepath.Join("..", "..", "proto", "provider", "variablestore", "v1", "fixtures")
	delivered := filepath.Join("..", "..", "proto", "common", "bindings", "v1", "fixtures")
	entries, err := os.ReadDir(published)
	if err != nil {
		t.Fatalf("read the published fixtures: %v", err)
	}
	for _, entry := range entries {
		raw, err := os.ReadFile(filepath.Join(published, entry.Name()))
		if err != nil {
			t.Fatal(err)
		}
		record, err := variablestoreserver.DecodePublishedBinding(raw)
		if err != nil {
			t.Fatalf("%s is not a published binding: %v", entry.Name(), err)
		}
		if err := variablestoreserver.VerifyBinding(record); err != nil {
			t.Errorf("%s is refused: %v", entry.Name(), err)
		}
		if len(record.GetGrants()) == 0 {
			t.Errorf("%s carries no grants, and a published fixture shows what a publisher grants beside the record", entry.Name())
		}
		raw, err = os.ReadFile(filepath.Join(delivered, entry.Name()))
		if err != nil {
			t.Fatalf("%s has no app fixture of the same name: %v", entry.Name(), err)
		}
		app := &bindingsv1.Binding{}
		if err := protojson.Unmarshal(raw, app); err != nil {
			t.Fatal(err)
		}
		if !proto.Equal(record.GetBinding(), app) {
			t.Errorf("%s publishes a binding that is not the record an app reads from %s", entry.Name(), filepath.Join(delivered, entry.Name()))
		}
	}
}

func TestTheSealedValueCarriesTheGrantsAndTheRecordOnlyItsShape(t *testing.T) {
	t.Parallel()

	published := &variablestorev1.PublishedBinding{
		Binding: &bindingsv1.Binding{
			Name:       "orders",
			Source:     "pulumi",
			Properties: &bindingsv1.Binding_Postgres{Postgres: &bindingsv1.PostgresProperties{Host: "db.example", Password: "hunter2"}},
		},
		Grants: []*variablestorev1.Grant{{Label: "connect", Actions: []string{"rds-db:connect"}, Resources: []string{"arn:aws:rds-db:us-east-1:1:dbuser:cluster/orders"}}},
	}
	pair, err := variablestoreserver.BindingPair("pulumi", published)
	if err != nil {
		t.Fatalf("BindingPair: %v", err)
	}
	value, err := variablestoreserver.DecodePublishedBinding(pair.Value)
	if err != nil || !proto.Equal(value, published) {
		t.Errorf("value %q with grants %v, %v, want the published binding with its grants: a deploy renders the app's permissions from it", value.GetBinding().GetName(), value.GetGrants(), err)
	}
	record, err := variablestoreserver.DecodePublishedBinding(pair.Record)
	if err != nil {
		t.Fatalf("record: %v", err)
	}
	if record.GetBinding().GetPostgres().GetPassword() != "" || record.GetBinding().GetPostgres().GetHost() != "" {
		t.Errorf("record keeps host %q or a password, want the properties left out of what is stored unsealed", record.GetBinding().GetPostgres().GetHost())
	}
	if record.GetBinding().GetPostgres() == nil || record.GetBinding().GetSource() != "pulumi" || !proto.Equal(record.GetGrants()[0], published.GetGrants()[0]) {
		t.Errorf("record has postgres %t, source %q, grants %v, want its type, source and grants kept for a listing", record.GetBinding().GetPostgres() != nil, record.GetBinding().GetSource(), record.GetGrants())
	}
}
