package providerkit_test

import (
	"context"
	"net/http/httptest"
	"testing"

	connect "connectrpc.com/connect"

	"github.com/ocelhq/ocel/pkg/constants"
	progressv1 "github.com/ocelhq/ocel/pkg/proto/common/progress/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/proto/provider/contract/v1/contractv1connect"
	"github.com/ocelhq/ocel/pkg/providerkit"
	"github.com/ocelhq/ocel/pkg/providerkit/fake"
)

type connectorHost struct {
	*fake.Provider

	install providerkit.ConnectorInstall
	removed bool
}

func (h *connectorHost) Connector() providerkit.Connector { return h }

func (h *connectorHost) Target(context.Context) (providerkit.ConnectorTarget, error) {
	return providerkit.ConnectorTarget{
		Fingerprint: "vps/SHA256:AAAA/ocel",
		Hostname:    "box.example.com",
		Arch:        "arm64",
		Installed:   &providerkit.ConnectorRelease{Version: "0.4.1", PublicKey: "ZmFrZQ==", Compute: providerkit.ComputeContainer},
	}, nil
}

func (h *connectorHost) Install(_ context.Context, install providerkit.ConnectorInstall, progress providerkit.Progress) (providerkit.ConnectorAddress, error) {
	h.install = install
	progress.Say("wrote the connector")
	compute := install.Compute
	if compute == "" {
		compute = providerkit.ComputeContainer
	}
	return providerkit.ConnectorAddress{
		URL:       "https://box.example.com/" + constants.ProjectStateDirName + "/connector",
		PublicKey: "ZmFrZQ==",
		Compute:   compute,
	}, nil
}

func (h *connectorHost) Remove(context.Context, providerkit.Progress) error {
	h.removed = true
	return nil
}

func connectorServing(t *testing.T, provider providerkit.Provider) contractv1connect.ProviderServiceClient {
	t.Helper()

	server := httptest.NewServer(providerkit.ConformanceMux(providerkit.Spec{
		Version: "test",
		New:     func(context.Context, providerkit.Settings) (providerkit.Provider, error) { return provider, nil },
	}))
	t.Cleanup(server.Close)

	client := contractv1connect.NewProviderServiceClient(server.Client(), server.URL)
	if _, err := client.Configure(context.Background(), &contractv1.ConfigureRequest{
		Config: &contractv1.ProviderConfig{},
	}); err != nil {
		t.Fatalf("Configure() error = %v", err)
	}
	return client
}

func finalResult(t *testing.T, stream *connect.ServerStreamForClient[progressv1.OperationEvent], err error) *progressv1.ResultEvent {
	t.Helper()

	if err != nil {
		t.Fatalf("call error = %v", err)
	}
	defer stream.Close()
	var result *progressv1.ResultEvent
	for stream.Receive() {
		if held := stream.Msg().GetResult(); held != nil {
			result = held
		}
	}
	if err := stream.Err(); err != nil {
		t.Fatalf("stream error = %v", err)
	}
	if result == nil {
		t.Fatal("the stream closed without a result")
	}
	return result
}

func TestAProviderThatPutsNoConnectorOnItsTargetsSaysSo(t *testing.T) {
	client := connectorServing(t, fake.NewProvider(fake.Options{}))
	ctx := context.Background()

	if _, err := client.DescribeConnectorTarget(ctx, &contractv1.DescribeConnectorTargetRequest{}); connect.CodeOf(err) != connect.CodeUnimplemented {
		t.Errorf("DescribeConnectorTarget() = %v, want unimplemented", err)
	}
	if err := closed(client.InstallConnector(ctx, &contractv1.InstallConnectorRequest{
		Binary: []byte("x"), Version: "0.1.0", ConfigJson: []byte("{}"),
	})); connect.CodeOf(err) != connect.CodeUnimplemented {
		t.Errorf("InstallConnector() = %v, want unimplemented", err)
	}
	if err := closed(client.RemoveConnector(ctx, &contractv1.RemoveConnectorRequest{})); connect.CodeOf(err) != connect.CodeUnimplemented {
		t.Errorf("RemoveConnector() = %v, want unimplemented", err)
	}
}

func TestDescribingAConnectorTargetCarriesWhatTheConsoleKeysItBy(t *testing.T) {
	held := &connectorHost{Provider: fake.NewProvider(fake.Options{})}
	client := connectorServing(t, held)

	described, err := client.DescribeConnectorTarget(context.Background(), &contractv1.DescribeConnectorTargetRequest{})
	if err != nil {
		t.Fatalf("DescribeConnectorTarget() error = %v", err)
	}
	if described.GetTargetFingerprint() != "vps/SHA256:AAAA/ocel" {
		t.Errorf("fingerprint = %q", described.GetTargetFingerprint())
	}
	if described.GetHostname() != "box.example.com" || described.GetArch() != "arm64" {
		t.Errorf("hostname/arch = %q/%q", described.GetHostname(), described.GetArch())
	}
	if described.GetInstalled().GetVersion() != "0.4.1" || described.GetInstalled().GetPublicKey() != "ZmFrZQ==" {
		t.Errorf("installed = %v, want the version and key the target holds", described.GetInstalled())
	}
}

func TestInstallingAConnectorEndsWithTheAddressAndTheKey(t *testing.T) {
	held := &connectorHost{Provider: fake.NewProvider(fake.Options{})}
	client := connectorServing(t, held)

	stream, err := client.InstallConnector(context.Background(), &contractv1.InstallConnectorRequest{
		Binary: []byte("#!/bin/sh\n"), Version: "0.4.1", ConfigJson: []byte(`{"console":"https://ocel.app"}`),
	})
	result := finalResult(t, stream, err)
	if !result.GetSuccess() {
		t.Fatalf("result error = %q, want a success", result.GetError())
	}
	if result.GetConnector().GetUrl() != "https://box.example.com/"+constants.ProjectStateDirName+"/connector" {
		t.Errorf("url = %q", result.GetConnector().GetUrl())
	}
	if result.GetConnector().GetPublicKey() != "ZmFrZQ==" {
		t.Errorf("public key = %q", result.GetConnector().GetPublicKey())
	}
	if string(held.install.Binary) != "#!/bin/sh\n" || held.install.Version != "0.4.1" {
		t.Errorf("the provider was handed %+v", held.install)
	}
	if string(held.install.Config) != `{"console":"https://ocel.app"}` {
		t.Errorf("the provider was handed the config %q", held.install.Config)
	}
}

func closed(stream *connect.ServerStreamForClient[progressv1.OperationEvent], err error) error {
	if err != nil {
		return err
	}
	defer stream.Close()
	for stream.Receive() {
	}
	return stream.Err()
}

func TestAnEmptyInstallIsRefusedBeforeTheProviderSeesIt(t *testing.T) {
	held := &connectorHost{Provider: fake.NewProvider(fake.Options{})}
	client := connectorServing(t, held)

	err := closed(client.InstallConnector(context.Background(), &contractv1.InstallConnectorRequest{}))
	if connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Errorf("InstallConnector() with nothing to install = %v, want invalid argument", err)
	}
	if held.install.Version != "" {
		t.Error("the provider was handed an install carrying no binary")
	}
}

func TestRemovingAConnectorReachesTheProvider(t *testing.T) {
	held := &connectorHost{Provider: fake.NewProvider(fake.Options{})}
	client := connectorServing(t, held)

	stream, err := client.RemoveConnector(context.Background(), &contractv1.RemoveConnectorRequest{})
	if result := finalResult(t, stream, err); !result.GetSuccess() {
		t.Fatalf("result error = %q, want a success", result.GetError())
	}
	if !held.removed {
		t.Error("the provider was never asked to take the connector off")
	}
}

func TestTheComputeTheCallerAsksForReachesTheProviderAndItsChoiceComesBack(t *testing.T) {
	held := &connectorHost{Provider: fake.NewProvider(fake.Options{})}
	client := connectorServing(t, held)

	stream, err := client.InstallConnector(context.Background(), &contractv1.InstallConnectorRequest{
		Binary: []byte("#!/bin/sh\n"), Version: "0.4.1", ConfigJson: []byte(`{"console":"https://ocel.app"}`),
		Compute: string(providerkit.ComputeServerless),
	})
	result := finalResult(t, stream, err)
	if held.install.Compute != providerkit.ComputeServerless {
		t.Errorf("the provider was asked for %q, want the compute the caller named", held.install.Compute)
	}
	if result.GetConnector().GetCompute() != string(providerkit.ComputeServerless) {
		t.Errorf("compute = %q, want what the provider settled on so the console records it", result.GetConnector().GetCompute())
	}
}

func TestAnUnsetComputeLeavesTheChoiceToTheProvider(t *testing.T) {
	held := &connectorHost{Provider: fake.NewProvider(fake.Options{})}
	client := connectorServing(t, held)

	stream, err := client.InstallConnector(context.Background(), &contractv1.InstallConnectorRequest{
		Binary: []byte("#!/bin/sh\n"), Version: "0.4.1", ConfigJson: []byte(`{"console":"https://ocel.app"}`),
	})
	result := finalResult(t, stream, err)
	if held.install.Compute != "" {
		t.Errorf("the provider was asked for %q, want nothing named so it picks its own default", held.install.Compute)
	}
	if result.GetConnector().GetCompute() != string(providerkit.ComputeContainer) {
		t.Errorf("compute = %q, want the provider's own default carried back", result.GetConnector().GetCompute())
	}
}

func TestAComputeOutsideTheVocabularyIsRefusedBeforeTheProviderSeesIt(t *testing.T) {
	held := &connectorHost{Provider: fake.NewProvider(fake.Options{})}
	client := connectorServing(t, held)

	err := closed(client.InstallConnector(context.Background(), &contractv1.InstallConnectorRequest{
		Binary: []byte("#!/bin/sh\n"), Version: "0.4.1", ConfigJson: []byte(`{"console":"https://ocel.app"}`),
		Compute: "vm",
	}))
	if connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Errorf("InstallConnector() naming a compute outside the wire pin = %v, want invalid argument", err)
	}
	if held.install.Version != "" {
		t.Error("the provider was handed an install naming a compute the wire does not admit")
	}
}
