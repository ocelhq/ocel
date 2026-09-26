package clitest

import (
	"context"
	"encoding/json"
	"errors"
	"os"

	connect "connectrpc.com/connect"

	"github.com/ocelhq/ocel/pkg/constants"
	progressv1 "github.com/ocelhq/ocel/pkg/proto/common/progress/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/providerkit/provider"
)

const (
	FakeConnectorTargetEnvVar = "OCEL_TEST_FAKE_CONNECTOR_TARGET"
	FakeConnectorHostEnvVar   = "OCEL_TEST_FAKE_CONNECTOR_HOST"
	FakeConnectorArchEnvVar   = "OCEL_TEST_FAKE_CONNECTOR_ARCH"
	FakeConnectorLogEnvVar    = "OCEL_TEST_FAKE_CONNECTOR_LOG"
	FakeConnectorRefuseEnvVar = "OCEL_TEST_FAKE_CONNECTOR_REFUSE"
)

type FakeConnectorLog struct {
	Installed  bool   `json:"installed"`
	Removed    bool   `json:"removed"`
	Binary     []byte `json:"binary"`
	Version    string `json:"version"`
	ConfigJSON []byte `json:"configJson"`
	Compute    string `json:"compute"`
}

func LoadFakeConnectorLog(path string) (FakeConnectorLog, error) {
	var held FakeConnectorLog
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return held, nil
	}
	if err != nil {
		return held, err
	}
	return held, json.Unmarshal(raw, &held)
}

func recordFakeConnector(change func(*FakeConnectorLog)) error {
	path := os.Getenv(FakeConnectorLogEnvVar)
	if path == "" {
		return nil
	}
	held, err := LoadFakeConnectorLog(path)
	if err != nil {
		return err
	}
	change(&held)
	raw, err := json.Marshal(held)
	if err != nil {
		return err
	}
	return os.WriteFile(path, raw, 0o600)
}

func (s *deployFakeProviderServer) DescribeConnectorTarget(context.Context, *contractv1.DescribeConnectorTargetRequest) (*contractv1.DescribeConnectorTargetResponse, error) {
	if why := os.Getenv(FakeConnectorRefuseEnvVar); why != "" {
		return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New(why))
	}
	return &contractv1.DescribeConnectorTargetResponse{
		TargetFingerprint: os.Getenv(FakeConnectorTargetEnvVar),
		Hostname:          os.Getenv(FakeConnectorHostEnvVar),
		Arch:              os.Getenv(FakeConnectorArchEnvVar),
	}, nil
}

func (s *deployFakeProviderServer) InstallConnector(_ context.Context, req *contractv1.InstallConnectorRequest, stream *connect.ServerStream[progressv1.OperationEvent]) error {
	if err := recordFakeConnector(func(held *FakeConnectorLog) {
		held.Installed, held.Binary, held.Version, held.ConfigJSON = true, req.GetBinary(), req.GetVersion(), req.GetConfigJson()
		held.Compute = req.GetCompute()
	}); err != nil {
		return err
	}
	if err := declareFakeStages(stream); err != nil {
		return err
	}
	if err := stream.Send(&progressv1.OperationEvent{Event: &progressv1.OperationEvent_Progress{
		Progress: &progressv1.ProgressEvent{Message: "wrote the connector", StageId: fakePhaseID},
	}}); err != nil {
		return err
	}
	return stream.Send(&progressv1.OperationEvent{Event: &progressv1.OperationEvent_Result{
		Result: &progressv1.ResultEvent{Success: true, Connector: &progressv1.ConnectorInstalled{
			Url:       "https://" + os.Getenv(FakeConnectorHostEnvVar) + "/" + constants.ProjectStateDirName + "/connector",
			PublicKey: "ZmFrZS1jb25uZWN0b3Ita2V5",
			Compute:   fakeCompute(req.GetCompute()),
		}},
	}})
}

func (s *deployFakeProviderServer) RemoveConnector(_ context.Context, _ *contractv1.RemoveConnectorRequest, stream *connect.ServerStream[progressv1.OperationEvent]) error {
	if err := recordFakeConnector(func(held *FakeConnectorLog) { held.Removed = true }); err != nil {
		return err
	}
	if err := declareFakeStages(stream); err != nil {
		return err
	}
	return stream.Send(&progressv1.OperationEvent{Event: &progressv1.OperationEvent_Result{
		Result: &progressv1.ResultEvent{Success: true},
	}})
}

func fakeCompute(asked string) string {
	if asked == "" {
		return string(provider.ComputeContainer)
	}
	return asked
}
