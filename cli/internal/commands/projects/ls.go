package projects

import (
	"context"
	"fmt"
	"io"
	"slices"
	"strconv"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/ocelhq/ocel/cli/internal/commands"
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
	deployDate  = "2006-01-02"
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
	if invocation.Presentation(stdout).Format == terminal.FormatJSON {
		return terminal.WriteResultJSON(stdout, listResult(listed.GetProjects()))
	}
	renderList(stdout, identity, listed.GetProjects())
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

func renderList(out io.Writer, identity *contractv1.Identity, projects []*contractv1.ProjectSummary) {
	header := identityHeader(identity, nil)
	if len(projects) == 0 {
		if header == "" {
			fmt.Fprintln(out, "This account has no projects")
			return
		}
		fmt.Fprintf(out, "%s%sno projects\n", header, cellSep)
		return
	}
	if header != "" {
		fmt.Fprintln(out, header)
	}
	tw := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "PROJECT\tPRODUCTION\tPREVIEWS\tEDGE\tLAST DEPLOY")
	for _, summary := range projects {
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", summary.GetSlug(), productionCell(summary), previewsCell(summary), orUnknown(summary.GetEdge()), lastDeployCell(summary))
	}
	_ = tw.Flush()
}

func productionCell(summary *contractv1.ProjectSummary) string {
	if summary.GetProductionPromotionId() == "" {
		return unknownCell
	}
	return liveCell
}

func previewsCell(summary *contractv1.ProjectSummary) string {
	if !slices.Contains(summary.GetTiers(), environmentv1.Tier_TIER_PREVIEW) {
		return unknownCell
	}
	return strconv.Itoa(int(summary.GetPreviews()))
}

func lastDeployCell(summary *contractv1.ProjectSummary) string {
	if summary.GetUpdatedAt() == 0 {
		return unknownCell
	}
	return time.Unix(summary.GetUpdatedAt(), 0).UTC().Format(deployDate)
}
