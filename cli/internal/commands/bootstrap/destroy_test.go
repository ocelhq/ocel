package bootstrap

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/cli/internal/clitest"
	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/proto/provider/contract/v1/contractv1connect"
	"github.com/ocelhq/ocel/pkg/provider/fake"
)

func TestBootstrapDestroySendsTheEdgeTheProjectDeclared(t *testing.T) {
	cases := []struct {
		name        string
		declaration string
		want        string
	}{
		{"an omitted edge names none, leaving the provider to choose", "", ""},
		{"a declared direct edge names it", "edge: \"direct\"", "direct"},
		{"a declared relay edge names it", "edge: \"relay\"", "relay"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			project, invocation := bootstrapProject(t, tc.declaration)
			project.Provider.FakeBootstrap().SetRaisedEdges(fake.KindRelay)

			var stdout bytes.Buffer
			clitest.AttachTerminalSink(invocation, &stdout)
			if err := RunDestroy(context.Background(), invocation, project.Root, environmentv1.Tier_TIER_PRODUCTION, Options{Yes: true}, &stdout, strings.NewReader("")); err != nil {
				t.Fatalf("RunDestroy err = %v; stdout=%s", err, stdout.String())
			}

			planned := clitest.RequestsTo[*contractv1.BootstrapScope](t, project.Requests, contractv1connect.ProviderServicePlanRemoveBootstrapProcedure)
			removed := clitest.RequestsTo[*contractv1.BootstrapScope](t, project.Requests, contractv1connect.ProviderServiceRemoveBootstrapProcedure)
			if len(planned) != 1 || len(removed) != 1 {
				t.Fatalf("destroy asked for %d plans and %d removals, want the plan and the teardown", len(planned), len(removed))
			}
			for _, scope := range []*contractv1.BootstrapScope{planned[0], removed[0]} {
				if got := scope.GetEdge().GetKind(); got != tc.want {
					t.Errorf("provider was sent edge %q, want %q", got, tc.want)
				}
			}
			if !strings.Contains(stdout.String(), "fronted by the relay edge") {
				t.Errorf("stdout = %q, want the plan to name the edge left installed in the account, not the one this run selected", stdout.String())
			}
		})
	}
}
