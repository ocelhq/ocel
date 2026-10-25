package projects

import (
	"cmp"
	"context"
	"fmt"
	"io"
	"slices"
	"strconv"

	"github.com/spf13/cobra"

	"github.com/ocelhq/ocel/cli/internal/commands"
	"github.com/ocelhq/ocel/cli/internal/english"
	"github.com/ocelhq/ocel/cli/internal/terminal"
	"github.com/ocelhq/ocel/pkg/progress"
	resultv1 "github.com/ocelhq/ocel/pkg/proto/cli/result/v1"
	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/proto/provider/contract/v1/contractv1connect"
)

const (
	listCommand = "ocel projects"
	liveCell    = "live"
)

func newListCommand(invocation commands.Invocation) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "ls",
		Short: "List the projects Ocel has deployed into this account",
		Args:  cobra.NoArgs,
		RunE:  listProjects(invocation),
	}
	return commands.DeclareResult(commands.DeclareReadOnly(commands.ReserveStdout(cmd)), &resultv1.ProjectListResult{})
}

func listProjects(invocation commands.Invocation) func(*cobra.Command, []string) error {
	return func(cmd *cobra.Command, _ []string) error {
		return runList(cmd.Context(), invocation, cmd.OutOrStdout())
	}
}

func runList(ctx context.Context, invocation commands.Invocation, stdout io.Writer) error {
	target, err := resolveTarget(ctx, invocation)
	if err != nil {
		return err
	}
	cfg := target.Project
	var listed *contractv1.ListProjectsResponse
	var identity *contractv1.Identity
	err = invocation.WithProvider(ctx, cfg, listCommand, accountOptions(target), func(ctx context.Context, p commands.ProviderRun) error {
		identity = p.Preflight.Response.GetIdentity()
		span := p.Check.Child(cfg.Slug, progress.Reading.Title("the projects in this account"))
		err := p.Provider.Call(ctx, func(client contractv1connect.ProviderServiceClient) (err error) {
			listed, err = client.ListProjects(ctx, &contractv1.ListProjectsRequest{})
			return err
		})
		span.End(err)
		return err
	})
	if err != nil {
		return err
	}
	present := invocation.Presentation(stdout)
	if present.Format == terminal.FormatJSON {
		return terminal.WriteResultJSON(stdout, listResult(listed.GetProjects()))
	}
	renderList(stdout, present, identity, listed.GetProjects())
	return nil
}

func listResult(projects []*contractv1.ProjectSummary) *resultv1.ProjectListResult {
	result := &resultv1.ProjectListResult{}
	for _, summary := range projects {
		result.Projects = append(result.Projects, &resultv1.ProjectSummary{
			Slug:                  summary.GetSlug(),
			Tiers:                 summary.GetTiers(),
			Edge:                  summary.GetEdge(),
			ProductionPromotionId: summary.GetProductionPromotionId(),
			Previews:              summary.GetPreviews(),
			LastDeployedAt:        terminal.EpochRFC3339(summary.GetUpdatedAt()),
		})
	}
	return result
}

func renderList(out io.Writer, present terminal.Presentation, identity *contractv1.Identity, projects []*contractv1.ProjectSummary) {
	palette := present.Palette()
	if len(projects) == 0 {
		if header := identityHeader(palette, identity, nil, "no projects"); header != "" {
			fmt.Fprintln(out, header)
			return
		}
		fmt.Fprintln(out, "This account has no projects")
		return
	}
	if header := identityHeader(palette, identity, nil); header != "" {
		fmt.Fprintf(out, "%s\n\n", header)
	}
	table := terminal.NewTable(
		terminal.Column{Head: "PROJECT"},
		terminal.Column{Head: "PRODUCTION"},
		terminal.Column{Head: "PREVIEWS", Align: terminal.AlignRight},
		terminal.Column{Head: "EDGE"},
		terminal.Column{Head: "LAST DEPLOY"},
	)
	for _, summary := range projects {
		table.Row(palette.Bold(summary.GetSlug()), productionCell(palette, summary), previewsCell(summary), cmp.Or(summary.GetEdge(), unknownCell), present.EpochAge(summary.GetUpdatedAt()))
	}
	fmt.Fprint(out, table.Render(palette, "  "))
	fmt.Fprintf(out, "\n%s\n", palette.Muted(english.Count(len(projects), "project")))
}

func productionCell(palette terminal.Palette, summary *contractv1.ProjectSummary) string {
	if summary.GetProductionPromotionId() == "" {
		return palette.Health(terminal.Inactive, "none")
	}
	return palette.Health(terminal.Healthy, liveCell)
}

func previewsCell(summary *contractv1.ProjectSummary) string {
	if !slices.Contains(summary.GetTiers(), environmentv1.Tier_TIER_PREVIEW) {
		return unknownCell
	}
	return strconv.Itoa(int(summary.GetPreviews()))
}
