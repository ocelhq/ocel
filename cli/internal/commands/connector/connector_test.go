package connector

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	connectvalidate "connectrpc.com/validate"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/ocelhq/ocel/cli/internal/clierror"
	"github.com/ocelhq/ocel/cli/internal/clitest"
	"github.com/ocelhq/ocel/cli/internal/console"
	"github.com/ocelhq/ocel/cli/internal/executables"
	"github.com/ocelhq/ocel/cli/internal/exitcode"
	"github.com/ocelhq/ocel/cli/internal/project"
	"github.com/ocelhq/ocel/cli/internal/providerprocess"
	resultv1 "github.com/ocelhq/ocel/pkg/proto/cli/result/v1"
	streamv1 "github.com/ocelhq/ocel/pkg/proto/cli/stream/v1"
	progressv1 "github.com/ocelhq/ocel/pkg/proto/common/progress/v1"
	consolev1 "github.com/ocelhq/ocel/pkg/proto/console/v1"
	"github.com/ocelhq/ocel/pkg/proto/console/v1/consolev1connect"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/pkg/statedir"
)

const (
	fingerprint  = "fake/sha256:aaaa/ocel"
	hostname     = "box.example.com"
	registeredID = "0199b5c4-0000-7000-8000-00000000cafe"
)

type consoleServer struct {
	*httptest.Server
	consolev1connect.UnimplementedConnectorServiceHandler
	rows          []*consolev1.Connector
	upserted      []*consolev1.UpsertConnectorRequest
	addressed     []*consolev1.SetConnectorAddressRequest
	deleted       []string
	refuseAddress bool
}

func newConsoleServer(t *testing.T, rows ...*consolev1.Connector) *consoleServer {
	t.Helper()

	c := &consoleServer{rows: rows}
	mux := http.NewServeMux()
	path, handler := consolev1connect.NewConnectorServiceHandler(c, connect.WithInterceptors(connectvalidate.NewInterceptor()))
	mux.Handle("/api/connect"+path, http.StripPrefix("/api/connect", handler))
	c.Server = httptest.NewServer(mux)
	t.Cleanup(c.Close)
	return c
}

func (c *consoleServer) List(context.Context, *consolev1.ListConnectorsRequest) (*consolev1.ListConnectorsResponse, error) {
	return &consolev1.ListConnectorsResponse{Connectors: c.rows}, nil
}

func (c *consoleServer) Upsert(_ context.Context, req *consolev1.UpsertConnectorRequest) (*consolev1.UpsertConnectorResponse, error) {
	c.upserted = append(c.upserted, req)
	registered := &consolev1.Connector{
		Id: registeredID, OrganizationId: "org_1", Target: req.GetTarget(), Vendor: req.GetVendor(), Reach: req.GetReach(),
	}
	c.rows = append(c.rows, registered)
	return &consolev1.UpsertConnectorResponse{Connector: registered}, nil
}

func (c *consoleServer) SetAddress(_ context.Context, req *consolev1.SetConnectorAddressRequest) (*consolev1.SetConnectorAddressResponse, error) {
	if c.refuseAddress {
		return nil, connect.NewError(connect.CodeInternal, errors.New("nope"))
	}
	c.addressed = append(c.addressed, req)
	return &consolev1.SetConnectorAddressResponse{Connector: &consolev1.Connector{
		Id: req.GetId(), Target: fingerprint, Vendor: "fake", Url: proto.String(req.GetUrl()),
		Compute: consolev1.ComputeKind_COMPUTE_KIND_CONTAINER, Reach: consolev1.ConnectorReach_CONNECTOR_REACH_DIAL,
	}}, nil
}

func (c *consoleServer) Remove(_ context.Context, req *consolev1.RemoveConnectorRequest) (*consolev1.RemoveConnectorResponse, error) {
	c.deleted = append(c.deleted, req.GetId())
	return &consolev1.RemoveConnectorResponse{}, nil
}

func linked(t *testing.T, dir, apiURL string) {
	t.Helper()

	if err := console.WriteLink(dir, console.Link{
		APIURL:         apiURL,
		OrganizationID: "org_1",
		ProjectID:      "p1",
		ProjectName:    "My App",
	}); err != nil {
		t.Fatalf("consolelink.Write: %v", err)
	}
}

func resolved(t *testing.T, root string) *project.Project {
	t.Helper()

	cfg, err := project.Load(context.Background(), root, filepath.Join(root, "ocel.fake.json"))
	if err != nil {
		t.Fatalf("project.Resolve: %v", err)
	}
	return cfg
}

func opened(t *testing.T, srv *consoleServer) options {
	t.Helper()

	return options{write: true, apiURL: srv.URL, console: console.New(srv.URL)}
}

func TestAddPairsTheTargetWithTheConsoleAndInstallsTheAsset(t *testing.T) {
	project := clitest.SetUpConnectorFixture(t, fingerprint, hostname)
	root := project.Root
	srv := newConsoleServer(t)
	linked(t, root, srv.URL)

	dependencies := newTestDependencies()
	dependencies.LoadCredentials = clitest.LoadLoggedInCredentials
	dependencies.ConfigPath = func() string { return filepath.Join(root, "ocel.fake.json") }

	var stdout bytes.Buffer
	clitest.AttachTerminalSink(dependencies.Invocation, &stdout)
	if err := runAdd(context.Background(), dependencies, resolved(t, root), read(t, root, srv.URL), opened(t, srv)); err != nil {
		t.Fatalf("runAdd err = %v\n%s", err, stdout.String())
	}

	if len(srv.upserted) != 1 {
		t.Fatalf("upserts = %v, want one", srv.upserted)
	}
	wantUpsert := &consolev1.UpsertConnectorRequest{Target: fingerprint, Vendor: "fake", Reach: consolev1.ConnectorReach_CONNECTOR_REACH_DIAL}
	if !proto.Equal(srv.upserted[0], wantUpsert) {
		t.Errorf("upsert = %v, want %v", srv.upserted[0], wantUpsert)
	}
	if len(srv.addressed) != 1 {
		t.Fatalf("addresses = %v, want one", srv.addressed)
	}
	if srv.addressed[0].GetUrl() != "https://"+hostname+"/"+statedir.Name+"/connector" {
		t.Errorf("addressed url = %v, want the box's own connector path", srv.addressed[0].GetUrl())
	}
	if srv.addressed[0].GetPublicKey() == "" {
		t.Error("the console was told no public key, so it can verify no heartbeat")
	}
	if srv.addressed[0].GetCompute() != consolev1.ComputeKind_COMPUTE_KIND_CONTAINER {
		t.Errorf("addressed compute = %v, want the compute the provider chose for itself", srv.addressed[0].GetCompute())
	}

	installed := project.Provider.FakeConnector().Installed()
	if len(installed) == 0 {
		t.Fatal("the provider was never asked to install a connector")
	}
	if string(installed[0].Binary) != clitest.FakeConnectorBinary {
		t.Errorf("the provider was handed %q, want the connector asset the arch names", string(installed[0].Binary))
	}
	var config map[string]any
	if err := json.Unmarshal(installed[0].Config, &config); err != nil {
		t.Fatalf("the config the provider was handed is not an object: %v", err)
	}
	if config["console"] != srv.URL || config["connectorId"] != registeredID || config["organizationId"] != "org_1" {
		t.Errorf("config = %v, want it to name this console, the row it upserted and the org", config)
	}
	if config["target"] != fingerprint {
		t.Errorf("config target = %v, want %q", config["target"], fingerprint)
	}
	grants, _ := config["grants"].([]any)
	if len(grants) != 2 || grants[0] != "variables.read" || grants[1] != "variables.write" {
		t.Errorf("grants = %v, want read and write and no reveal", grants)
	}
	if !strings.Contains(stdout.String(), fingerprint) {
		t.Errorf("stdout = %q, want it to name the target it paired", stdout.String())
	}
}

func TestAddInstallsTheConnectorBuiltForThePlatformTheProviderNames(t *testing.T) {
	project := clitest.SetUpConnectorFixture(t, fingerprint, hostname)
	root := project.Root
	clitest.InstallConnector(t, "fake", executables.Platform{GOOS: "freebsd", GOARCH: "amd64"}, []byte("freebsd connector"))
	project.Provider.FakeConnector().Runs(provider.ConnectorTarget{Fingerprint: fingerprint, Hostname: hostname, OS: "freebsd", Arch: "amd64"})
	srv := newConsoleServer(t)
	linked(t, root, srv.URL)

	dependencies := newTestDependencies()
	dependencies.LoadCredentials = clitest.LoadLoggedInCredentials
	dependencies.ConfigPath = func() string { return filepath.Join(root, "ocel.fake.json") }

	var stdout bytes.Buffer
	clitest.AttachTerminalSink(dependencies.Invocation, &stdout)
	if err := runAdd(context.Background(), dependencies, resolved(t, root), read(t, root, srv.URL), opened(t, srv)); err != nil {
		t.Fatalf("runAdd err = %v\n%s", err, stdout.String())
	}

	installed := project.Provider.FakeConnector().Installed()
	if len(installed) != 1 || string(installed[0].Binary) != "freebsd connector" {
		t.Errorf("the provider was handed %v, want the connector built for the freebsd target it described", installed)
	}
}

func TestAddRelaysWhatTheProviderSaysWhileItInstallsThroughItsRun(t *testing.T) {
	project := clitest.SetUpConnectorFixture(t, fingerprint, hostname)
	root := project.Root
	srv := newConsoleServer(t)
	linked(t, root, srv.URL)

	dependencies := newJSONDependencies()
	dependencies.ConfigPath = func() string { return filepath.Join(root, "ocel.fake.json") }

	var stream bytes.Buffer
	clitest.AttachTerminalSink(dependencies.Invocation, &stream)
	if err := runAdd(context.Background(), dependencies, resolved(t, root), read(t, root, srv.URL), opened(t, srv)); err != nil {
		t.Fatalf("runAdd err = %v\n%s", err, stream.String())
	}

	events := clitest.RunEvents(t, stream.String())
	if !slices.ContainsFunc(events, func(event *streamv1.RunEvent) bool { return event.GetOperation().GetMessage() == "wrote the connector" }) {
		t.Errorf("stream = %s, want the line the provider said while installing", stream.String())
	}
	result := events[len(events)-1].GetSummary()
	if !result.GetSuccess() || !strings.Contains(result.GetHeadline(), fingerprint) || strings.Contains(result.GetHeadline(), "variablestore") {
		t.Errorf("result = %v, want a success that names the paired target and leaves the grants to their own line", result)
	}
	if !slices.ContainsFunc(events, func(event *streamv1.RunEvent) bool {
		return event.GetOperation().GetBody() == nil && event.GetCli() == nil && event.GetOperation().GetMessage() == "The console may use this connector for variables.read and variables.write"
	}) {
		t.Errorf("stream = %s, want the grants said on a line of their own", stream.String())
	}
}

func TestAddGrantsRevealOnlyWhenItIsAskedFor(t *testing.T) {
	project := clitest.SetUpConnectorFixture(t, fingerprint, hostname)
	root := project.Root
	srv := newConsoleServer(t)
	linked(t, root, srv.URL)

	dependencies := newTestDependencies()
	dependencies.LoadCredentials = clitest.LoadLoggedInCredentials

	opts := opened(t, srv)
	opts.reveal = true
	opts.write = false
	if err := runAdd(context.Background(), dependencies, resolved(t, root), read(t, root, srv.URL), opts); err != nil {
		t.Fatalf("runAdd err = %v", err)
	}

	installed := project.Provider.FakeConnector().Installed()
	if len(installed) != 1 {
		t.Fatalf("installed = %v, want one connector", installed)
	}
	var config map[string]any
	if err := json.Unmarshal(installed[0].Config, &config); err != nil {
		t.Fatal(err)
	}
	grants, _ := config["grants"].([]any)
	if len(grants) != 2 || grants[0] != "variables.read" || grants[1] != "variables.reveal" {
		t.Errorf("grants = %v, want read and reveal and no write", grants)
	}
}

func TestAConnectorOverTheChannelCeilingIsRefusedBeforeItIsSent(t *testing.T) {
	project := clitest.SetUpConnectorFixture(t, fingerprint, hostname)
	root := project.Root
	srv := newConsoleServer(t)
	linked(t, root, srv.URL)

	binary := clitest.InstallConnector(t, "fake", executables.Platform{GOOS: "linux", GOARCH: "amd64"}, []byte(clitest.FakeConnectorBinary))
	if err := os.Truncate(binary, providerprocess.MaxMessageBytes+1); err != nil {
		t.Fatalf("grow the connector past the ceiling: %v", err)
	}

	dependencies := newJSONDependencies()
	var stream bytes.Buffer
	clitest.AttachTerminalSink(dependencies.Invocation, &stream)

	err := runAdd(context.Background(), dependencies, resolved(t, root), read(t, root, srv.URL), opened(t, srv))
	if err == nil {
		t.Fatal("runAdd err = nil, want a connector over the ceiling refused")
	}
	said := failure(t, stream.String())
	for _, want := range []string{"fake connector", "over the"} {
		if !strings.Contains(said, want) {
			t.Errorf("runAdd err = %q, want it to contain %q", said, want)
		}
	}
	if installed := project.Provider.FakeConnector().Installed(); len(installed) != 0 {
		t.Errorf("the connector was installed anyway (%d installs), want nothing sent over the channel", len(installed))
	}
}

func TestAFailedAddSaysRunningItAgainFinishesIt(t *testing.T) {
	project := clitest.SetUpConnectorFixture(t, fingerprint, hostname)
	root := project.Root
	srv := newConsoleServer(t)
	srv.refuseAddress = true
	linked(t, root, srv.URL)

	dependencies := newJSONDependencies()
	var stream bytes.Buffer
	clitest.AttachTerminalSink(dependencies.Invocation, &stream)

	err := runAdd(context.Background(), dependencies, resolved(t, root), read(t, root, srv.URL), opened(t, srv))
	if err == nil {
		t.Fatal("a console that refused the address answered no error")
	}
	said := failure(t, stream.String())
	if !strings.Contains(said, "ocel connector add") || !strings.Contains(said, "already has this target registered") {
		t.Errorf("runAdd err = %q, and a half-finished add has to say that the console has the target registered and that re-running finishes it", said)
	}
	if len(srv.upserted) != 1 {
		t.Errorf("upserts = %v, want the one the run made before it failed", srv.upserted)
	}
}

func TestRemoveTakesTheConnectorOffTheBoxAndForgetsTheRow(t *testing.T) {
	project := clitest.SetUpConnectorFixture(t, fingerprint, hostname)
	root := project.Root
	srv := newConsoleServer(t, &consolev1.Connector{
		Id: registeredID, Target: fingerprint, Vendor: "fake", Compute: consolev1.ComputeKind_COMPUTE_KIND_CONTAINER,
		Reach: consolev1.ConnectorReach_CONNECTOR_REACH_DIAL, Capabilities: []string{"variables.read"},
	})
	linked(t, root, srv.URL)

	dependencies := newTestDependencies()
	dependencies.LoadCredentials = clitest.LoadLoggedInCredentials

	var stdout bytes.Buffer
	clitest.AttachTerminalSink(dependencies.Invocation, &stdout)
	if err := runRemove(context.Background(), dependencies, resolved(t, root), read(t, root, srv.URL), opened(t, srv)); err != nil {
		t.Fatalf("runRemove err = %v", err)
	}

	if project.Provider.FakeConnector().Removals() == 0 {
		t.Fatal("the provider was never asked to take the connector off the machine")
	}
	if len(srv.deleted) != 1 || srv.deleted[0] != registeredID {
		t.Fatalf("deleted = %v, want the one row keyed by this target", srv.deleted)
	}
}

func TestAnUnreachableMachineIsPointedAtRmTarget(t *testing.T) {
	project := clitest.SetUpConnectorFixture(t, fingerprint, hostname)
	root := project.Root
	project.Provider.FakeConnector().Refuse(refusal.Refuse(refusal.CodeNotReady, "this machine is not reachable"))
	srv := newConsoleServer(t, &consolev1.Connector{
		Id: registeredID, Target: fingerprint, Vendor: "fake", Compute: consolev1.ComputeKind_COMPUTE_KIND_CONTAINER,
		Reach: consolev1.ConnectorReach_CONNECTOR_REACH_DIAL,
	})
	linked(t, root, srv.URL)

	dependencies := newJSONDependencies()
	var stream bytes.Buffer
	clitest.AttachTerminalSink(dependencies.Invocation, &stream)

	err := runRemove(context.Background(), dependencies, resolved(t, root), read(t, root, srv.URL), opened(t, srv))
	if err == nil {
		t.Fatal("runRemove err = nil, want the unreachable machine reported")
	}
	said := failure(t, stream.String())
	if !strings.Contains(said, "ocel connector status") || !strings.Contains(said, "rm --target") {
		t.Errorf("runRemove err = %q, want it to name the remedy for a target that will not answer", said)
	}
	if len(srv.deleted) != 0 {
		t.Fatalf("deleted = %v: with no fingerprint there is no row to key on", srv.deleted)
	}
}

func TestRmTargetForgetsTheRowWithoutTouchingTheTarget(t *testing.T) {
	project := clitest.SetUpConnectorFixture(t, fingerprint, hostname)
	root := project.Root
	project.Provider.FakeConnector().Refuse(refusal.Refuse(refusal.CodeNotReady, "this machine is not reachable"))
	srv := newConsoleServer(t, &consolev1.Connector{
		Id: registeredID, Target: fingerprint, Vendor: "fake", Compute: consolev1.ComputeKind_COMPUTE_KIND_CONTAINER,
		Reach: consolev1.ConnectorReach_CONNECTOR_REACH_DIAL,
	})
	linked(t, root, srv.URL)

	dependencies := newTestDependencies()
	dependencies.LoadCredentials = clitest.LoadLoggedInCredentials

	opts := opened(t, srv)
	opts.target = fingerprint

	var stdout bytes.Buffer
	clitest.AttachTerminalSink(dependencies.Invocation, &stdout)
	if err := runRemove(context.Background(), dependencies, resolved(t, root), read(t, root, srv.URL), opts); err != nil {
		t.Fatalf("runRemove err = %v", err)
	}
	if len(srv.deleted) != 1 || srv.deleted[0] != registeredID {
		t.Fatalf("deleted = %v, want the row keyed by the target the flag named", srv.deleted)
	}
	if project.Provider.FakeConnector().Removals() != 0 {
		t.Error("the provider was asked to take the connector off a machine this run was told not to reach")
	}
	if !strings.Contains(stdout.String(), "never reached") {
		t.Errorf("stdout = %q, want it to say the target itself was left alone", stdout.String())
	}
}

func TestRemovingATargetSaysSoWhenTheConsoleHasNoSuchTarget(t *testing.T) {
	project := clitest.SetUpConnectorFixture(t, fingerprint, hostname)
	root := project.Root
	srv := newConsoleServer(t)
	linked(t, root, srv.URL)

	dependencies := newTestDependencies()
	dependencies.LoadCredentials = clitest.LoadLoggedInCredentials

	opts := opened(t, srv)
	opts.target = fingerprint

	var stdout bytes.Buffer
	clitest.AttachTerminalSink(dependencies.Invocation, &stdout)
	if err := runRemove(context.Background(), dependencies, resolved(t, root), read(t, root, srv.URL), opts); err != nil {
		t.Fatalf("runRemove err = %v", err)
	}
	if len(srv.deleted) != 0 {
		t.Fatalf("deleted = %v, want nothing deleted", srv.deleted)
	}
	if !strings.Contains(stdout.String(), "has no connector registered") {
		t.Errorf("stdout = %q, want it to say the console has no such target registered", stdout.String())
	}
}

func TestStatusSaysWhatTheConsoleHasRegistered(t *testing.T) {
	project := clitest.SetUpConnectorFixture(t, fingerprint, hostname)
	root := project.Root
	seen := time.Now().Add(-10 * time.Second)
	srv := newConsoleServer(t, &consolev1.Connector{
		Id: registeredID, Target: fingerprint, Vendor: "fake", Compute: consolev1.ComputeKind_COMPUTE_KIND_CONTAINER,
		Reach: consolev1.ConnectorReach_CONNECTOR_REACH_DIAL,
		Url:   proto.String("https://" + hostname + "/" + statedir.Name + "/connector"), Capabilities: []string{"variables.read", "variables.write"},
		ConnectedAt: timestamppb.New(seen), LastSeenAt: timestamppb.New(seen), Online: true,
	})
	linked(t, root, srv.URL)

	dependencies := newTestDependencies()
	dependencies.LoadCredentials = clitest.LoadLoggedInCredentials
	dependencies.ConfigPath = func() string { return "" }

	var stdout bytes.Buffer
	if err := runStatus(context.Background(), dependencies, resolved(t, root), read(t, root, srv.URL), opened(t, srv), &stdout); err != nil {
		t.Fatalf("runStatus err = %v", err)
	}
	for _, want := range []string{fingerprint, "container over dial", "online", "variables.read, variables.write"} {
		if !strings.Contains(stdout.String(), want) {
			t.Errorf("stdout = %q, want it to contain %q", stdout.String(), want)
		}
	}
}

func TestStatusSaysSoWhenTheOrganizationHasNoConnector(t *testing.T) {
	project := clitest.SetUpConnectorFixture(t, fingerprint, hostname)
	root := project.Root
	srv := newConsoleServer(t)
	linked(t, root, srv.URL)

	dependencies := newTestDependencies()
	dependencies.LoadCredentials = clitest.LoadLoggedInCredentials
	dependencies.ConfigPath = func() string { return "" }

	var stdout bytes.Buffer
	if err := runStatus(context.Background(), dependencies, resolved(t, root), read(t, root, srv.URL), opened(t, srv), &stdout); err != nil {
		t.Fatalf("runStatus err = %v", err)
	}
	if !strings.Contains(stdout.String(), "ocel connector add") {
		t.Errorf("stdout = %q, want it to point at the command that adds one", stdout.String())
	}
}

func TestStatusAsJSONPrintsEveryRegisteredConnectorAsOneEnvelope(t *testing.T) {
	project := clitest.SetUpConnectorFixture(t, fingerprint, hostname)
	root := project.Root
	seen := time.Date(2026, 10, 5, 8, 0, 0, 0, time.UTC)
	srv := newConsoleServer(t,
		&consolev1.Connector{
			Id: "0199b5c4-0000-7000-8000-00000000cafd", Target: "zzz/sha256:bbbb/ocel", Vendor: "fake", Reach: consolev1.ConnectorReach_CONNECTOR_REACH_DIAL,
		},
		&consolev1.Connector{
			Id: registeredID, Target: fingerprint, Vendor: "fake", Compute: consolev1.ComputeKind_COMPUTE_KIND_CONTAINER,
			Reach:        consolev1.ConnectorReach_CONNECTOR_REACH_DIAL,
			Url:          proto.String("https://" + hostname + "/" + statedir.Name + "/connector"),
			Capabilities: []string{"variables.write", "variables.read"},
			ConnectedAt:  timestamppb.New(seen), LastSeenAt: timestamppb.New(seen), Online: true,
			LastDenied: &consolev1.ConnectorDenial{
				Verb: "variables.write", At: timestamppb.New(time.Date(2026, 10, 5, 7, 0, 0, 123_000_000, time.UTC)), Message: "not allowed",
			},
		})
	linked(t, root, srv.URL)

	dependencies := newJSONDependencies()
	dependencies.ConfigPath = func() string { return "" }

	var stdout, stderr bytes.Buffer
	clitest.AttachTerminalSink(dependencies.Invocation, &stderr)
	if err := runStatus(context.Background(), dependencies, resolved(t, root), read(t, root, srv.URL), opened(t, srv), &stdout); err != nil {
		t.Fatalf("runStatus err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
	}

	var status resultv1.ConnectorStatusResult
	clitest.DecodeResultInto(t, stdout.String(), &status)
	if len(status.GetConnectors()) != 2 {
		t.Fatalf("connectors = %v, want both registered", status.GetConnectors())
	}
	first, second := status.GetConnectors()[0], status.GetConnectors()[1]
	if first.GetTarget() != fingerprint || first.GetCompute() != "container" || first.GetReach() != "dial" {
		t.Errorf("first = %v, want the connector sorted by target", first)
	}
	if first.GetLiveness() != resultv1.ConnectorLiveness_CONNECTOR_LIVENESS_ONLINE || first.GetLastSeenAt() != "2026-10-05T08:00:00Z" {
		t.Errorf("first = %v, want it online and last seen at an RFC 3339 time", first)
	}
	if !slices.Equal(first.GetCapabilities(), []string{"variables.read", "variables.write"}) {
		t.Errorf("capabilities = %v, want them sorted", first.GetCapabilities())
	}
	if first.GetLastDenied().GetVerb() != "variables.write" || first.GetLastDenied().GetMessage() != "not allowed" {
		t.Errorf("lastDenied = %v, want the refusal the console recorded", first.GetLastDenied())
	}
	if first.GetLastDenied().GetAt() != "2026-10-05T07:00:00Z" {
		t.Errorf("lastDenied.at = %q, want the console's millisecond time as UTC RFC 3339 to the second", first.GetLastDenied().GetAt())
	}
	if second.GetLiveness() != resultv1.ConnectorLiveness_CONNECTOR_LIVENESS_NEVER_CONNECTED || second.GetLastDenied() != nil {
		t.Errorf("second = %v, want a connector that never connected and was never refused", second)
	}
}

func TestStatusAsJSONKeepsARefusalButLeavesItsTimeEmptyWhenTheConsoleRecordedNone(t *testing.T) {
	project := clitest.SetUpConnectorFixture(t, fingerprint, hostname)
	root := project.Root
	srv := newConsoleServer(t, &consolev1.Connector{
		Id: registeredID, Target: fingerprint, Vendor: "fake", Reach: consolev1.ConnectorReach_CONNECTOR_REACH_DIAL,
		LastDenied: &consolev1.ConnectorDenial{Verb: "variables.write", Message: "not allowed"},
	})
	linked(t, root, srv.URL)

	dependencies := newJSONDependencies()
	dependencies.ConfigPath = func() string { return "" }

	var stdout bytes.Buffer
	if err := runStatus(context.Background(), dependencies, resolved(t, root), read(t, root, srv.URL), opened(t, srv), &stdout); err != nil {
		t.Fatalf("runStatus err = %v", err)
	}

	var status resultv1.ConnectorStatusResult
	clitest.DecodeResultInto(t, stdout.String(), &status)
	denied := status.GetConnectors()[0].GetLastDenied()
	if denied.GetVerb() != "variables.write" || denied.GetMessage() != "not allowed" || denied.GetAt() != "" {
		t.Errorf("lastDenied = %v, want the refusal kept and its unreadable time left empty", denied)
	}
}

func TestStatusAsJSONPrintsAnEmptyListWhenTheOrganizationHasNoConnector(t *testing.T) {
	project := clitest.SetUpConnectorFixture(t, fingerprint, hostname)
	root := project.Root
	srv := newConsoleServer(t)
	linked(t, root, srv.URL)

	dependencies := newJSONDependencies()
	dependencies.ConfigPath = func() string { return "" }

	var stdout bytes.Buffer
	if err := runStatus(context.Background(), dependencies, resolved(t, root), read(t, root, srv.URL), opened(t, srv), &stdout); err != nil {
		t.Fatalf("runStatus err = %v", err)
	}

	if data := clitest.DecodeResult(t, stdout.String()); !reflect.DeepEqual(data["connectors"], []any{}) {
		t.Errorf("data = %v, want an empty connectors list", data)
	}
}

func TestStatusForAConfigAsJSONPrintsAnEmptyListWhenItsTargetIsNotRegistered(t *testing.T) {
	project := clitest.SetUpConnectorFixture(t, fingerprint, hostname)
	root := project.Root
	srv := newConsoleServer(t, &consolev1.Connector{Id: "0199b5c4-0000-7000-8000-00000000cafc", Target: "other/sha256:cccc/ocel", Vendor: "fake", Reach: consolev1.ConnectorReach_CONNECTOR_REACH_DIAL})
	linked(t, root, srv.URL)

	dependencies := newJSONDependencies()
	dependencies.ConfigPath = func() string { return filepath.Join(root, "ocel.fake.json") }

	var stdout, stderr bytes.Buffer
	clitest.AttachTerminalSink(dependencies.Invocation, &stderr)
	if err := runStatus(context.Background(), dependencies, resolved(t, root), read(t, root, srv.URL), opened(t, srv), &stdout); err != nil {
		t.Fatalf("runStatus err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
	}

	if data := clitest.DecodeResult(t, stdout.String()); !reflect.DeepEqual(data["connectors"], []any{}) {
		t.Errorf("data = %v, want an empty connectors list", data)
	}
}

func TestAnUnlinkedTreeIsPointedAtOcelLink(t *testing.T) {
	project := clitest.SetUpConnectorFixture(t, fingerprint, hostname)
	root := project.Root

	dependencies := newTestDependencies()
	dependencies.LoadCredentials = clitest.LoadLoggedInCredentials
	dependencies.ConfigPath = func() string { return filepath.Join(root, "ocel.fake.json") }

	cmd := NewCommand(dependencies)
	cmd.SetArgs([]string{"status"})
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	if err := chdir(t, root, cmd.Execute); !errors.As(err, new(*exitcode.ExitError)) {
		t.Fatalf("Execute() = %v, want an exit error", err)
	}
	if !strings.Contains(out.String(), "ocel link") {
		t.Errorf("output = %q, want it to point at `ocel link`", out.String())
	}
}

func TestBeingLoggedOutIsPointedAtOcelLogin(t *testing.T) {
	project := clitest.SetUpConnectorFixture(t, fingerprint, hostname)
	root := project.Root

	dependencies := newTestDependencies()
	dependencies.LoadCredentials = func() (console.Credentials, error) {
		return console.Credentials{}, console.ErrNotLoggedIn
	}
	dependencies.ConfigPath = func() string { return filepath.Join(root, "ocel.fake.json") }

	cmd := NewCommand(dependencies)
	cmd.SetArgs([]string{"status"})
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	if err := chdir(t, root, cmd.Execute); !errors.As(err, new(*exitcode.ExitError)) {
		t.Fatalf("Execute() = %v, want an exit error", err)
	}
	if !strings.Contains(out.String(), "ocel login") {
		t.Errorf("output = %q, want it to point at `ocel login`", out.String())
	}
}

func TestAnUnlinkedTreeReportsConsoleNotLinkedWithOcelLinkAsItsHint(t *testing.T) {
	project := clitest.SetUpConnectorFixture(t, fingerprint, hostname)
	root := project.Root

	dependencies := newTestDependencies()
	dependencies.LoadCredentials = clitest.LoadLoggedInCredentials
	dependencies.ConfigPath = func() string { return filepath.Join(root, "ocel.fake.json") }

	cmd := NewCommand(dependencies)
	cmd.SetArgs([]string{"status"})
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	err := chdir(t, root, cmd.Execute)

	got := clierror.NewRunError(fmt.Errorf("status: %w", err))
	if got.GetCode() != "console.not_linked" || got.GetHint() != "ocel link" {
		t.Fatalf("run error = %s, want console.not_linked hinting `ocel link`", protojson.Format(got))
	}
	if code, ok := exitcode.Of(err); !ok || code != 1 {
		t.Errorf("exit code = %d, %v, want 1 from the exit error", code, ok)
	}
	if strings.Count(out.String(), "ocel link") != 1 {
		t.Errorf("output = %q, want the one human line pointing at `ocel link`", out.String())
	}
}

func TestBeingLoggedOutReportsConsoleNotLoggedInWithOcelLoginAsItsHint(t *testing.T) {
	project := clitest.SetUpConnectorFixture(t, fingerprint, hostname)
	root := project.Root

	dependencies := newTestDependencies()
	dependencies.LoadCredentials = func() (console.Credentials, error) {
		return console.Credentials{}, console.ErrNotLoggedIn
	}
	dependencies.ConfigPath = func() string { return filepath.Join(root, "ocel.fake.json") }

	cmd := NewCommand(dependencies)
	cmd.SetArgs([]string{"status"})
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	err := chdir(t, root, cmd.Execute)

	got := clierror.NewRunError(fmt.Errorf("status: %w", err))
	if got.GetCode() != "console.not_logged_in" || got.GetHint() != "ocel login" {
		t.Fatalf("run error = %s, want console.not_logged_in hinting `ocel login`", protojson.Format(got))
	}
	if code, ok := exitcode.Of(err); !ok || code != 1 {
		t.Errorf("exit code = %d, %v, want 1 from the exit error", code, ok)
	}
	if !errors.Is(err, console.ErrNotLoggedIn) {
		t.Errorf("Execute() = %v, want it to still be console.ErrNotLoggedIn", err)
	}
}

func TestTheVendorIsWhateverTheConfigPointsAtAndNoTableGatesIt(t *testing.T) {
	t.Parallel()

	for _, vendor := range []string{"fake", "elsewhere", "nowhere"} {
		cfg := &project.Project{
			Path:     "ocel." + vendor + ".json",
			Provider: &project.Provider{ID: vendor},
		}
		named, err := requireProviderID(cfg)
		if err != nil {
			t.Fatalf("requireProviderID(%s) = %v, want the vendor the config names", vendor, err)
		}
		if named != vendor {
			t.Errorf("requireProviderID(%s) = %q, want %q", vendor, named, vendor)
		}
	}
}

func TestTheComputeGoesToTheProviderUntouched(t *testing.T) {
	project := clitest.SetUpConnectorFixture(t, fingerprint, hostname)
	root := project.Root
	srv := newConsoleServer(t)
	linked(t, root, srv.URL)

	dependencies := newTestDependencies()
	dependencies.LoadCredentials = clitest.LoadLoggedInCredentials

	project.Provider.FakeConnector().RunsOn(provider.ComputeContainer, provider.ComputeServerless)
	opts := opened(t, srv)
	opts.compute = "serverless"
	if err := runAdd(context.Background(), dependencies, resolved(t, root), read(t, root, srv.URL), opts); err != nil {
		t.Fatalf("runAdd err = %v", err)
	}

	installed := project.Provider.FakeConnector().Installed()
	if len(installed) != 1 || installed[0].Compute != provider.ComputeServerless {
		t.Errorf("the provider was asked for %v, want the compute the flag named passed through with no vendor table in the way", installed)
	}
	if len(srv.addressed) != 1 || srv.addressed[0].GetCompute() != consolev1.ComputeKind_COMPUTE_KIND_SERVERLESS {
		t.Errorf("addresses = %v, want the console told what the provider chose", srv.addressed)
	}
}

func read(t *testing.T, dir, apiURL string) *console.Link {
	t.Helper()

	linked, err := console.ReadLink(dir, apiURL)
	if err != nil {
		t.Fatalf("consolelink.Read: %v", err)
	}
	if linked == nil {
		t.Fatal("no link written")
	}
	return linked
}

func chdir(t *testing.T, dir string, run func() error) error {
	t.Helper()

	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(wd)
	return run()
}

func newJSONDependencies() Dependencies {
	dependencies := newTestDependencies()
	dependencies.LoadCredentials = clitest.LoadLoggedInCredentials
	dependencies.Presentation = clitest.ResolveJSONPresentation
	return dependencies
}

func failure(t *testing.T, stream string) string {
	t.Helper()
	events := clitest.RunEvents(t, stream)
	if len(events) == 0 || events[len(events)-1].GetSummary() == nil {
		t.Fatalf("stream = %s, want it to end with the run's result", stream)
	}
	result := events[len(events)-1].GetSummary()
	if result.GetSuccess() {
		t.Fatalf("result = %v, want the run to fail", result)
	}
	return result.GetDetail()
}

func TestStatusForAConfigReadsItsTargetInTheCheckPhaseOfItsRunAndPrintsWhatTheConsoleHasAloneOnStdout(t *testing.T) {
	project := clitest.SetUpConnectorFixture(t, fingerprint, hostname)
	root := project.Root
	srv := newConsoleServer(t, &consolev1.Connector{
		Id: registeredID, Target: fingerprint, Vendor: "fake", Compute: consolev1.ComputeKind_COMPUTE_KIND_CONTAINER,
		Reach: consolev1.ConnectorReach_CONNECTOR_REACH_DIAL,
	})
	linked(t, root, srv.URL)

	dependencies := newJSONDependencies()
	dependencies.ConfigPath = func() string { return filepath.Join(root, "ocel.fake.json") }

	var stdout, stderr bytes.Buffer
	clitest.AttachTerminalSink(dependencies.Invocation, &stderr)
	if err := runStatus(context.Background(), dependencies, resolved(t, root), read(t, root, srv.URL), opened(t, srv), &stdout); err != nil {
		t.Fatalf("runStatus err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
	}

	events := clitest.RunEvents(t, stderr.String())
	if len(events) == 0 || events[0].GetOperation().GetStarted() == nil || events[0].GetOperation().GetPhase() != progressv1.Phase_PHASE_CHECK {
		t.Fatalf("stream = %s, want a run that opens with the check phase that starts the provider", stderr.String())
	}
	if result := events[len(events)-1].GetSummary(); !result.GetSuccess() {
		t.Errorf("result = %v, want the status run to succeed", result)
	}
	if !strings.Contains(stdout.String(), fingerprint) || strings.Contains(stderr.String(), "container over dial") {
		t.Errorf("stdout = %q, stream = %q: want what the console has on stdout and not on the stream", stdout.String(), stderr.String())
	}
}
