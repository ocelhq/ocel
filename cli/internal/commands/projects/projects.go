package projects

import (
	"context"
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/ocelhq/ocel/cli/internal/commands"
	"github.com/ocelhq/ocel/cli/internal/readiness"
	"github.com/ocelhq/ocel/cli/internal/terminal"
	resultv1 "github.com/ocelhq/ocel/pkg/proto/cli/result/v1"
	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
)

const (
	unknownCell = terminal.NoValue
)

func NewCommand(invocation commands.Invocation) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "projects",
		Short: "List, show and destroy the projects Ocel has deployed into this account",
		Long: "List the projects Ocel has deployed into the account, region and namespace this directory's " +
			"provider reaches: whether production is live, how many previews each has, the edge that serves it and " +
			"when it last deployed.\n\n" +
			"`ocel projects show` lists one project's environments and every resource Ocel created for it; " +
			"`ocel projects destroy` takes a project down in both tiers.",
		Example: "  $ ocel projects\n" +
			"  $ ocel projects show blog --links\n" +
			"  $ ocel projects destroy --tier preview",
		Args: cobra.NoArgs,
		RunE: listProjects(invocation),
	}
	cmd.AddCommand(newListCommand(invocation), newShowCommand(invocation), newDestroyCommand(invocation))
	return commands.DeclareResult(commands.DeclareReadOnly(commands.ReserveStdout(cmd)), &resultv1.ProjectListResult{})
}

func workingDirectory() (string, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("determine working directory: %w", err)
	}
	return cwd, nil
}

func resolveTarget(ctx context.Context, invocation commands.Invocation) (commands.Target, error) {
	cwd, err := workingDirectory()
	if err != nil {
		return commands.Target{}, err
	}
	return invocation.ResolveTarget(ctx, cwd)
}

func accountOptions(target commands.Target) commands.OpenOptions {
	return commands.OpenOptions{Intent: commands.UseAccount, Tier: environmentv1.Tier_TIER_PRODUCTION, Cloud: target.Cloud}
}

func identityHeader(palette terminal.Palette, identity *contractv1.Identity, before []string, after ...string) string {
	cells := append(append(before, readiness.IdentityCells(identity)...), after...)
	if len(cells) == 0 {
		return ""
	}
	return palette.Heading(cells[0], cells[1:]...)
}
