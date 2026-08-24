package providerkit_test

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"

	connect "connectrpc.com/connect"

	"github.com/ocelhq/ocel/pkg/channel"
	"github.com/ocelhq/ocel/pkg/naming"
	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"
	linksv1 "github.com/ocelhq/ocel/pkg/proto/common/links/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/proto/provider/contract/v1/contractv1connect"
	envvarsv1 "github.com/ocelhq/ocel/pkg/proto/provider/envvars/v1"
	"github.com/ocelhq/ocel/pkg/proto/provider/envvars/v1/envvarsv1connect"
	"github.com/ocelhq/ocel/pkg/providerkit"
	"github.com/ocelhq/ocel/pkg/providerkit/fake"
)

const slug = "shop"

func served(t *testing.T) (envvarsv1connect.EnvVarsServiceClient, *fake.Provider) {
	t.Helper()

	provider := fake.NewProvider(fake.Options{})
	spec := providerkit.Spec{
		Version: "test",
		New: func(context.Context, providerkit.Options) (providerkit.Provider, error) {
			return provider, nil
		},
	}
	const token = "a-token"
	server := httptest.NewServer(providerkit.NewMux(spec, token))
	t.Cleanup(server.Close)

	auth := connect.WithInterceptors(connect.UnaryInterceptorFunc(
		func(next connect.UnaryFunc) connect.UnaryFunc {
			return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
				req.Header().Set("Authorization", channel.FormatAuthHeader(token))
				return next(ctx, req)
			}
		}))

	contract := contractv1connect.NewProviderServiceClient(server.Client(), server.URL, auth)
	if _, err := contract.Configure(context.Background(), &contractv1.ConfigureRequest{}); err != nil {
		t.Fatalf("Configure() error = %v", err)
	}
	return envvarsv1connect.NewEnvVarsServiceClient(server.Client(), server.URL, auth), provider
}

func cell(key string) *envvarsv1.Coordinate {
	return &envvarsv1.Coordinate{Slug: slug, Key: key}
}

func TestSetGetAndRevealAnswerAcrossTheWire(t *testing.T) {
	vars, _ := served(t)
	ctx := context.Background()

	set, err := vars.SetValue(ctx, &envvarsv1.SetValueRequest{
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

	got, err := vars.GetValue(ctx, &envvarsv1.GetValueRequest{
		Tier:       environmentv1.Tier_TIER_PRODUCTION,
		Coordinate: cell("DATABASE_URL"),
	})
	if err != nil || !got.GetFound() || got.GetValue() != "" {
		t.Fatalf("GetValue() = %+v, %v, want it found and unrevealed", got, err)
	}

	got, err = vars.GetValue(ctx, &envvarsv1.GetValueRequest{
		Tier:       environmentv1.Tier_TIER_PRODUCTION,
		Coordinate: cell("DATABASE_URL"),
		Reveal:     true,
	})
	if err != nil || got.GetValue() != "postgres://one" {
		t.Fatalf("GetValue(reveal) = %q, %v", got.GetValue(), err)
	}

	missing, err := vars.GetValue(ctx, &envvarsv1.GetValueRequest{
		Tier:       environmentv1.Tier_TIER_PRODUCTION,
		Coordinate: cell("NOTHING_HERE"),
	})
	if err != nil || missing.GetFound() {
		t.Fatalf("GetValue() of a key nobody set = %+v, %v, want it answered as not found", missing, err)
	}

	listed, err := vars.ListValues(ctx, &envvarsv1.ListValuesRequest{
		Tier: environmentv1.Tier_TIER_PRODUCTION,
		Slug: slug,
	})
	if err != nil || len(listed.GetValues()) != 1 {
		t.Fatalf("ListValues() = %+v, %v, want the one value set", listed.GetValues(), err)
	}

	revealed, err := vars.RevealValues(ctx, &envvarsv1.RevealValuesRequest{
		Tier:  environmentv1.Tier_TIER_PRODUCTION,
		Slug:  slug,
		Cells: []*envvarsv1.Coordinate{cell("DATABASE_URL"), cell("NOTHING_HERE")},
	})
	if err != nil || len(revealed.GetValues()) != 1 || revealed.GetValues()[0].GetValue() != "postgres://one" {
		t.Fatalf("RevealValues() = %+v, %v", revealed.GetValues(), err)
	}
}

func TestVersionsAndDeleteAnswerAcrossTheWire(t *testing.T) {
	vars, _ := served(t)
	ctx := context.Background()

	for _, value := range []string{"one", "two"} {
		if _, err := vars.SetValue(ctx, &envvarsv1.SetValueRequest{
			Tier:       environmentv1.Tier_TIER_PRODUCTION,
			Coordinate: cell("KEY"),
			Value:      value,
		}); err != nil {
			t.Fatal(err)
		}
	}

	history, err := vars.ListVersions(ctx, &envvarsv1.ListVersionsRequest{
		Tier:       environmentv1.Tier_TIER_PRODUCTION,
		Coordinate: cell("KEY"),
	})
	if err != nil || len(history.GetVersions()) != 2 {
		t.Fatalf("ListVersions() = %+v, %v, want one entry per write", history.GetVersions(), err)
	}

	stale := int64(1)
	_, err = vars.DeleteValue(ctx, &envvarsv1.DeleteValueRequest{
		Tier:            environmentv1.Tier_TIER_PRODUCTION,
		Coordinate:      cell("KEY"),
		ExpectedVersion: &stale,
	})
	if got := connect.CodeOf(err); got != connect.CodeFailedPrecondition {
		t.Fatalf("DeleteValue() at a version that moved: code = %v, want %v", got, connect.CodeFailedPrecondition)
	}

	deleted, err := vars.DeleteValue(ctx, &envvarsv1.DeleteValueRequest{
		Tier:       environmentv1.Tier_TIER_PRODUCTION,
		Coordinate: cell("KEY"),
	})
	if err != nil || !deleted.GetDeleted() {
		t.Fatalf("DeleteValue() = %+v, %v", deleted, err)
	}
}

func TestAValueOverTheCapIsAnInvalidArgument(t *testing.T) {
	vars, _ := served(t)

	_, err := vars.SetValue(context.Background(), &envvarsv1.SetValueRequest{
		Tier:       environmentv1.Tier_TIER_PRODUCTION,
		Coordinate: cell("KEY"),
		Value:      strings.Repeat("x", 4097),
	})
	if got := connect.CodeOf(err); got != connect.CodeInvalidArgument {
		t.Fatalf("SetValue() over the cap: code = %v, want %v", got, connect.CodeInvalidArgument)
	}
}

func TestReferencesAnswerAcrossTheWire(t *testing.T) {
	vars, _ := served(t)
	ctx := context.Background()

	if _, err := vars.SetValue(ctx, &envvarsv1.SetValueRequest{
		Tier:       environmentv1.Tier_TIER_PRODUCTION,
		Coordinate: &envvarsv1.Coordinate{Slug: "platform", Key: "DATABASE_URL"},
		Value:      "postgres://shared",
	}); err != nil {
		t.Fatal(err)
	}

	set, err := vars.SetReference(ctx, &envvarsv1.SetReferenceRequest{
		Tier:       environmentv1.Tier_TIER_PRODUCTION,
		Coordinate: cell("DATABASE_URL"),
		Target:     &envvarsv1.Coordinate{Slug: "platform", Key: "DATABASE_URL"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if set.GetMetadata().GetTarget().GetSlug() != "platform" {
		t.Fatalf("SetReference() = %+v, want the target reported back", set.GetMetadata())
	}

	got, err := vars.GetValue(ctx, &envvarsv1.GetValueRequest{
		Tier:       environmentv1.Tier_TIER_PRODUCTION,
		Coordinate: cell("DATABASE_URL"),
		Reveal:     true,
	})
	if err != nil || got.GetValue() != "postgres://shared" {
		t.Fatalf("GetValue() through a reference = %q, %v", got.GetValue(), err)
	}

	found, err := vars.ListReferences(ctx, &envvarsv1.ListReferencesRequest{
		Tier:       environmentv1.Tier_TIER_PRODUCTION,
		Coordinate: &envvarsv1.Coordinate{Slug: "platform", Key: "DATABASE_URL"},
	})
	if err != nil || len(found.GetReferences()) != 1 || found.GetReferences()[0].GetSlug() != slug {
		t.Fatalf("ListReferences() = %+v, %v", found.GetReferences(), err)
	}

	_, err = vars.SetValue(ctx, &envvarsv1.SetValueRequest{
		Tier:       environmentv1.Tier_TIER_PRODUCTION,
		Coordinate: cell("DATABASE_URL"),
		Value:      "postgres://mine",
	})
	if got := connect.CodeOf(err); got != connect.CodeInvalidArgument {
		t.Fatalf("SetValue() over a reference: code = %v, want %v", got, connect.CodeInvalidArgument)
	}

	_, err = vars.SetReference(ctx, &envvarsv1.SetReferenceRequest{
		Tier:       environmentv1.Tier_TIER_PRODUCTION,
		Coordinate: &envvarsv1.Coordinate{Slug: "web", Key: "DATABASE_URL"},
		Target:     &envvarsv1.Coordinate{Slug: slug, Key: "DATABASE_URL"},
	})
	if got := connect.CodeOf(err); got != connect.CodeInvalidArgument {
		t.Fatalf("a reference to a reference: code = %v, want %v", got, connect.CodeInvalidArgument)
	}
}

func TestTheEnvironmentGateRefusesWhatNothingWouldRead(t *testing.T) {
	vars, provider := served(t)
	ctx := context.Background()

	named := &envvarsv1.Coordinate{Slug: slug, Key: "KEY", Environment: "pr-7"}

	_, err := vars.SetValue(ctx, &envvarsv1.SetValueRequest{
		Tier:       environmentv1.Tier_TIER_PRODUCTION,
		Coordinate: named,
		Value:      "one",
	})
	if got := connect.CodeOf(err); got != connect.CodeInvalidArgument {
		t.Fatalf("a named environment in production: code = %v, want %v", got, connect.CodeInvalidArgument)
	}

	_, err = vars.SetValue(ctx, &envvarsv1.SetValueRequest{
		Tier:       environmentv1.Tier_TIER_PREVIEW,
		Coordinate: named,
		Value:      "one",
	})
	if got := connect.CodeOf(err); got != connect.CodeFailedPrecondition {
		t.Fatalf("a preview environment nobody deployed: code = %v, want %v", got, connect.CodeFailedPrecondition)
	}

	deployPreview(t, provider, "pr-7")

	if _, err := vars.SetValue(ctx, &envvarsv1.SetValueRequest{
		Tier:       environmentv1.Tier_TIER_PREVIEW,
		Coordinate: named,
		Value:      "one",
	}); err != nil {
		t.Fatalf("SetValue() for a deployed preview environment = %v, want it written", err)
	}

	_, err = vars.SetValue(ctx, &envvarsv1.SetValueRequest{
		Tier:       environmentv1.Tier_TIER_PREVIEW,
		Coordinate: &envvarsv1.Coordinate{Slug: slug, Key: "KEY", Environment: "pr-9"},
		Value:      "one",
	})
	if got := connect.CodeOf(err); got != connect.CodeFailedPrecondition {
		t.Fatalf("an environment beside the deployed one: code = %v, want %v", got, connect.CodeFailedPrecondition)
	}
	if !strings.Contains(err.Error(), "pr-7") {
		t.Fatalf("the refusal does not name the environments that do exist: %v", err)
	}
}

func TestLinksAnswerAcrossTheWire(t *testing.T) {
	vars, _ := served(t)
	ctx := context.Background()

	link := &linksv1.Link{
		Name:   "db",
		Source: "neon",
		Properties: &linksv1.Link_Postgres{Postgres: &linksv1.PostgresProperties{
			Host:     "db.example",
			Port:     5432,
			Database: "shop",
			Username: "shop",
			Password: "hunter2",
		}},
	}

	set, err := vars.SetLink(ctx, &envvarsv1.SetLinkRequest{
		Slug:  slug,
		Tier:  environmentv1.Tier_TIER_PRODUCTION,
		Link:  link,
		Owner: "neon",
	})
	if err != nil || set.GetVersion() != 1 {
		t.Fatalf("SetLink() = %+v, %v", set, err)
	}

	listed, err := vars.ListLinks(ctx, &envvarsv1.ListLinksRequest{
		Slug: slug,
		Tier: environmentv1.Tier_TIER_PRODUCTION,
	})
	if err != nil || len(listed.GetLinks()) != 1 {
		t.Fatalf("ListLinks() = %+v, %v", listed.GetLinks(), err)
	}
	summary := listed.GetLinks()[0]
	if summary.GetName() != "db" || summary.GetOwner() != "neon" || summary.GetSource() != "neon" {
		t.Fatalf("ListLinks() summary = %+v", summary)
	}
	if summary.GetType() != linksv1.LinkType_LINK_TYPE_POSTGRES {
		t.Fatalf("ListLinks() type = %v, want the postgres it published", summary.GetType())
	}
	if len(summary.GetProperties()) == 0 {
		t.Fatal("ListLinks() reported no property shapes, and a consumer binds against them")
	}

	_, err = vars.SetLink(ctx, &envvarsv1.SetLinkRequest{
		Slug:  slug,
		Tier:  environmentv1.Tier_TIER_PRODUCTION,
		Link:  link,
		Owner: "supabase",
	})
	if got := connect.CodeOf(err); got != connect.CodeFailedPrecondition {
		t.Fatalf("a second publisher taking the name: code = %v, want %v", got, connect.CodeFailedPrecondition)
	}

	removed, err := vars.RemoveLink(ctx, &envvarsv1.RemoveLinkRequest{
		Slug: slug,
		Tier: environmentv1.Tier_TIER_PRODUCTION,
		Name: "db",
	})
	if err != nil || !removed.GetRemoved() {
		t.Fatalf("RemoveLink() = %+v, %v", removed, err)
	}
	listed, err = vars.ListLinks(ctx, &envvarsv1.ListLinksRequest{
		Slug: slug,
		Tier: environmentv1.Tier_TIER_PRODUCTION,
	})
	if err != nil || len(listed.GetLinks()) != 0 {
		t.Fatalf("ListLinks() after RemoveLink() = %+v, %v", listed.GetLinks(), err)
	}
}

func TestALinkOcelCouldNotHaveProducedIsRefused(t *testing.T) {
	vars, _ := served(t)
	ctx := context.Background()

	for name, link := range map[string]*linksv1.Link{
		"unsourced": {
			Name:       "db",
			Properties: &linksv1.Link_Postgres{Postgres: &linksv1.PostgresProperties{Host: "db.example"}},
		},
		"granting a whole service": {
			Name:       "files",
			Source:     "acme",
			Properties: &linksv1.Link_Bucket{Bucket: &linksv1.BucketProperties{Bucket: "files"}},
			Grants:     []*linksv1.Grant{{Actions: []string{"s3:*"}, Resources: []string{"arn:aws:s3:::files"}}},
		},
		"granting every resource": {
			Name:       "files",
			Source:     "acme",
			Properties: &linksv1.Link_Bucket{Bucket: &linksv1.BucketProperties{Bucket: "files"}},
			Grants:     []*linksv1.Grant{{Actions: []string{"s3:GetObject"}, Resources: []string{"*"}}},
		},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := vars.SetLink(ctx, &envvarsv1.SetLinkRequest{
				Slug:  slug,
				Tier:  environmentv1.Tier_TIER_PRODUCTION,
				Link:  link,
				Owner: "acme",
			})
			if got := connect.CodeOf(err); got != connect.CodeInvalidArgument {
				t.Fatalf("SetLink(): code = %v, want %v", got, connect.CodeInvalidArgument)
			}
		})
	}
}

func TestALinkNamesAnEnvironmentOnlyInPreview(t *testing.T) {
	vars, _ := served(t)

	_, err := vars.ListLinks(context.Background(), &envvarsv1.ListLinksRequest{
		Slug:        slug,
		Tier:        environmentv1.Tier_TIER_PRODUCTION,
		Environment: "pr-7",
	})
	if got := connect.CodeOf(err); got != connect.CodeInvalidArgument {
		t.Fatalf("a named environment in production: code = %v, want %v", got, connect.CodeInvalidArgument)
	}
}

func TestEveryValueRPCRefusesBeforeConfigure(t *testing.T) {
	spec := providerkit.Spec{
		Version: "test",
		New: func(context.Context, providerkit.Options) (providerkit.Provider, error) {
			return fake.NewProvider(fake.Options{}), nil
		},
	}
	const token = "a-token"
	server := httptest.NewServer(providerkit.NewMux(spec, token))
	t.Cleanup(server.Close)

	vars := envvarsv1connect.NewEnvVarsServiceClient(server.Client(), server.URL, connect.WithInterceptors(
		connect.UnaryInterceptorFunc(func(next connect.UnaryFunc) connect.UnaryFunc {
			return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
				req.Header().Set("Authorization", channel.FormatAuthHeader(token))
				return next(ctx, req)
			}
		})))

	ctx := context.Background()
	calls := map[string]func() error{
		"SetValue": func() error {
			_, err := vars.SetValue(ctx, &envvarsv1.SetValueRequest{Coordinate: cell("KEY")})
			return err
		},
		"ListValues": func() error {
			_, err := vars.ListValues(ctx, &envvarsv1.ListValuesRequest{Slug: slug})
			return err
		},
		"GetValue": func() error {
			_, err := vars.GetValue(ctx, &envvarsv1.GetValueRequest{Coordinate: cell("KEY")})
			return err
		},
		"RevealValues": func() error {
			_, err := vars.RevealValues(ctx, &envvarsv1.RevealValuesRequest{Slug: slug})
			return err
		},
		"DeleteValue": func() error {
			_, err := vars.DeleteValue(ctx, &envvarsv1.DeleteValueRequest{Coordinate: cell("KEY")})
			return err
		},
		"SetReference": func() error {
			_, err := vars.SetReference(ctx, &envvarsv1.SetReferenceRequest{Coordinate: cell("KEY"), Target: cell("OTHER")})
			return err
		},
		"ListReferences": func() error {
			_, err := vars.ListReferences(ctx, &envvarsv1.ListReferencesRequest{Coordinate: cell("KEY")})
			return err
		},
		"ListVersions": func() error {
			_, err := vars.ListVersions(ctx, &envvarsv1.ListVersionsRequest{Coordinate: cell("KEY")})
			return err
		},
		"SetLink": func() error {
			_, err := vars.SetLink(ctx, &envvarsv1.SetLinkRequest{
				Slug:  slug,
				Tier:  environmentv1.Tier_TIER_PRODUCTION,
				Owner: "acme",
				Link: &linksv1.Link{
					Name:       "db",
					Source:     "acme",
					Properties: &linksv1.Link_Bucket{Bucket: &linksv1.BucketProperties{Bucket: "files"}},
				},
			})
			return err
		},
		"RemoveLink": func() error {
			_, err := vars.RemoveLink(ctx, &envvarsv1.RemoveLinkRequest{Slug: slug, Tier: environmentv1.Tier_TIER_PRODUCTION, Name: "db"})
			return err
		},
		"ListLinks": func() error {
			_, err := vars.ListLinks(ctx, &envvarsv1.ListLinksRequest{Slug: slug, Tier: environmentv1.Tier_TIER_PRODUCTION})
			return err
		},
	}
	if len(calls) != 11 {
		t.Fatalf("the suite drives %d of EnvVarsService's 11 RPCs", len(calls))
	}
	for name, call := range calls {
		t.Run(name, func(t *testing.T) {
			if got := connect.CodeOf(call()); got != connect.CodeFailedPrecondition {
				t.Fatalf("%s before Configure: code = %v, want %v", name, got, connect.CodeFailedPrecondition)
			}
		})
	}
}

func deployPreview(t *testing.T, provider *fake.Provider, environment string) {
	t.Helper()
	name := providerkit.StackRecord(providerkit.ClassPreview, slug, naming.InfraStack(environment))
	if _, err := provider.Records().Write(context.Background(), providerkit.Record{Name: name, Bytes: []byte("{}")}); err != nil {
		t.Fatalf("record a deployed preview environment: %v", err)
	}
}
