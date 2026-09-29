package bootstrap

import (
	"bytes"
	"context"
	"strings"
	"testing"

	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/proto/provider/contract/v1/contractv1connect"

	"github.com/ocelhq/ocel/cli/internal/cli/clitest"
)

func TestOnlyWhatRendersDependentsPaysForThem(t *testing.T) {
	t.Run("bootstrap reads the catalogue, then plans the apply it is about to send", func(t *testing.T) {
		project, deps := bootstrapProject(t, "", featureISR)

		var stdout, stderr bytes.Buffer
		clitest.AttachTerminalSink(deps, &stdout)
		if err := Run(context.Background(), deps, project.Root, environmentv1.Tier_TIER_PRODUCTION, Options{Yes: true}, &stdout, &stderr, strings.NewReader("")); err != nil {
			t.Fatalf("runBootstrap err = %v; stderr=%s", err, stderr.String())
		}
		described := clitest.RequestsTo[*contractv1.DescribeBootstrapRequest](t, project.Requests, contractv1connect.ProviderServiceDescribeBootstrapProcedure)
		if len(described) != 1 || !described[0].GetWithDependents() {
			t.Errorf("the catalogue was read %v; a provider that draws no plan leaves the dependent names nowhere else to come from", described)
		}
		planned := clitest.RequestsTo[*contractv1.BootstrapRequest](t, project.Requests, contractv1connect.ProviderServiceBootstrapProcedure)
		if len(planned) == 0 || !planned[0].GetDry() || strings.Join(planned[0].GetFeatures(), ",") != featureISR || planned[0].GetForce() {
			t.Errorf("the provider was asked to plan %v, want the first ask to be the apply it would send, drawn dry", planned)
		}
	})
}
