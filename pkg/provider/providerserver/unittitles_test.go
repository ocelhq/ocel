package providerserver_test

import (
	"slices"
	"testing"

	connect "connectrpc.com/connect"

	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"
	progressv1 "github.com/ocelhq/ocel/pkg/proto/common/progress/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/proto/provider/contract/v1/contractv1connect"
	"github.com/ocelhq/ocel/pkg/provider/fake"
)

type openedUnit struct{ subject, message string }

func openedUnits(events []*progressv1.OperationEvent) []openedUnit {
	var units []openedUnit
	for _, event := range events {
		if started := event.GetStarted(); started != nil && len(started.GetParentSpanId()) == 0 {
			units = append(units, openedUnit{subject: event.GetSubject(), message: event.GetMessage()})
		}
	}
	return units
}

func TestEveryUnitAProductionDeployOpensNamesWhichThingItWorksOnAndWhatItDoes(t *testing.T) {
	builtProject(t)
	client, _ := deployServed(t)

	result, events := deploy(t, client, deployRequest())
	if result == nil || !result.GetSuccess() {
		t.Fatalf("Deploy() = %q, want it to succeed", result.GetError())
	}
	want := []openedUnit{
		{"production", "Checking the bootstrap, domains and bindings for shop"},
		{"relay", "Reconciling the routes for shop in production"},
		{"production", "Provisioning shared resource orders"},
		{"web", "Deploying the serverless app to production"},
		{"relay", "Attaching production hostname shop.example"},
		{"production", "Switching traffic to promotion " + result.GetPromotionId()},
	}
	if got := openedUnits(events); !slices.Equal(got, want) {
		t.Errorf("the deploy opened units\n%v\nwant\n%v", got, want)
	}
}

func TestEveryUnitADryRunOpensSaysItOnlyReadsOrPlans(t *testing.T) {
	builtProject(t)
	client, _ := deployServed(t)
	req := deployRequest()
	req.Dry = true

	result, events := deploy(t, client, req)
	if result == nil || !result.GetSuccess() {
		t.Fatalf("Deploy() = %q, want it to succeed", result.GetError())
	}
	want := []openedUnit{
		{"production", "Checking the bootstrap, domains and bindings for shop"},
		{"relay", "Reading the routes for shop in production"},
		{"production", "Planning shared resource orders"},
		{"web", "Planning the serverless app for production"},
	}
	if got := openedUnits(events); !slices.Equal(got, want) {
		t.Errorf("the dry run opened units\n%v\nwant\n%v", got, want)
	}
}

func TestAPreviewDeploysUnitsNameThePreviewTheyDeployTo(t *testing.T) {
	builtProject(t)
	client, _ := deployServed(t)
	previewBootstrapped(t, client)

	result, events := deploy(t, client, previewRequest())
	if result == nil || !result.GetSuccess() {
		t.Fatalf("Deploy() = %q, want it to succeed", result.GetError())
	}
	want := []openedUnit{
		{"pr-7", "Checking the bootstrap, domains and bindings for shop"},
		{"relay", "Reconciling the routes for shop in preview pr-7"},
		{"pr-7", "Provisioning shared resource orders"},
		{"web", "Deploying the serverless app to preview pr-7"},
		{"pr-7", "Switching traffic to promotion " + result.GetPromotionId()},
	}
	if got := openedUnits(events); !slices.Equal(got, want) {
		t.Errorf("the preview deploy opened units\n%v\nwant\n%v", got, want)
	}
}

func TestEveryUnitALifecycleCallOpensNamesWhichThingItWorksOnAndWhatItDoes(t *testing.T) {
	t.Parallel()
	production := &environmentv1.Environment{Tier: environmentv1.Tier_TIER_PRODUCTION}
	preview := &environmentv1.Environment{Tier: environmentv1.Tier_TIER_PREVIEW, Identity: "pr-7"}
	for _, tc := range []struct {
		name      string
		connector bool
		call      func(contractv1connect.ProviderServiceClient) (*connect.ServerStreamForClient[progressv1.OperationEvent], error)
		want      openedUnit
	}{
		{"a bootstrap", false, func(c contractv1connect.ProviderServiceClient) (*connect.ServerStreamForClient[progressv1.OperationEvent], error) {
			return c.Bootstrap(t.Context(), &contractv1.BootstrapRequest{Tier: environmentv1.Tier_TIER_PRODUCTION})
		}, openedUnit{"production", "Updating bootstrap stacks fake-production, fake-production-cache and fake-production-images"}},
		{"a bootstrap removal", false, func(c contractv1connect.ProviderServiceClient) (*connect.ServerStreamForClient[progressv1.OperationEvent], error) {
			return c.RemoveBootstrap(t.Context(), &contractv1.BootstrapScope{Tier: environmentv1.Tier_TIER_PRODUCTION})
		}, openedUnit{"production", "Removing the bootstrap"}},
		{"a connector install", true, func(c contractv1connect.ProviderServiceClient) (*connect.ServerStreamForClient[progressv1.OperationEvent], error) {
			return c.InstallConnector(t.Context(), &contractv1.InstallConnectorRequest{Binary: []byte("#!/bin/sh\n"), Version: "1.2.0", ConfigJson: []byte("{}"), Compute: "container"})
		}, openedUnit{"connector", "Installing the connector 1.2.0 on container compute in this account"}},
		{"a connector removal", true, func(c contractv1connect.ProviderServiceClient) (*connect.ServerStreamForClient[progressv1.OperationEvent], error) {
			return c.RemoveConnector(t.Context(), &contractv1.RemoveConnectorRequest{})
		}, openedUnit{"connector", "Removing the connector from this account"}},
		{"a preview removal", false, func(c contractv1connect.ProviderServiceClient) (*connect.ServerStreamForClient[progressv1.OperationEvent], error) {
			return c.RemoveEnvironment(t.Context(), &contractv1.RemoveEnvironmentRequest{Slug: "shop", Environment: preview})
		}, openedUnit{"pr-7", "Removing the preview environment of shop"}},
		{"a hostname add", false, func(c contractv1connect.ProviderServiceClient) (*connect.ServerStreamForClient[progressv1.OperationEvent], error) {
			return c.AddHostname(t.Context(), &contractv1.HostnameRequest{Slug: "shop", Configured: configuredHosts("app.acme.com", "www.acme.com")})
		}, openedUnit{"shop", "Attaching production hostnames app.acme.com and www.acme.com"}},
		{"a hostname removal", false, func(c contractv1connect.ProviderServiceClient) (*connect.ServerStreamForClient[progressv1.OperationEvent], error) {
			return c.RemoveHostname(t.Context(), &contractv1.HostnameRequest{Slug: "shop", Host: "old.acme.com"})
		}, openedUnit{"shop", "Detaching old.acme.com from production"}},
		{"a global preview domain", false, func(c contractv1connect.ProviderServiceClient) (*connect.ServerStreamForClient[progressv1.OperationEvent], error) {
			return c.UsePreviewWildcard(t.Context(), &contractv1.UsePreviewWildcardRequest{Tier: environmentv1.Tier_TIER_PREVIEW, BaseDomain: "preview.acme.com"})
		}, openedUnit{"preview", "Serving every project's previews on *.preview.acme.com"}},
		{"a global preview domain release", false, func(c contractv1connect.ProviderServiceClient) (*connect.ServerStreamForClient[progressv1.OperationEvent], error) {
			return c.RemovePreviewWildcard(t.Context(), &contractv1.PreviewWildcardRequest{Tier: environmentv1.Tier_TIER_PREVIEW})
		}, openedUnit{"preview", "Releasing the global preview domain"}},
		{"a production destroy", false, func(c contractv1connect.ProviderServiceClient) (*connect.ServerStreamForClient[progressv1.OperationEvent], error) {
			return c.RemoveProject(t.Context(), &contractv1.ProjectRequest{Slug: "shop", Environment: production})
		}, openedUnit{"shop", "Destroying the production footprint"}},
		{"a preview destroy", false, func(c contractv1connect.ProviderServiceClient) (*connect.ServerStreamForClient[progressv1.OperationEvent], error) {
			return c.RemoveProject(t.Context(), &contractv1.ProjectRequest{Slug: "shop", Environment: preview})
		}, openedUnit{"shop", "Destroying the footprint of preview pr-7"}},
		{"a prune", false, func(c contractv1connect.ProviderServiceClient) (*connect.ServerStreamForClient[progressv1.OperationEvent], error) {
			return c.RemoveStalePromotions(t.Context(), &contractv1.RemoveStalePromotionsRequest{Slug: "shop", Environment: production, KeepN: 3})
		}, openedUnit{"shop", "Pruning the promotions of production beyond the newest 3"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			client, _ := contractServed(t, "1.0.0")
			if tc.connector {
				client = connectorServing(t, &connectorHost{Provider: fake.NewProvider(fake.Options{})})
			}

			stream, err := tc.call(client)
			if err != nil {
				t.Fatalf("the call error = %v", err)
			}
			if got := openedUnits(recorded(stream)); len(got) == 0 || got[0] != tc.want {
				t.Errorf("the call opened units %v, want it to open %v first", got, tc.want)
			}
		})
	}
}

func TestTheRecordsADeployAsksYouToWriteAreAWarningOnTheUnitAttachingTheHostname(t *testing.T) {
	builtProject(t)
	client, _ := deployServed(t)

	_, events := deploy(t, client, deployRequest())
	var asked []*progressv1.OperationEvent
	for _, event := range events {
		if event.GetDnsManualRecords() != nil {
			asked = append(asked, event)
		}
	}
	if len(asked) != 1 {
		t.Fatalf("the deploy asked for records %d times, want once", len(asked))
	}
	event := asked[0]
	if event.GetLevel() != progressv1.Level_LEVEL_WARN {
		t.Errorf("the records are asked for at %v, want WARN: nothing answers at the hostname until you write them", event.GetLevel())
	}
	if event.GetPhase() != progressv1.Phase_PHASE_PROVISION || event.GetSubject() != "relay" {
		t.Errorf("the records are asked for as %q in %v, want the relay edge's hostname unit in the provision phase", event.GetSubject(), event.GetPhase())
	}
	if want := "Point shop.example at the relay edge"; event.GetMessage() != want {
		t.Errorf("the records are asked for with %q, want %q", event.GetMessage(), want)
	}
}
