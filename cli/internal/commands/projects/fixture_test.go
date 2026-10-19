package projects

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/ocelhq/ocel/cli/internal/clitest"
	"github.com/ocelhq/ocel/cli/internal/commands"
	"github.com/ocelhq/ocel/cli/internal/terminal"
	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/naming"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/proto/provider/contract/v1/contractv1connect"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/fake"
	"github.com/ocelhq/ocel/pkg/records"
	"github.com/ocelhq/ocel/pkg/router"
	"github.com/ocelhq/ocel/pkg/stackrecords"
)

var (
	productionDeployedAt = time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)
	previewDeployedAt    = time.Date(2026, 10, 5, 9, 0, 0, 0, time.UTC)
	blogDeployedAt       = time.Date(2026, 9, 14, 9, 0, 0, 0, time.UTC)
)

const (
	promotionID     = "promo-1"
	productionHost  = "shop.acme.com"
	webFunctionID   = "test-app-web-api"
	webFunctionLink = "https://console.fake.example/" + webFunctionID
)

func deployedToBothTiers(t *testing.T) clitest.FakeProject {
	t.Helper()
	project := clitest.SetUpProject(t)
	clitest.Bootstrap(t, project.Provider, environment.TierPreview, fake.PartCache, fake.PartImages)
	relay := &stackrecords.ProjectEdge{Kind: fake.KindRelay}
	clitest.RecordProject(t, project, environment.TierProduction, clitest.FixtureSlug, stackrecords.Project{Edge: relay, UpdatedAt: productionDeployedAt.Unix()})
	clitest.RecordProject(t, project, environment.TierPreview, clitest.FixtureSlug, stackrecords.Project{Edge: relay, UpdatedAt: previewDeployedAt.Unix()})
	clitest.RecordStacks(t, project, environment.TierProduction,
		naming.InfraStack(stackrecords.ProductionEnv), naming.AppStack(stackrecords.ProductionEnv, "web", naming.NewReleaseToken("b1", "")))
	clitest.RecordPromotions(t, project, promotionID)
	clitest.RecordStacks(t, project, environment.TierPreview,
		naming.InfraStack("pr-1"), naming.InfraStack("pr-2"), naming.AppStack("pr-1", "web", naming.NewReleaseToken("b1", "")))
	clitest.RecordEdgeStack(t, project, environment.TierPreview, fake.KindRelay)
	return project
}

func deployedToProductionOnly(t *testing.T) clitest.FakeProject {
	t.Helper()
	project := clitest.SetUpProject(t)
	clitest.RecordProject(t, project, environment.TierProduction, clitest.FixtureSlug, stackrecords.Project{Edge: &stackrecords.ProjectEdge{Kind: fake.KindRelay}, UpdatedAt: productionDeployedAt.Unix()})
	clitest.RecordStacks(t, project, environment.TierProduction,
		naming.InfraStack(stackrecords.ProductionEnv), naming.AppStack(stackrecords.ProductionEnv, "web", naming.NewReleaseToken("b1", "")))
	clitest.RecordEdgeStack(t, project, environment.TierProduction, fake.KindRelay)
	return project
}

func recordBlog(t *testing.T, project clitest.FakeProject, recorded stackrecords.Project) {
	t.Helper()
	recorded.UpdatedAt = blogDeployedAt.Unix()
	clitest.RecordProject(t, project, environment.TierProduction, "blog", recorded)
	stack := naming.InfraStack(stackrecords.ProductionEnv)
	if err := stackrecords.Write(context.Background(), project.Provider.KeyValues(), environment.TierProduction, "blog", stack, stackrecords.Stack{Kind: provider.StackInfra}); err != nil {
		t.Fatalf("record blog's stack %s: %v", stack, err)
	}
}

func recordWebFunction(t *testing.T, project clitest.FakeProject) {
	t.Helper()
	stack := naming.AppStack(stackrecords.ProductionEnv, "web", naming.NewReleaseToken("b1", ""))
	recorded := stackrecords.Stack{
		Kind:      provider.StackApp,
		App:       "web",
		Release:   stack.Release.String(),
		Functions: []provider.Function{{Name: "api", Physical: webFunctionID}},
	}
	if err := stackrecords.Write(context.Background(), project.Provider.KeyValues(), environment.TierProduction, clitest.FixtureSlug, stack, recorded); err != nil {
		t.Fatalf("record stack %s: %v", stack, err)
	}
}

func serveOnProductionHost(t *testing.T, project clitest.FakeProject) {
	t.Helper()
	ctx := context.Background()
	store := project.Provider.KeyValues()
	name := stackrecords.EdgeStackKey(environment.TierProduction, clitest.FixtureSlug)
	recorded, err := records.ReadOrEmpty(ctx, store, name)
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	var state stackrecords.EdgeState
	if err := json.Unmarshal(recorded.Value, &state); err != nil {
		t.Fatalf("decode %s: %v", name, err)
	}
	state.Hosts = map[string]stackrecords.HostnameState{productionHost: {Pointer: router.DefaultPointer}}
	if recorded.Value, err = json.Marshal(state); err != nil {
		t.Fatalf("encode %s: %v", name, err)
	}
	if _, err := store.Write(ctx, recorded); err != nil {
		t.Fatalf("record %s: %v", name, err)
	}
}

func onATerminal(invocation *commands.Invocation) {
	invocation.Presentation = func(io.Writer) terminal.Presentation {
		return terminal.Resolve(terminal.Conditions{TTY: true})
	}
}

func asJSON(invocation *commands.Invocation) {
	invocation.Presentation = clitest.ResolveJSONPresentation
}

type ran struct {
	stdout string
	run    string
	err    error
}

func runProjects(t *testing.T, invocation commands.Invocation, root string, stdin io.Reader, args ...string) ran {
	t.Helper()
	t.Chdir(root)
	var stdout, events bytes.Buffer
	clitest.AttachTerminalSink(invocation, &events)
	cmd := NewCommand(invocation)
	cmd.SetArgs(args)
	cmd.SetOut(&stdout)
	cmd.SetErr(io.Discard)
	cmd.SetIn(stdin)
	cmd.SilenceUsage, cmd.SilenceErrors = true, true
	err := cmd.ExecuteContext(context.Background())
	return ran{stdout: stdout.String(), run: events.String(), err: err}
}

func runDestroyCommand(t *testing.T, invocation commands.Invocation, root string, stdin string, args ...string) ran {
	t.Helper()
	t.Chdir(root)
	var stdout bytes.Buffer
	clitest.AttachTerminalSink(invocation, &stdout)
	cmd := NewCommand(invocation)
	cmd.SetArgs(append([]string{"destroy"}, args...))
	cmd.SetOut(&stdout)
	cmd.SetErr(io.Discard)
	cmd.SetIn(strings.NewReader(stdin))
	cmd.SilenceUsage, cmd.SilenceErrors = true, true
	err := cmd.ExecuteContext(context.Background())
	return ran{stdout: stdout.String(), run: stdout.String(), err: err}
}

func rowOf(t *testing.T, out, first string) []string {
	t.Helper()
	for _, line := range strings.Split(out, "\n") {
		if fields := strings.Fields(line); len(fields) > 0 && fields[0] == first {
			return fields
		}
	}
	t.Fatalf("output has no row for %s:\n%s", first, out)
	return nil
}

func removals(t *testing.T, project clitest.FakeProject) (planned, removed []*contractv1.ProjectRequest) {
	t.Helper()
	planned = clitest.RequestsTo[*contractv1.ProjectRequest](t, project.Requests, contractv1connect.ProviderServicePlanRemoveProjectProcedure)
	removed = clitest.RequestsTo[*contractv1.ProjectRequest](t, project.Requests, contractv1connect.ProviderServiceRemoveProjectProcedure)
	return planned, removed
}

func recordedStacks(t *testing.T, project clitest.FakeProject, tier environment.Tier, slug string) []stackrecords.NamedStack {
	t.Helper()
	recorded, err := stackrecords.List(context.Background(), project.Provider.KeyValues(), tier, slug)
	if err != nil {
		t.Fatalf("list the recorded stacks: %v", err)
	}
	return recorded
}
