package deploy

import (
	"bytes"
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/cli/internal/clitest"
	"github.com/ocelhq/ocel/pkg/proto/provider/contract/v1/contractv1connect"
	"github.com/ocelhq/ocel/pkg/provider/fake"
	"github.com/ocelhq/ocel/pkg/refusal"
)

func forwardingRelay(fixture clitest.FakeProject) *fake.Edge {
	relay := fixture.Provider.Edges().(*fake.Edges).Edge(fake.KindRelay)
	relay.ProxiesRecords()
	return relay
}

func TestADeployWhoseForwardedHostnameTheEdgeRefusesProvisionsNothing(t *testing.T) {
	for name, refused := range map[string]struct {
		code    refusal.Code
		message string
	}{
		"a zone not on Full (strict)": {refusal.CodeInvalid,
			`zone acme.com reaches origins with SSL mode "full", which does not check the certificate the origin answers with: set the zone's SSL mode to Full (strict)`},
		"a token denied the client certificate API": {refusal.CodeDenied,
			"CLOUDFLARE_API_TOKEN may not read whether zone acme.com presents a client certificate to origins"},
	} {
		t.Run(name, func(t *testing.T) {
			dependencies := newTestDependencies()
			stubBuild(&dependencies, nil)
			fixture := setUpDeployProject(t)
			forwardingRelay(fixture).RefusesUnreadyHostnames(refusal.Refuse(refused.code, "%s", refused.message))

			var stdout, stderr bytes.Buffer
			clitest.AttachTerminalSink(dependencies.Invocation, &stdout)
			err := runDeploy(context.Background(), dependencies, fixture.Root, deployOptions{yes: true}, &stdout, &stderr, strings.NewReader(""))

			if err == nil || !strings.Contains(err.Error()+stdout.String()+stderr.String(), refused.message) {
				t.Fatalf("runDeploy err = %v, want the edge's refusal %q", err, refused.message)
			}
			if provisioned := fixture.Provider.FakeStacks().Provisioned(); len(provisioned) != 0 {
				t.Errorf("the deploy provisioned %d stacks, want none: the zone refuses %s only once the deploy attaches it, after every stack it provisioned", len(provisioned), productionDomain)
			}
			procedures := fixture.Requests.Procedures()
			for _, procedure := range []string{contractv1connect.ProviderServiceProvisionInfraProcedure, contractv1connect.ProviderServiceDeployProcedure} {
				if slices.Contains(procedures, procedure) {
					t.Errorf("the CLI called %s, want the deploy refused at its checks, before anything is provisioned", procedure)
				}
			}
		})
	}
}

func TestADeployWhoseForwardedHostnameTheEdgeCanForwardGoesAhead(t *testing.T) {
	dependencies := newTestDependencies()
	stubBuild(&dependencies, nil)
	fixture := setUpDeployProject(t)
	relay := forwardingRelay(fixture)

	deployed(t, dependencies, fixture, deployOptions{yes: true})

	if asked := relay.HostnamesAskedReady(); !slices.Contains(asked, productionDomain) {
		t.Errorf("the edge was asked about %v, want %s, the hostname it forwards to the origin", asked, productionDomain)
	}
	if len(sentProvisionInfras(t, fixture)) != 1 || len(fixture.Provider.FakeStacks().Provisioned()) == 0 {
		t.Error("the deploy provisioned neither its infra nor any stack, want it to go ahead")
	}
}
