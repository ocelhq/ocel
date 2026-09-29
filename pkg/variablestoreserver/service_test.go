package variablestoreserver_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	connect "connectrpc.com/connect"
	"connectrpc.com/validate"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/keyvalue"
	"github.com/ocelhq/ocel/pkg/naming"
	resourcesv1 "github.com/ocelhq/ocel/pkg/proto/app/resources/v1"
	bindingsv1 "github.com/ocelhq/ocel/pkg/proto/common/bindings/v1"
	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"
	variablestorev1 "github.com/ocelhq/ocel/pkg/proto/provider/variablestore/v1"
	"github.com/ocelhq/ocel/pkg/proto/provider/variablestore/v1/variablestorev1connect"
	"github.com/ocelhq/ocel/pkg/provider/fake"
	"github.com/ocelhq/ocel/pkg/stackrecords"
	"github.com/ocelhq/ocel/pkg/variablestoreserver"
)

const slug = "shop"

func served(t *testing.T) (variablestorev1connect.VariableStoreServiceClient, *fake.Provider) {
	t.Helper()

	provider := fake.NewProvider(fake.Options{})
	backend := variablestoreserver.FixedBackend{KeyValues: provider.KeyValues(), Cipher: provider.Cipher(), VerifyGrants: provider.Hooks().VerifyGrants}
	return serve(t, &variablestoreserver.Service{Source: backend, CallerNamesEnvSource: true}), provider
}

func serve(t *testing.T, service *variablestoreserver.Service) variablestorev1connect.VariableStoreServiceClient {
	t.Helper()

	mux := http.NewServeMux()
	mux.Handle(variablestorev1connect.NewVariableStoreServiceHandler(
		service,
		connect.WithInterceptors(validate.NewInterceptor()),
	))
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	return variablestorev1connect.NewVariableStoreServiceClient(server.Client(), server.URL)
}

func cell(key string) *variablestorev1.Coordinate {
	return &variablestorev1.Coordinate{Slug: slug, Key: key}
}

func TestSetGetAndRevealAnswerAcrossTheWire(t *testing.T) {
	variables, _ := served(t)
	ctx := context.Background()

	set, err := variables.SetValue(ctx, &variablestorev1.SetValueRequest{
		Tier:       environmentv1.Tier_TIER_PRODUCTION,
		Coordinate: cell("DATABASE_URL"),
		Value:      "postgres://one",
	})
	if err != nil {
		t.Fatal(err)
	}
	if set.GetMetadata().GetVersion() != 1 || set.GetMetadata().GetCoordinate().GetSlug() != slug {
		t.Fatalf("SetValue() = %+v, want version 1 at the coordinate asked for", set.GetMetadata())
	}

	got, err := variables.GetValue(ctx, &variablestorev1.GetValueRequest{
		Tier:       environmentv1.Tier_TIER_PRODUCTION,
		Coordinate: cell("DATABASE_URL"),
	})
	if err != nil || !got.GetFound() || got.GetValue() != "" {
		t.Fatalf("GetValue() found=%t revealed=%t, %v, want it found and unrevealed", got.GetFound(), got.GetValue() != "", err)
	}

	got, err = variables.GetValue(ctx, &variablestorev1.GetValueRequest{
		Tier:       environmentv1.Tier_TIER_PRODUCTION,
		Coordinate: cell("DATABASE_URL"),
		Reveal:     true,
	})
	if err != nil || got.GetValue() != "postgres://one" {
		t.Fatalf("GetValue(reveal) = %q, %v", got.GetValue(), err)
	}

	missing, err := variables.GetValue(ctx, &variablestorev1.GetValueRequest{
		Tier:       environmentv1.Tier_TIER_PRODUCTION,
		Coordinate: cell("NOTHING_HERE"),
	})
	if err != nil || missing.GetFound() {
		t.Fatalf("GetValue() of a key nobody set found=%t, %v, want it answered as not found", missing.GetFound(), err)
	}

	listed, err := variables.ListValues(ctx, &variablestorev1.ListValuesRequest{
		Tier: environmentv1.Tier_TIER_PRODUCTION,
		Slug: slug,
	})
	if err != nil || len(listed.GetValues()) != 1 {
		t.Fatalf("ListValues() = %+v, %v, want the one value set", listed.GetValues(), err)
	}

	revealed, err := variables.RevealValues(ctx, &variablestorev1.RevealValuesRequest{
		Tier:  environmentv1.Tier_TIER_PRODUCTION,
		Slug:  slug,
		Cells: []*variablestorev1.Coordinate{cell("DATABASE_URL"), cell("NOTHING_HERE")},
	})
	if err != nil || len(revealed.GetValues()) != 1 || revealed.GetValues()[0].GetValue() != "postgres://one" {
		keys := make([]string, 0, len(revealed.GetValues()))
		for _, value := range revealed.GetValues() {
			keys = append(keys, value.GetMetadata().GetCoordinate().GetKey())
		}
		t.Fatalf("RevealValues() revealed %q, %v, want DATABASE_URL's alone and as it was set", keys, err)
	}
}

func TestVersionsAndDeleteAnswerAcrossTheWire(t *testing.T) {
	variables, _ := served(t)
	ctx := context.Background()

	for _, value := range []string{"one", "two"} {
		if _, err := variables.SetValue(ctx, &variablestorev1.SetValueRequest{
			Tier:       environmentv1.Tier_TIER_PRODUCTION,
			Coordinate: cell("KEY"),
			Value:      value,
		}); err != nil {
			t.Fatal(err)
		}
	}

	history, err := variables.ListVersions(ctx, &variablestorev1.ListVersionsRequest{
		Tier:       environmentv1.Tier_TIER_PRODUCTION,
		Coordinate: cell("KEY"),
	})
	if err != nil || len(history.GetVersions()) != 2 {
		t.Fatalf("ListVersions() = %+v, %v, want one entry per write", history.GetVersions(), err)
	}
	if newest := history.GetVersions()[0].GetVersion(); newest != 2 {
		t.Errorf("ListVersions() lists version %d first, want the newest, 2", newest)
	}

	stale := int64(1)
	_, err = variables.DeleteValue(ctx, &variablestorev1.DeleteValueRequest{
		Tier:            environmentv1.Tier_TIER_PRODUCTION,
		Coordinate:      cell("KEY"),
		ExpectedVersion: &stale,
	})
	if got := connect.CodeOf(err); got != connect.CodeAborted {
		t.Fatalf("DeleteValue() at a version that moved: code = %v, want %v — a caller that reads the code must tell a test-and-set conflict from a bootstrap that is not ready", got, connect.CodeAborted)
	}

	deleted, err := variables.DeleteValue(ctx, &variablestorev1.DeleteValueRequest{
		Tier:       environmentv1.Tier_TIER_PRODUCTION,
		Coordinate: cell("KEY"),
	})
	if err != nil || !deleted.GetDeleted() {
		t.Fatalf("DeleteValue() = %+v, %v", deleted, err)
	}
}

func TestAValueOverTheCapIsAnInvalidArgument(t *testing.T) {
	variables, _ := served(t)

	_, err := variables.SetValue(context.Background(), &variablestorev1.SetValueRequest{
		Tier:       environmentv1.Tier_TIER_PRODUCTION,
		Coordinate: cell("KEY"),
		Value:      strings.Repeat("x", 4097),
	})
	if got := connect.CodeOf(err); got != connect.CodeInvalidArgument {
		t.Fatalf("SetValue() over the cap: code = %v, want %v", got, connect.CodeInvalidArgument)
	}
}

func TestAValueNamingNoTierIsRefusedRatherThanWrittenToProduction(t *testing.T) {
	variables, _ := served(t)
	ctx := context.Background()

	_, err := variables.SetValue(ctx, &variablestorev1.SetValueRequest{
		Coordinate: cell("DATABASE_URL"),
		Value:      "postgres://unaddressed",
	})
	if got := connect.CodeOf(err); got != connect.CodeInvalidArgument {
		t.Fatalf("SetValue() with no tier: code = %v, want %v", got, connect.CodeInvalidArgument)
	}

	got, err := variables.GetValue(ctx, &variablestorev1.GetValueRequest{
		Tier:       environmentv1.Tier_TIER_PRODUCTION,
		Coordinate: cell("DATABASE_URL"),
	})
	if err != nil || got.GetFound() {
		t.Fatalf("production GetValue() found=%t, %v, want nothing written there", got.GetFound(), err)
	}
}

func TestReferencesAnswerAcrossTheWire(t *testing.T) {
	variables, _ := served(t)
	ctx := context.Background()

	if _, err := variables.SetValue(ctx, &variablestorev1.SetValueRequest{
		Tier:       environmentv1.Tier_TIER_PRODUCTION,
		Coordinate: &variablestorev1.Coordinate{Slug: "platform", Key: "DATABASE_URL"},
		Value:      "postgres://shared",
	}); err != nil {
		t.Fatal(err)
	}

	set, err := variables.SetReference(ctx, &variablestorev1.SetReferenceRequest{
		Tier:       environmentv1.Tier_TIER_PRODUCTION,
		Coordinate: cell("DATABASE_URL"),
		Target:     &variablestorev1.Coordinate{Slug: "platform", Key: "DATABASE_URL"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if set.GetMetadata().GetTarget().GetSlug() != "platform" {
		t.Fatalf("SetReference() = %+v, want the target reported back", set.GetMetadata())
	}

	got, err := variables.GetValue(ctx, &variablestorev1.GetValueRequest{
		Tier:       environmentv1.Tier_TIER_PRODUCTION,
		Coordinate: cell("DATABASE_URL"),
		Reveal:     true,
	})
	if err != nil || got.GetValue() != "postgres://shared" {
		t.Fatalf("GetValue() through a reference = %q, %v", got.GetValue(), err)
	}

	found, err := variables.ListReferences(ctx, &variablestorev1.ListReferencesRequest{
		Tier:       environmentv1.Tier_TIER_PRODUCTION,
		Coordinate: &variablestorev1.Coordinate{Slug: "platform", Key: "DATABASE_URL"},
	})
	if err != nil || len(found.GetReferences()) != 1 || found.GetReferences()[0].GetSlug() != slug {
		t.Fatalf("ListReferences() = %+v, %v", found.GetReferences(), err)
	}

	_, err = variables.SetValue(ctx, &variablestorev1.SetValueRequest{
		Tier:       environmentv1.Tier_TIER_PRODUCTION,
		Coordinate: cell("DATABASE_URL"),
		Value:      "postgres://mine",
	})
	if got := connect.CodeOf(err); got != connect.CodeInvalidArgument {
		t.Fatalf("SetValue() over a reference: code = %v, want %v", got, connect.CodeInvalidArgument)
	}

	_, err = variables.SetReference(ctx, &variablestorev1.SetReferenceRequest{
		Tier:       environmentv1.Tier_TIER_PRODUCTION,
		Coordinate: &variablestorev1.Coordinate{Slug: "web", Key: "DATABASE_URL"},
		Target:     &variablestorev1.Coordinate{Slug: slug, Key: "DATABASE_URL"},
	})
	if got := connect.CodeOf(err); got != connect.CodeInvalidArgument {
		t.Fatalf("a reference to a reference: code = %v, want %v", got, connect.CodeInvalidArgument)
	}
}

func TestTheEnvironmentGateRefusesWhatNothingWouldRead(t *testing.T) {
	variables, provider := served(t)
	ctx := context.Background()

	named := &variablestorev1.Coordinate{Slug: slug, Key: "KEY", Environment: "pr-7"}

	_, err := variables.SetValue(ctx, &variablestorev1.SetValueRequest{
		Tier:       environmentv1.Tier_TIER_PRODUCTION,
		Coordinate: named,
		Value:      "one",
	})
	if got := connect.CodeOf(err); got != connect.CodeInvalidArgument {
		t.Fatalf("a named environment in production: code = %v, want %v", got, connect.CodeInvalidArgument)
	}

	_, err = variables.SetValue(ctx, &variablestorev1.SetValueRequest{
		Tier:       environmentv1.Tier_TIER_PREVIEW,
		Coordinate: named,
		Value:      "one",
	})
	if got := connect.CodeOf(err); got != connect.CodeFailedPrecondition {
		t.Fatalf("a preview environment nobody deployed: code = %v, want %v", got, connect.CodeFailedPrecondition)
	}

	deployPreview(t, provider, "pr-7")

	if _, err := variables.SetValue(ctx, &variablestorev1.SetValueRequest{
		Tier:       environmentv1.Tier_TIER_PREVIEW,
		Coordinate: named,
		Value:      "one",
	}); err != nil {
		t.Fatalf("SetValue() for a deployed preview environment = %v, want it written", err)
	}

	_, err = variables.SetValue(ctx, &variablestorev1.SetValueRequest{
		Tier:       environmentv1.Tier_TIER_PREVIEW,
		Coordinate: &variablestorev1.Coordinate{Slug: slug, Key: "KEY", Environment: "pr-9"},
		Value:      "one",
	})
	if got := connect.CodeOf(err); got != connect.CodeFailedPrecondition {
		t.Fatalf("an environment beside the deployed one: code = %v, want %v", got, connect.CodeFailedPrecondition)
	}
	if !strings.Contains(err.Error(), "pr-7") {
		t.Fatalf("the refusal does not name the environments that do exist: %v", err)
	}
}

func TestBindingsAnswerAcrossTheWire(t *testing.T) {
	variables, _ := served(t)
	ctx := context.Background()

	binding := &bindingsv1.Binding{
		Name:   "db",
		Source: "neon",
		Properties: &bindingsv1.Binding_Postgres{Postgres: &bindingsv1.PostgresProperties{
			Host:     "db.example",
			Port:     5432,
			Database: "shop",
			Username: "shop",
			Password: "hunter2",
		}},
	}

	set, err := variables.SetBinding(ctx, &variablestorev1.SetBindingRequest{
		Slug:    slug,
		Tier:    environmentv1.Tier_TIER_PRODUCTION,
		Binding: binding,
		Owner:   "neon",
	})
	if err != nil || set.GetVersion() != 1 {
		t.Fatalf("SetBinding() = %+v, %v", set, err)
	}

	listed, err := variables.ListBindings(ctx, &variablestorev1.ListBindingsRequest{
		Slug: slug,
		Tier: environmentv1.Tier_TIER_PRODUCTION,
	})
	if err != nil || len(listed.GetBindings()) != 1 {
		t.Fatalf("ListBindings() = %+v, %v", listed.GetBindings(), err)
	}
	summary := listed.GetBindings()[0]
	if summary.GetName() != "db" || summary.GetOwner() != "neon" || summary.GetSource() != "neon" {
		t.Fatalf("ListBindings() summary = %+v", summary)
	}
	if summary.GetType() != bindingsv1.BindingType_BINDING_TYPE_POSTGRES {
		t.Fatalf("ListBindings() type = %v, want the postgres it published", summary.GetType())
	}
	if len(summary.GetProperties()) == 0 {
		t.Fatal("ListBindings() reported no property shapes, and a consumer binds against them")
	}

	_, err = variables.SetBinding(ctx, &variablestorev1.SetBindingRequest{
		Slug:    slug,
		Tier:    environmentv1.Tier_TIER_PRODUCTION,
		Binding: binding,
		Owner:   "supabase",
	})
	if got := connect.CodeOf(err); got != connect.CodeFailedPrecondition {
		t.Fatalf("a second publisher taking the name: code = %v, want %v", got, connect.CodeFailedPrecondition)
	}

	removed, err := variables.RemoveBinding(ctx, &variablestorev1.RemoveBindingRequest{
		Slug: slug,
		Tier: environmentv1.Tier_TIER_PRODUCTION,
		Name: "db",
	})
	if err != nil || !removed.GetRemoved() {
		t.Fatalf("RemoveBinding() = %+v, %v", removed, err)
	}
	listed, err = variables.ListBindings(ctx, &variablestorev1.ListBindingsRequest{
		Slug: slug,
		Tier: environmentv1.Tier_TIER_PRODUCTION,
	})
	if err != nil || len(listed.GetBindings()) != 0 {
		t.Fatalf("ListBindings() after RemoveBinding() = %+v, %v", listed.GetBindings(), err)
	}
}

func TestABindingOcelCouldNotHaveProducedIsRefused(t *testing.T) {
	variables, _ := served(t)
	ctx := context.Background()

	for name, binding := range map[string]*bindingsv1.Binding{
		"unsourced": {
			Name:       "db",
			Properties: &bindingsv1.Binding_Postgres{Postgres: &bindingsv1.PostgresProperties{Host: "db.example"}},
		},
		"granting no action": {
			Name:       "files",
			Source:     "acme",
			Properties: &bindingsv1.Binding_Bucket{Bucket: &bindingsv1.BucketProperties{Bucket: "files"}},
			Grants:     []*bindingsv1.Grant{{Resources: []string{"arn:aws:s3:::files"}}},
		},
		"granting over no resource": {
			Name:       "files",
			Source:     "acme",
			Properties: &bindingsv1.Binding_Bucket{Bucket: &bindingsv1.BucketProperties{Bucket: "files"}},
			Grants:     []*bindingsv1.Grant{{Actions: []string{"s3:GetObject"}}},
		},
		"granting every action": {
			Name:       "files",
			Source:     "acme",
			Properties: &bindingsv1.Binding_Bucket{Bucket: &bindingsv1.BucketProperties{Bucket: "files"}},
			Grants:     []*bindingsv1.Grant{{Actions: []string{"*"}, Resources: []string{"arn:aws:s3:::files"}}},
		},
		"granting over every resource": {
			Name:       "files",
			Source:     "acme",
			Properties: &bindingsv1.Binding_Bucket{Bucket: &bindingsv1.BucketProperties{Bucket: "files"}},
			Grants:     []*bindingsv1.Grant{{Actions: []string{"s3:GetObject"}, Resources: []string{"*"}}},
		},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := variables.SetBinding(ctx, &variablestorev1.SetBindingRequest{
				Slug:    slug,
				Tier:    environmentv1.Tier_TIER_PRODUCTION,
				Binding: binding,
				Owner:   "acme",
			})
			if got := connect.CodeOf(err); got != connect.CodeInvalidArgument {
				t.Fatalf("SetBinding(): code = %v, want %v", got, connect.CodeInvalidArgument)
			}
		})
	}
}

func TestABindingNamesAnEnvironmentOnlyInPreview(t *testing.T) {
	variables, _ := served(t)

	_, err := variables.ListBindings(context.Background(), &variablestorev1.ListBindingsRequest{
		Slug:        slug,
		Tier:        environmentv1.Tier_TIER_PRODUCTION,
		Environment: "pr-7",
	})
	if got := connect.CodeOf(err); got != connect.CodeInvalidArgument {
		t.Fatalf("a named environment in production: code = %v, want %v", got, connect.CodeInvalidArgument)
	}
}

func deployPreview(t *testing.T, provider *fake.Provider, preview string) {
	t.Helper()
	name := stackrecords.StackKey(environment.TierPreview, slug, naming.InfraStack(preview))
	if _, err := provider.KeyValues().Write(context.Background(), keyvalue.Entry{Key: name, Value: []byte("{}")}); err != nil {
		t.Fatalf("record a deployed preview environment: %v", err)
	}
}

func TestTheRecordsInlineBindingsKeepAreWrittenByThemAlone(t *testing.T) {
	variables, _ := served(t)
	ctx := context.Background()
	record := func(name string) *bindingsv1.Binding {
		return &bindingsv1.Binding{
			Name:       name,
			Source:     "ocel.json",
			Properties: &bindingsv1.Binding_Postgres{Postgres: &bindingsv1.PostgresProperties{Url: "postgres://u:p@db/shop"}},
		}
	}
	inline := naming.InlineRecordName(resourcesv1.ResourceType_RESOURCE_TYPE_POSTGRES, "orders")

	for name, req := range map[string]*variablestorev1.SetBindingRequest{
		"a publisher writing a reserved name":       {Slug: slug, Tier: environmentv1.Tier_TIER_PRODUCTION, Binding: record(inline), Owner: "terraform"},
		"the inline owner writing an ordinary name": {Slug: slug, Tier: environmentv1.Tier_TIER_PRODUCTION, Binding: record("orders"), Owner: naming.InlineRecordOwner},
	} {
		if _, err := variables.SetBinding(ctx, req); connect.CodeOf(err) != connect.CodeInvalidArgument {
			t.Errorf("%s: code = %v, want %v", name, connect.CodeOf(err), connect.CodeInvalidArgument)
		}
	}

	if _, err := variables.SetBinding(ctx, &variablestorev1.SetBindingRequest{Slug: slug, Tier: environmentv1.Tier_TIER_PRODUCTION, Binding: record(inline), Owner: naming.InlineRecordOwner}); err != nil {
		t.Fatalf("the inline owner writing its own record: %v", err)
	}
}
