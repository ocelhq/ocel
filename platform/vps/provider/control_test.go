package vps_test

import (
	"context"
	"errors"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"connectrpc.com/connect"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/progress"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/proto/provider/contract/v1/contractv1connect"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/fake"
	"github.com/ocelhq/ocel/pkg/provider/providerserver"
	"github.com/ocelhq/ocel/pkg/refusal"
	vps "github.com/ocelhq/ocel/platform/vps/provider"
	"github.com/ocelhq/ocel/platform/vps/provider/boxapi"
	"github.com/ocelhq/ocel/platform/vps/provider/ssh"
)

var onTheBox = provider.Options{"ssh": map[string]any{"host": "box.example.com", "user": boxapi.DeployLogin}}

func controlOver(t *testing.T, machine *box) provider.Provider {
	t.Helper()
	built, err := vps.ControlOver(onTheBox, machine, fake.NewKeyValues())
	if err != nil {
		t.Fatal(err)
	}
	return built
}

func needsRunner(err error) bool {
	var refused refusal.Refusal
	return errors.As(err, &refused) && refused.Code == refusal.CodeNeedsRunner
}

func TestTheBoxsControlAssemblyStatesTheFactsTheFullProviderDoes(t *testing.T) {
	t.Parallel()

	if got, want := controlOver(t, &box{}).Facts(), over(&box{}).Facts(); !reflect.DeepEqual(got, want) {
		t.Errorf("control facts = %+v, want the full provider's %+v", got, want)
	}
}

func TestTheBoxsControlAssemblyRefusesWhatOnlyARunnerDoes(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	p := controlOver(t, &box{})
	hooks := p.Hooks()
	_, bootstrap := p.Bootstrap(edge.None)
	_, provision := p.Stacks().Provision(ctx, provider.StackSpec{}, progress.Discard())
	_, binary := p.Runtime().Binary(ctx, "amd64")
	_, planned := hooks.Preflight.Plan(ctx, provider.DeployPreflight{})
	applied := hooks.Preflight.Apply(ctx, provider.DeployPreflight{})
	_, registry := hooks.OpenRegistryImages(ctx, provider.RegistryTarget{})
	_, direct := hooks.OpenDirectImages(ctx)
	_, checked := hooks.CheckHost(ctx, provider.HostCheckRequest{Tier: environment.TierProduction})
	_, forwarded := hooks.BindingAccess.ForwardPorts(ctx, provider.PortForwardRequest{}, progress.Discard())
	_, installed := p.Connector().Install(ctx, provider.ConnectorInstall{}, progress.Discard())
	_, whoami := p.Credentials().Whoami(ctx)
	for name, err := range map[string]error{
		"Bootstrap": bootstrap, "Stacks.Provision": provision, "Runtime.Binary": binary,
		"Preflight.Plan": planned, "Preflight.Apply": applied, "OpenRegistryImages": registry, "OpenDirectImages": direct,
		"CheckHost": checked, "ForwardPorts": forwarded, "Connector.Install": installed, "Credentials.Whoami": whoami,
	} {
		if !needsRunner(err) {
			t.Errorf("%s = %v, want a needs-runner refusal", name, err)
		}
	}
}

func TestTheBoxsControlAssemblyOpensTheRouterAndEdgeAControlCallMoves(t *testing.T) {
	t.Parallel()

	p := controlOver(t, &box{})
	if _, err := p.Routers().Open(boxapi.SwitchboardRouter); err != nil {
		t.Errorf("Routers().Open(switchboard) = %v", err)
	}
	if _, err := p.Edges().Open(edge.None, nil); err != nil {
		t.Errorf("Edges().Open(none) = %v", err)
	}
}

func controlServing(t *testing.T, machine *box) contractv1connect.ProviderServiceClient {
	t.Helper()
	server := httptest.NewServer(providerserver.ConformanceMux(providerserver.Config{
		Version: "test",
		New: func(context.Context, provider.Settings) (provider.Provider, error) {
			return controlOver(t, machine), nil
		},
	}))
	t.Cleanup(server.Close)
	client := contractv1connect.NewProviderServiceClient(server.Client(), server.URL)
	if _, err := client.Configure(context.Background(), &contractv1.ConfigureRequest{
		Config: &contractv1.ProviderConfig{ProjectDir: t.TempDir(), Slug: "shop"},
	}); err != nil {
		t.Fatalf("Configure() = %v", err)
	}
	return client
}

func TestTheBoxsControlAssemblyServesAProjectsControlReads(t *testing.T) {
	t.Parallel()

	client := controlServing(t, &box{})
	ctx := context.Background()
	if _, err := client.ListPromotions(ctx, &contractv1.ListPromotionsRequest{Slug: "shop"}); err != nil {
		t.Errorf("ListPromotions() = %v, want it served on the box", err)
	}
	_, err := client.DescribeProject(ctx, &contractv1.DescribeProjectRequest{Slug: "shop"})
	if code := connect.CodeOf(err); code == connect.CodeFailedPrecondition || code == connect.CodeUnimplemented || code == connect.CodeInternal {
		t.Errorf("DescribeProject() = %v, want it read from the box's records", err)
	}
	if _, err := client.ListProjects(ctx, &contractv1.ListProjectsRequest{}); err != nil {
		t.Errorf("ListProjects() = %v, want it served on the box", err)
	}
}

func TestTheBoxsControlAssemblySendsABootstrapToARunner(t *testing.T) {
	t.Parallel()

	_, err := controlServing(t, &box{}).DescribeBootstrap(context.Background(), &contractv1.DescribeBootstrapRequest{})
	if connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Errorf("DescribeBootstrap() = %v, want the needs-runner refusal", err)
	}
}

func servedOnTheBox(err error) bool {
	var wire *connect.Error
	if !errors.As(err, &wire) {
		return true
	}
	for _, detail := range wire.Details() {
		if value, derr := detail.Value(); derr == nil {
			if refused, ok := value.(*contractv1.Refusal); ok && refused.GetCode() == contractv1.RefusalCode_REFUSAL_CODE_NEEDS_RUNNER {
				return false
			}
		}
	}
	return wire.Code() != connect.CodeUnimplemented
}

func TestTheBoxsControlAssemblyAnswersARollbackAndAHostnameItself(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	client := controlServing(t, &box{})
	stream, err := client.Rollback(ctx, &contractv1.RollbackRequest{Slug: "shop"})
	if err == nil {
		for stream.Receive() {
			if failed := stream.Msg().GetResult(); failed != nil && !failed.GetSuccess() {
				err = errors.New(failed.GetError())
			}
		}
		if streamErr := stream.Err(); streamErr != nil {
			err = streamErr
		}
	}
	if !servedOnTheBox(err) {
		t.Errorf("Rollback() = %v, want the box to answer it, not send it to a runner", err)
	}
	_, err = client.GetHostnameStatus(ctx, &contractv1.HostnameRequest{Slug: "shop", Host: "shop.example.com"})
	if !servedOnTheBox(err) {
		t.Errorf("GetHostnameStatus() = %v, want the box to answer it", err)
	}
}

func TestAControlOverTheBoxItselfAddressesItByTheHostTheDeployRecordedNeverByLoopback(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	onBox := ssh.Local{Hostname: "box.example.com", User: boxapi.DeployLogin}
	for recorded, want := range map[string]string{"203.0.113.10": "203.0.113.10", "box.example.com": "box.example.com"} {
		built, err := vps.NewControl(ctx, provider.Settings{Options: provider.Options{"ssh": map[string]any{"host": recorded}}}, onBox)
		if err != nil {
			t.Fatal(err)
		}
		address, err := vps.BoxAddress(ctx, built)
		if err != nil || address != want {
			t.Errorf("the box recorded as %s is addressed at %q, %v, want %q", recorded, address, err, want)
		}
		if strings.HasPrefix(address, "127.") || address == "localhost" || address == "::1" {
			t.Errorf("the box is addressed at loopback %s, and its origins and DNS would point there", address)
		}
	}
}

func TestAControlForABoxItsDeploysNameByAnAliasReachesNoOriginItCannotAddress(t *testing.T) {
	t.Parallel()

	built, err := vps.NewControl(context.Background(), provider.Settings{Options: provider.Options{"ssh": "my-box"}}, ssh.Local{Hostname: "box.example.com", User: boxapi.DeployLogin})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := built.Edges().Open(edge.None, nil); !needsRunner(err) {
		t.Errorf("Edges().Open(none) = %v, want it sent to a runner", err)
	}
	if _, err := built.Routers().Open(boxapi.SwitchboardRouter); !needsRunner(err) {
		t.Errorf("Routers().Open(switchboard) = %v, want it sent to a runner", err)
	}
}

func TestAControlOpensNoCloudflareItHoldsNoCredentialsFor(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	p := controlOver(t, &box{})
	if _, err := p.Edges().Open(edge.Kind("cloudflare"), nil); !needsRunner(err) {
		t.Errorf("Edges().Open(cloudflare) = %v, want it sent to a runner", err)
	}
	if _, err := p.DNS().Open(provider.DNSKind("cloudflare"), "example.com", edge.None); !needsRunner(err) {
		t.Errorf("DNS().Open(cloudflare) = %v, want it sent to a runner", err)
	}
	if _, err := p.Hooks().ListProjectResources(ctx, provider.ProjectResourcesRequest{EdgeKind: edge.Kind("cloudflare")}); !needsRunner(err) {
		t.Errorf("ListProjectResources(cloudflare) = %v, want it sent to a runner", err)
	}
}

func TestAControlNamesWhatADeployLoginIsGrantedWithoutTheBoxsBinaries(t *testing.T) {
	t.Parallel()

	documented, err := controlOver(t, &box{}).Credentials().Permissions(edge.PurposeDeploy, environment.TierProduction)
	if err != nil || documented.Document == "" {
		t.Errorf("Permissions(deploy) = %+v, %v, want the grants written out", documented, err)
	}
}

func TestAControlNeverWritesTheBootstrapGrantForTheDeployLogin(t *testing.T) {
	t.Parallel()

	documented, err := controlOver(t, &box{}).Credentials().Permissions(edge.PurposeBootstrap, environment.TierProduction)
	if !needsRunner(err) {
		t.Errorf("Permissions(bootstrap) = %+v, %v, want it sent to a runner", documented, err)
	}
	if strings.Contains(documented.Document, "NOPASSWD: ALL") {
		t.Errorf("the box offers %q, root for the deploy login", documented.Document)
	}
}
