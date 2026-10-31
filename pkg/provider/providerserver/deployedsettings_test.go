package providerserver_test

import (
	"context"
	"net/http/httptest"
	"testing"

	"google.golang.org/protobuf/types/known/structpb"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/proto/provider/contract/v1/contractv1connect"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/fake"
	"github.com/ocelhq/ocel/pkg/provider/providerserver"
)

func deployedWithSettings(t *testing.T) (contractv1connect.ProviderServiceClient, *fake.Provider) {
	t.Helper()
	builtProject(t)
	vendor := fake.NewProvider(fake.Options{Region: "nowhere"})
	server := httptest.NewServer(providerserver.ConformanceMux(providerserver.Config{
		Version: "1.0.0",
		New:     func(context.Context, provider.Settings) (provider.Provider, error) { return vendor, nil },
	}))
	t.Cleanup(server.Close)
	client := contractv1connect.NewProviderServiceClient(server.Client(), server.URL)
	options, err := structpb.NewStruct(map[string]any{"region": "nowhere"})
	if err != nil {
		t.Fatal(err)
	}
	configure := configureInWorkingDir(t)
	configure.Config.Options = options
	if _, err := client.Configure(context.Background(), configure); err != nil {
		t.Fatalf("Configure() error = %v", err)
	}
	bootstrappedOverRPC(t, client)
	if result, _ := deploy(t, client, deployRequest()); !result.GetSuccess() {
		t.Fatalf("Deploy() = %q", result.GetError())
	}
	return client, vendor
}

func TestADeployRecordsTheSettingsItRanWithForItsTierAndProject(t *testing.T) {
	_, vendor := deployedWithSettings(t)

	recorded, found, err := providerserver.ReadDeployedSettings(context.Background(), vendor.KeyValues(), "shop", environment.TierProduction)
	if err != nil || !found || recorded.Revision == "" {
		t.Fatalf("ReadDeployedSettings() = (%v, %v, %v), want the settings shop deployed with at their revision", recorded, found, err)
	}
	settings := recorded.Settings
	if got := settings.GetOptions().AsMap()["region"]; got != "nowhere" {
		t.Errorf("the recorded region = %v, want the option the deploy was configured with", got)
	}
	if settings.GetEdge().GetKind() != string(fake.KindRelay) {
		t.Errorf("the recorded edge = %v, want the %s edge the deploy served through, though the request named none", settings.GetEdge(), fake.KindRelay)
	}

	if _, found, _ := providerserver.ReadDeployedSettings(context.Background(), vendor.KeyValues(), "shop", environment.TierPreview); found {
		t.Error("a production deploy recorded settings for the preview tier")
	}
}

func TestRemovingAProjectForgetsTheSettingsItDeployedWith(t *testing.T) {
	client, vendor := deployedWithSettings(t)

	if result := removeProject(t, client, projectRequest()); !result.GetSuccess() {
		t.Fatalf("RemoveProject() = %q", result.GetError())
	}
	if _, found, err := providerserver.ReadDeployedSettings(context.Background(), vendor.KeyValues(), "shop", environment.TierProduction); err != nil || found {
		t.Errorf("ReadDeployedSettings() after removal = (found %v, %v), want nothing left of shop", found, err)
	}
}
