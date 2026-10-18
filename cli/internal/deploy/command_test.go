package deploy_test

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/ocelhq/ocel/cli/internal/clitest"
	deploycommand "github.com/ocelhq/ocel/cli/internal/commands/deploy"
	"github.com/ocelhq/ocel/cli/internal/deploy"
	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/proto/provider/contract/v1/contractv1connect"
)

func runPreviewCommand(t *testing.T, fixture clitest.FakeProject, dependencies deploy.Dependencies, stdin string, args ...string) (string, error) {
	t.Helper()
	t.Chdir(fixture.Root)
	var stdout, stderr bytes.Buffer
	clitest.AttachTerminalSink(dependencies.Invocation, &stdout)
	cmd := deploycommand.NewPreviewCommand(dependencies)
	cmd.SetArgs(args)
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)
	cmd.SetIn(strings.NewReader(stdin))
	err := cmd.ExecuteContext(context.Background())
	return stdout.String() + stderr.String(), err
}

func TestAPreviewNamedOnTheCommandLineIsEphemeralUnlessPersistent(t *testing.T) {
	for _, tc := range []struct {
		name      string
		args      []string
		lifecycle environmentv1.Lifecycle
	}{
		{"preview up <name> deploys an ephemeral preview of that name", []string{"up", "staging"}, environmentv1.Lifecycle_LIFECYCLE_EPHEMERAL},
		{"preview up <name> --persistent deploys a persistent preview of that name", []string{"up", "staging", "--persistent"}, environmentv1.Lifecycle_LIFECYCLE_PERSISTENT},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fixture := deploy.SetUpPreviewProject(t)
			deploy.CoverPreview(t, fixture, "staging")

			out, err := runPreviewCommand(t, fixture, deploy.PreviewDependencies("feature/login", ""), "", tc.args...)
			if err != nil {
				t.Fatalf("ocel preview %v err = %v; out=%s", tc.args, err, out)
			}
			deploy.AssertEnvironment(t, deploy.SentDeploy(t, fixture).GetEnvironment(), tc.lifecycle, "staging")
		})
	}
}

func TestPreviewWithoutASubcommandTakesNoName(t *testing.T) {
	fixture := deploy.SetUpPreviewProject(t)

	out, err := runPreviewCommand(t, fixture, deploy.PreviewDependencies("feature/login", ""), "", "staging")
	if err == nil {
		t.Fatalf("ocel preview staging err = nil, want it refused: a name goes to ocel preview up, so no name can read as a subcommand; out=%s", out)
	}
	if !strings.Contains(err.Error(), "ocel preview up staging") {
		t.Errorf("err = %v, want it to point at ocel preview up staging", err)
	}
}

func TestPreviewPruneTakesItsNameOnTheCommandLine(t *testing.T) {
	fixture := deploy.SetUpPreviewProject(t)
	dependencies := deploy.PreviewDependencies("feature/login", "")
	deploy.PreviewUpNamed(t, fixture, dependencies, "staging")

	out, err := runPreviewCommand(t, fixture, dependencies, "", "prune", "staging", "--keep", "5")
	if err != nil {
		t.Fatalf("ocel preview prune staging err = %v; out=%s", err, out)
	}
	requests := clitest.RequestsTo[*contractv1.RemoveStalePromotionsRequest](t, fixture.Requests, contractv1connect.ProviderServiceRemoveStalePromotionsProcedure)
	if len(requests) != 1 || requests[0].GetEnvironment().GetIdentity() != "staging" || requests[0].GetKeepN() != 5 {
		t.Errorf("the CLI pruned %d environments, want staging pruned down to 5", len(requests))
	}
}

func TestEveryDeployingCommandTakesPrebuilt(t *testing.T) {
	for _, cmd := range []struct {
		name string
		want string
	}{
		{"deploy", "deploy"},
		{"preview", "preview"},
		{"preview up", "preview up"},
	} {
		t.Run("`ocel "+cmd.name+"` accepts --prebuilt", func(t *testing.T) {
			parts := strings.Fields(cmd.want)
			commands := map[string]*cobra.Command{
				"deploy":  deploycommand.NewCommand(deploy.Dependencies{}),
				"preview": deploycommand.NewPreviewCommand(deploy.Dependencies{}),
			}
			target := commands[parts[0]]
			for _, part := range parts[1:] {
				next, _, err := target.Find([]string{part})
				if err != nil || next == target {
					t.Fatalf("command %q not found: %v", cmd.want, err)
				}
				target = next
			}
			if target.Flags().Lookup("prebuilt") == nil {
				t.Errorf("`ocel %s` does not accept --prebuilt", cmd.want)
			}
		})
	}
}
