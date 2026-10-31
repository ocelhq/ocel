package providerserver_test

import (
	"context"
	"net/http/httptest"
	"testing"

	connect "connectrpc.com/connect"

	"github.com/ocelhq/ocel/pkg/environment"
	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/proto/provider/contract/v1/contractv1connect"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/fake"
	"github.com/ocelhq/ocel/pkg/provider/providerserver"
)

func servedFor(t *testing.T, config providerserver.Config) contractv1connect.ProviderServiceClient {
	t.Helper()
	vendor := fake.NewProvider(fake.Options{})
	config.Version = "1.0.0"
	config.New = func(context.Context, provider.Settings) (provider.Provider, error) { return vendor, nil }
	server := httptest.NewServer(providerserver.ConformanceMux(config))
	t.Cleanup(server.Close)
	client := contractv1connect.NewProviderServiceClient(server.Client(), server.URL)
	if _, err := client.Configure(context.Background(), configureInWorkingDir(t)); err != nil {
		t.Fatal(err)
	}
	return client
}

func rollbackOf(client contractv1connect.ProviderServiceClient, slug string) error {
	stream, err := client.Rollback(context.Background(), &contractv1.RollbackRequest{Slug: slug})
	if err != nil {
		return err
	}
	defer stream.Close()
	for stream.Receive() {
	}
	return stream.Err()
}

func TestAServerForOneProjectRefusesARequestNamingAnother(t *testing.T) {
	t.Parallel()
	client := servedFor(t, providerserver.Config{Slug: "shop", ServesSlugAlone: true})

	if _, err := client.ListPromotions(context.Background(), &contractv1.ListPromotionsRequest{Slug: "shop"}); err != nil {
		t.Fatalf("ListPromotions for shop = %v, want it served", err)
	}
	if _, err := client.ListPromotions(context.Background(), &contractv1.ListPromotionsRequest{Slug: "other"}); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Errorf("ListPromotions for other = %v, want it refused", err)
	}
	if err := rollbackOf(client, "other"); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Errorf("Rollback of other = %v, want it refused before it runs", err)
	}
}

func TestAServerForNoProjectRefusesEveryRequestNamingOne(t *testing.T) {
	t.Parallel()
	client := servedFor(t, providerserver.Config{ServesSlugAlone: true})

	if _, err := client.ListProjects(context.Background(), &contractv1.ListProjectsRequest{}); err != nil {
		t.Fatalf("ListProjects = %v, want a call naming no project served", err)
	}
	if _, err := client.ListPromotions(context.Background(), &contractv1.ListPromotionsRequest{Slug: "shop"}); connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Errorf("ListPromotions for shop = %v, want it refused: this session read no project's records", err)
	}
	if err := rollbackOf(client, "shop"); connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Errorf("Rollback of shop = %v, want it refused before it runs", err)
	}
}

func TestAServerForAnyProjectServesEveryProject(t *testing.T) {
	t.Parallel()
	client := servedFor(t, providerserver.Config{Slug: "shop"})

	for _, slug := range []string{"shop", "other"} {
		if _, err := client.ListPromotions(context.Background(), &contractv1.ListPromotionsRequest{Slug: slug}); err != nil {
			t.Errorf("ListPromotions for %s = %v, want it served", slug, err)
		}
	}
}

func TestAServerForOneTierRefusesARequestActingOnTheOther(t *testing.T) {
	t.Parallel()
	preview := servedFor(t, providerserver.Config{Slug: "shop", ServesSlugAlone: true, Tier: environment.TierPreview})
	production := servedFor(t, providerserver.Config{Slug: "shop", ServesSlugAlone: true, Tier: environment.TierProduction})
	protection := &contractv1.GetProtectionRequest{Tier: environmentv1.Tier_TIER_PRODUCTION, Slug: "shop"}

	if _, err := preview.GetProtection(context.Background(), protection); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Errorf("GetProtection for production through a preview server = %v, want it refused", err)
	}
	if _, err := preview.ListPromotions(context.Background(), &contractv1.ListPromotionsRequest{Slug: "shop"}); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Errorf("ListPromotions, which reads production, through a preview server = %v, want it refused", err)
	}
	if err := rollbackOf(preview, "shop"); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Errorf("Rollback, which acts on production, through a preview server = %v, want it refused", err)
	}
	if _, err := production.GetProtection(context.Background(), protection); err != nil {
		t.Errorf("GetProtection for production through a production server = %v, want it served", err)
	}
	if _, err := production.ListPromotions(context.Background(), &contractv1.ListPromotionsRequest{Slug: "shop"}); err != nil {
		t.Errorf("ListPromotions through a production server = %v, want it served", err)
	}
}
