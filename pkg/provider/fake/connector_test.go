package fake_test

import (
	"context"
	"errors"
	"testing"

	"connectrpc.com/connect"

	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/fake"
)

func TestTheFakeRunsNoConnectorUntilATestGivesItATarget(t *testing.T) {
	t.Parallel()

	p := fake.NewProvider(fake.Options{})
	if _, err := p.Connector().Target(context.Background()); connect.CodeOf(err) != connect.CodeUnimplemented {
		t.Fatalf("Target err = %v, want unimplemented before a target is given", err)
	}

	p.FakeConnector().Runs(provider.ConnectorTarget{Fingerprint: "fake/box", Hostname: "box.example.com", OS: "linux", Arch: "amd64"})
	at, err := p.Connector().Install(context.Background(), provider.ConnectorInstall{Binary: []byte("connector")}, nil)
	if err != nil {
		t.Fatalf("Install err = %v", err)
	}
	if at.URL != "https://box.example.com/.ocel/connector" || at.Compute != provider.ComputeContainer {
		t.Errorf("address = %+v, want the box's connector path on container compute", at)
	}
	if installed := p.FakeConnector().Installed(); len(installed) != 1 || string(installed[0].Binary) != "connector" {
		t.Errorf("installed = %+v, want the one binary handed over", installed)
	}
	if err := p.Connector().Remove(context.Background(), nil); err != nil || p.FakeConnector().Removals() != 1 {
		t.Errorf("Remove err = %v, removals = %d, want one removal", err, p.FakeConnector().Removals())
	}
}

func TestARefusingConnectorTargetRefusesEveryCall(t *testing.T) {
	t.Parallel()

	p := fake.NewProvider(fake.Options{})
	p.FakeConnector().Runs(provider.ConnectorTarget{Fingerprint: "fake/box"})
	unreachable := errors.New("this machine is not reachable")
	p.FakeConnector().Refuse(unreachable)

	if _, err := p.Connector().Target(context.Background()); !errors.Is(err, unreachable) {
		t.Errorf("Target err = %v, want the refusal", err)
	}
	if err := p.Connector().Remove(context.Background(), nil); !errors.Is(err, unreachable) || p.FakeConnector().Removals() != 0 {
		t.Errorf("Remove err = %v, removals = %d, want the refusal and nothing removed", err, p.FakeConnector().Removals())
	}
}

func TestTheFakeConnectorRunsOnTheComputeItIsToldItHandsOut(t *testing.T) {
	t.Parallel()

	p := fake.NewProvider(fake.Options{})
	p.FakeConnector().Runs(provider.ConnectorTarget{Fingerprint: "fake/box", Hostname: "box.example.com"})
	if _, err := p.Connector().Install(context.Background(), provider.ConnectorInstall{Compute: provider.ComputeServerless}, nil); err == nil {
		t.Fatal("Install on serverless err = nil, want it refused while the target hands out container compute alone")
	}

	p.FakeConnector().RunsOn(provider.ComputeContainer, provider.ComputeServerless)
	at, err := p.Connector().Install(context.Background(), provider.ConnectorInstall{Compute: provider.ComputeServerless}, nil)
	if err != nil || at.Compute != provider.ComputeServerless {
		t.Errorf("Install on serverless = %+v, %v, want the connector on serverless compute", at, err)
	}
}
