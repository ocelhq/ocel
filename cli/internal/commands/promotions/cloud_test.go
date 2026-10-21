package promotions

import (
	"bytes"
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/cli/internal/clitest"
	"github.com/ocelhq/ocel/pkg/environment"
	consolev1 "github.com/ocelhq/ocel/pkg/proto/console/v1"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/router"
)

func onOcelCloud(t *testing.T) (clitest.CloudProject, *bytes.Buffer) {
	t.Helper()
	project := clitest.SetUpCloudProject(t)
	recordPromotions(t, project.FakeProject, append(firstTwoPromotions(),
		router.Promotion{PromotionID: "promo-3", Ts: 3, Releases: map[string]string{"web": "build-3~fp4"}})...)
	return project, &bytes.Buffer{}
}

func TestPromotionsAndRollbackRunOnOcelCloudThroughTheProviderItServes(t *testing.T) {
	t.Run("promotions ls lists what Ocel Cloud promoted", func(t *testing.T) {
		project, stdout := onOcelCloud(t)
		invocation := clitest.NewInvocation()
		project.LogIn(&invocation)
		clitest.AttachTerminalSink(invocation, stdout)

		var stderr bytes.Buffer
		if err := runPromotionsList(context.Background(), invocation, project.Root, stdout, &stderr); err != nil {
			t.Fatalf("err = %v; stdout=%s", err, stdout)
		}
		if !strings.Contains(stdout.String(), "promo-2") || len(project.Remote.Calls()) == 0 {
			t.Fatalf("stdout = %q, want the promotions read from Ocel Cloud", stdout)
		}
		requireTokenScopes(t, project.Cloud, "cloud.apps.read")
	})

	t.Run("promotions prune reclaims on Ocel Cloud", func(t *testing.T) {
		project, stdout := onOcelCloud(t)
		invocation := clitest.NewInvocation()
		project.LogIn(&invocation)
		clitest.AttachTerminalSink(invocation, stdout)

		if err := runPromotionsPrune(context.Background(), invocation, project.Root, pruneOptions{keep: 1, yes: true}, stdout, strings.NewReader("")); err != nil {
			t.Fatalf("err = %v; stdout=%s", err, stdout)
		}
		history, err := project.Provider.Releases(environment.TierProduction, clitest.FixtureSlug).History(context.Background(), "")
		if err != nil || len(history) != 2 || history[1].PromotionID != "promo-2" {
			t.Fatalf("history = %+v, err = %v, want promo-1 reclaimed", history, err)
		}
		requireTokenScopes(t, project.Cloud, "cloud.apps.read", "cloud.apps.write")
	})

	t.Run("rollback moves Ocel Cloud's production back", func(t *testing.T) {
		project, stdout := onOcelCloud(t)
		invocation := clitest.NewInvocation()
		project.LogIn(&invocation)
		clitest.AttachTerminalSink(invocation, stdout)

		var stderr bytes.Buffer
		if err := runRollback(context.Background(), invocation, project.Root, rollbackOptions{yes: true}, stdout, &stderr, strings.NewReader("")); err != nil {
			t.Fatalf("err = %v; stdout=%s stderr=%s", err, stdout, stderr.String())
		}
		active, _, err := project.Provider.Releases(environment.TierProduction, clitest.FixtureSlug).ReadActive(context.Background(), "")
		if err != nil || active.Releases["web"] != "build-2~fp2" {
			t.Fatalf("production serves %+v, err = %v, want promo-2's builds again", active, err)
		}
		requireTokenScopes(t, project.Cloud, "cloud.apps.read", "cloud.apps.write")
	})
	t.Run("rollback reports a rollback deployment by the provider Ocel Cloud serves", func(t *testing.T) {
		project, stdout := onOcelCloud(t)
		project.Provider.FakeConnector().Runs(provider.ConnectorTarget{Fingerprint: "ocel/cloud"})
		project.Cloud.Link(t, project.Root)
		invocation := clitest.NewInvocation()
		project.LogIn(&invocation)
		clitest.AttachTerminalSink(invocation, stdout)

		var stderr bytes.Buffer
		if err := runRollback(context.Background(), invocation, project.Root, rollbackOptions{yes: true}, stdout, &stderr, strings.NewReader("")); err != nil {
			t.Fatalf("err = %v; stdout=%s stderr=%s", err, stdout, stderr.String())
		}
		reports := project.Cloud.ReportedDeployments()
		if len(reports) != 1 || stderr.Len() != 0 {
			t.Fatalf("the console received %d reports, stderr=%q, want the rollback reported", len(reports), stderr.String())
		}
		got := reports[0].GetDeployment()
		if got.GetKind() != consolev1.DeploymentKind_DEPLOYMENT_KIND_ROLLBACK || got.GetProvider().GetName() != "ocel" || got.GetTarget() != "ocel/cloud" {
			t.Errorf("deployment is a %v by %q on %q, want a rollback by ocel on ocel/cloud", got.GetKind(), got.GetProvider().GetName(), got.GetTarget())
		}
	})
}

func requireTokenScopes(t *testing.T, cloud *clitest.FakeOcelCloud, want ...string) {
	t.Helper()
	if len(cloud.TokenScopes) == 0 || slices.ContainsFunc(cloud.TokenScopes, func(got []string) bool { return !slices.Equal(got, want) }) {
		t.Fatalf("tokens issued with scopes %v, want each with exactly %v", cloud.TokenScopes, want)
	}
}
