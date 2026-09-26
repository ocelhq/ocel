package providerserver_test

import (
	"context"
	"net/http/httptest"
	"testing"

	connect "connectrpc.com/connect"

	"github.com/ocelhq/ocel/pkg/constants"
	progressv1 "github.com/ocelhq/ocel/pkg/proto/common/progress/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/proto/provider/contract/v1/contractv1connect"
	"github.com/ocelhq/ocel/pkg/providerkit/fake"
	"github.com/ocelhq/ocel/pkg/providerkit/provider"
	"github.com/ocelhq/ocel/pkg/providerkit/providerserver"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

type connectorHost struct {
	*fake.Provider

	install provider.ConnectorInstall
	removed bool
}

func (h *connectorHost) Connector() provider.Connector { return h }

func (h *connectorHost) Target(context.Context) (provider.ConnectorTarget, error) {
	return provider.ConnectorTarget{
		Fingerprint: "vps/SHA256:AAAA/ocel",
		Hostname:    "box.example.com",
		Arch:        "arm64",
		Installed:   &provider.ConnectorRelease{Version: "0.4.1", PublicKey: "ZmFrZQ==", Compute: provider.ComputeContainer},
	}, nil
}

func (h *connectorHost) Install(_ context.Context, install provider.ConnectorInstall, progress edge.Progress) (provider.ConnectorAddress, error) {
	h.install = install
	progress.Say("wrote the connector")
	compute := install.Compute
	if compute == "" {
		compute = provider.ComputeContainer
	}
	return provider.ConnectorAddress{
		URL:       "https://box.example.com/" + constants.ProjectStateDirName + "/connector",
		PublicKey: "ZmFrZQ==",
		Compute:   compute,
	}, nil
}

func (h *connectorHost) Remove(context.Context, edge.Progress) error {
	h.removed = true
	return nil
}

func connectorServing(t *testing.T, p provider.Provider) contractv1connect.ProviderServiceClient {
	t.Helper()

	server := httptest.NewServer(providerserver.ConformanceMux(providerserver.Config{
		Version: "test",
		New:     func(context.Context, provider.Settings) (provider.Provider, error) { return p, nil },
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
		if got := stream.Msg().GetResult(); got != nil {
			result = got
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

func TestDescribingAConnectorTargetIncludesWhatTheConsoleKeysItBy(t *testing.T) {
	host := &connectorHost{Provider: fake.NewProvider(fake.Options{})}
	client := connectorServing(t, host)

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
		t.Errorf("installed = %v, want the version and key installed on the target", described.GetInstalled())
	}
}

func TestInstallingAConnectorEndsWithTheAddressAndTheKey(t *testing.T) {
	host := &connectorHost{Provider: fake.NewProvider(fake.Options{})}
	client := connectorServing(t, host)

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
	if string(host.install.Binary) != "#!/bin/sh\n" || host.install.Version != "0.4.1" {
		t.Errorf("the provider was handed %+v", host.install)
	}
	if string(host.install.Config) != `{"console":"https://ocel.app"}` {
		t.Errorf("the provider was handed the config %q", host.install.Config)
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
	host := &connectorHost{Provider: fake.NewProvider(fake.Options{})}
	client := connectorServing(t, host)

	err := closed(client.InstallConnector(context.Background(), &contractv1.InstallConnectorRequest{}))
	if connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Errorf("InstallConnector() with nothing to install = %v, want invalid argument", err)
	}
	if host.install.Version != "" {
		t.Error("the provider was handed an install with no binary")
	}
}

func TestRemovingAConnectorReachesTheProvider(t *testing.T) {
	host := &connectorHost{Provider: fake.NewProvider(fake.Options{})}
	client := connectorServing(t, host)

	stream, err := client.RemoveConnector(context.Background(), &contractv1.RemoveConnectorRequest{})
	if result := finalResult(t, stream, err); !result.GetSuccess() {
		t.Fatalf("result error = %q, want a success", result.GetError())
	}
	if !host.removed {
		t.Error("the provider was never asked to take the connector off")
	}
}

func TestTheComputeTheCallerAsksForReachesTheProviderAndItsChoiceComesBack(t *testing.T) {
	host := &connectorHost{Provider: fake.NewProvider(fake.Options{})}
	client := connectorServing(t, host)

	stream, err := client.InstallConnector(context.Background(), &contractv1.InstallConnectorRequest{
		Binary: []byte("#!/bin/sh\n"), Version: "0.4.1", ConfigJson: []byte(`{"console":"https://ocel.app"}`),
		Compute: string(provider.ComputeServerless),
	})
	result := finalResult(t, stream, err)
	if host.install.Compute != provider.ComputeServerless {
		t.Errorf("the provider was asked for %q, want the compute the caller named", host.install.Compute)
	}
	if result.GetConnector().GetCompute() != string(provider.ComputeServerless) {
		t.Errorf("compute = %q, want what the provider chose so the console records it", result.GetConnector().GetCompute())
	}
}

func TestAnUnsetComputeLeavesTheChoiceToTheProvider(t *testing.T) {
	host := &connectorHost{Provider: fake.NewProvider(fake.Options{})}
	client := connectorServing(t, host)

	stream, err := client.InstallConnector(context.Background(), &contractv1.InstallConnectorRequest{
		Binary: []byte("#!/bin/sh\n"), Version: "0.4.1", ConfigJson: []byte(`{"console":"https://ocel.app"}`),
	})
	result := finalResult(t, stream, err)
	if host.install.Compute != "" {
		t.Errorf("the provider was asked for %q, want nothing named so it picks its own default", host.install.Compute)
	}
	if result.GetConnector().GetCompute() != string(provider.ComputeContainer) {
		t.Errorf("compute = %q, want the provider's own default passed back", result.GetConnector().GetCompute())
	}
}

func TestAComputeOutsideTheVocabularyIsRefusedBeforeTheProviderSeesIt(t *testing.T) {
	host := &connectorHost{Provider: fake.NewProvider(fake.Options{})}
	client := connectorServing(t, host)

	err := closed(client.InstallConnector(context.Background(), &contractv1.InstallConnectorRequest{
		Binary: []byte("#!/bin/sh\n"), Version: "0.4.1", ConfigJson: []byte(`{"console":"https://ocel.app"}`),
		Compute: "vm",
	}))
	if connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Errorf("InstallConnector() naming a compute outside the wire pin = %v, want invalid argument", err)
	}
	if host.install.Version != "" {
		t.Error("the provider was handed an install naming a compute the wire does not admit")
	}
}
