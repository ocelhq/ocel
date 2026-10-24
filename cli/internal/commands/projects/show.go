package projects

import (
	"cmp"
	"context"
	"fmt"
	"io"
	"maps"
	"slices"
	"strings"

	"github.com/spf13/cobra"

	"github.com/ocelhq/ocel/cli/internal/commands"
	"github.com/ocelhq/ocel/cli/internal/english"
	"github.com/ocelhq/ocel/cli/internal/readiness"
	"github.com/ocelhq/ocel/cli/internal/terminal"
	"github.com/ocelhq/ocel/pkg/progress"
	resultv1 "github.com/ocelhq/ocel/pkg/proto/cli/result/v1"
	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/proto/provider/contract/v1/contractv1connect"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/stackrecords"
)

const (
	showCommand = "ocel projects show"
	consoleMark = " ↗"
	blockIndent = "  "
)

type showOptions struct {
	slug  string
	links bool
}

func newShowCommand(invocation commands.Invocation) *cobra.Command {
	var opts showOptions
	cmd := &cobra.Command{
		Use:   "show [slug]",
		Short: "Show a project's environments and every resource Ocel created for it",
		Long: "Show a project's environments, the promotion each serves and its URL, then every resource Ocel " +
			"created for it, grouped by environment, and the bootstrap parts it shares with other projects.\n\n" +
			"Shows this directory's project unless given another's slug. A resource marked ↗ has a page in the " +
			"provider's console: on a terminal that supports it the id links there, and --links prints each URL.",
		Example: "  $ ocel projects show\n" +
			"  $ ocel projects show blog --links",
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) > 0 {
				opts.slug = args[0]
			}
			return runShow(cmd.Context(), invocation, opts, cmd.OutOrStdout())
		},
	}
	cmd.Flags().BoolVar(&opts.links, "links", false, "Print each resource's console URL in a column of its own")
	return commands.DeclareResult(commands.DeclareReadOnly(commands.ReserveStdout(cmd)), &resultv1.ProjectShowResult{})
}

func runShow(ctx context.Context, invocation commands.Invocation, opts showOptions, stdout io.Writer) error {
	target, err := resolveTarget(ctx, invocation)
	if err != nil {
		return err
	}
	cfg := target.Project
	slug := cmp.Or(opts.slug, cfg.Slug)
	req := &contractv1.DescribeProjectRequest{Slug: slug}
	var described *contractv1.DescribeProjectResponse
	var identity *contractv1.Identity
	err = invocation.WithProvider(ctx, cfg, showCommand, accountOptions(target), func(ctx context.Context, p commands.ProviderRun) error {
		identity = p.Preflight.Response.GetIdentity()
		span := p.Check.Child(slug, progress.Reading.Title("what Ocel created for "+slug))
		err := p.Provider.Call(ctx, func(client contractv1connect.ProviderServiceClient) (err error) {
			described, err = client.DescribeProject(ctx, req)
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
		return terminal.WriteResultJSON(stdout, showResult(slug, described))
	}
	renderShow(stdout, present, identity, slug, described, opts)
	return nil
}

func showResult(slug string, described *contractv1.DescribeProjectResponse) *resultv1.ProjectShowResult {
	result := &resultv1.ProjectShowResult{Slug: slug, Edge: described.GetProject().GetEdge()}
	for _, env := range described.GetEnvironments() {
		result.Environments = append(result.Environments, &resultv1.ProjectEnvironmentStatus{
			Tier:        env.GetEnvironment().GetTier(),
			Environment: env.GetEnvironment().GetIdentity(),
			PromotionId: env.GetPromotionId(),
			Releases:    env.GetReleases(),
			Urls:        env.GetUrls(),
		})
	}
	for _, group := range described.GetResources() {
		result.Resources = append(result.Resources, &resultv1.ProjectResourceGroup{
			Tier:        group.GetTier(),
			Environment: group.GetEnvironment(),
			Stack:       group.GetStack(),
			Resources:   physicalResults(group.GetResources()),
		})
	}
	for _, shared := range described.GetShared() {
		result.Shared = append(result.Shared, &resultv1.ProjectSharedPart{
			Tier:      shared.GetTier(),
			Name:      shared.GetPart().GetName(),
			UsedBy:    shared.GetPart().GetUsedBy(),
			Resources: physicalResults(shared.GetPart().GetResources()),
		})
	}
	return result
}

func physicalResults(resources []*contractv1.PhysicalResource) []*resultv1.PhysicalResource {
	out := make([]*resultv1.PhysicalResource, 0, len(resources))
	for _, resource := range resources {
		out = append(out, &resultv1.PhysicalResource{
			Kind:       resource.GetKind(),
			Id:         resource.GetId(),
			Location:   resource.GetLocation(),
			ConsoleUrl: resource.GetConsoleUrl(),
		})
	}
	return out
}

func renderShow(out io.Writer, present terminal.Presentation, identity *contractv1.Identity, slug string, described *contractv1.DescribeProjectResponse, opts showOptions) {
	palette := present.Palette()
	var edge []string
	if kind := described.GetProject().GetEdge(); kind != "" {
		edge = append(edge, "edge "+kind)
	}
	fmt.Fprintln(out, identityHeader(palette, identity, []string{slug}, edge...))
	renderEnvironments(out, palette, described.GetEnvironments())
	renderResources(out, present, described.GetResources(), opts)
	renderShared(out, palette, slug, described.GetShared())
}

func renderEnvironments(out io.Writer, palette terminal.Palette, environments []*contractv1.ProjectEnvironment) {
	if len(environments) == 0 {
		return
	}
	fmt.Fprintf(out, "\n%s\n", palette.Heading("Environments"))
	table := terminal.NewTable(
		terminal.Column{Head: "ENV"},
		terminal.Column{Head: "TIER"},
		terminal.Column{Head: "PROMOTION"},
		terminal.Column{Head: "APPS"},
		terminal.Column{Head: "URL"},
	)
	for _, env := range environments {
		apps := appCells(palette, env.GetReleases())
		table.Row(
			palette.Bold(environmentTitle(env.GetEnvironment().GetTier(), env.GetEnvironment().GetIdentity())),
			readiness.TierName(env.GetEnvironment().GetTier()),
			orUnknown(terminal.ShortID(env.GetPromotionId())),
			apps[0],
			firstURL(env.GetUrls()))
		for _, app := range apps[1:] {
			table.Row("", "", "", app)
		}
	}
	fmt.Fprint(out, table.Render(palette, blockIndent))
}

func environmentTitle(tier environmentv1.Tier, identity string) string {
	if tier == environmentv1.Tier_TIER_PREVIEW {
		return identity
	}
	return stackrecords.ProductionEnv
}

func appCells(palette terminal.Palette, releases map[string]string) []string {
	if len(releases) == 0 {
		return []string{unknownCell}
	}
	cells := make([]string, 0, len(releases))
	for _, app := range slices.Sorted(maps.Keys(releases)) {
		cells = append(cells, app+" "+palette.Muted(shortBuild(releases[app])))
	}
	return cells
}

func shortBuild(release string) string {
	parsed, err := provider.ParseRelease(release)
	if err != nil {
		return terminal.ShortID(release)
	}
	return terminal.ShortID(parsed.BuildID())
}

func firstURL(urls []string) string {
	if len(urls) == 0 {
		return unknownCell
	}
	return urls[0]
}

type resourceBlock struct {
	context   []string
	resources []*contractv1.PhysicalResource
}

func groupResources(groups []*contractv1.ResourceGroup) []resourceBlock {
	var blocks []resourceBlock
	listed := map[string]bool{}
	for _, group := range groups {
		context := resourceBlockContext(group)
		at := slices.IndexFunc(blocks, func(block resourceBlock) bool { return slices.Equal(block.context, context) })
		if at < 0 {
			blocks = append(blocks, resourceBlock{context: context})
			at = len(blocks) - 1
		}
		for _, resource := range group.GetResources() {
			key := strings.Join(append(slices.Clone(context), resource.GetKind(), resource.GetId()), "\x00")
			if listed[key] {
				continue
			}
			listed[key] = true
			blocks[at].resources = append(blocks[at].resources, resource)
		}
	}
	return blocks
}

func resourceBlockContext(group *contractv1.ResourceGroup) []string {
	if group.GetEnvironment() == "" {
		return []string{readiness.TierName(group.GetTier()), "project-wide"}
	}
	return []string{environmentTitle(group.GetTier(), group.GetEnvironment())}
}

func renderResources(out io.Writer, present terminal.Presentation, groups []*contractv1.ResourceGroup, opts showOptions) {
	palette := present.Palette()
	for _, block := range groupResources(groups) {
		fmt.Fprintf(out, "\n%s\n", palette.Heading("Resources", block.context...))
		columns := []terminal.Column{{Head: "KIND"}, {Head: "ID"}}
		if opts.links {
			columns = append(columns, terminal.Column{Head: "URL"})
		}
		table := terminal.NewTable(columns...)
		for _, resource := range block.resources {
			kind := kindCell(palette, resource.GetKind())
			if opts.links {
				table.Row(kind, markedID(resource, resource.GetId()), orUnknown(resource.GetConsoleUrl()))
				continue
			}
			table.Row(kind, markedID(resource, present.Hyperlink(resource.GetId(), resource.GetConsoleUrl())))
		}
		fmt.Fprint(out, table.Render(palette, blockIndent))
	}
}

func kindCell(palette terminal.Palette, kind string) string {
	at := strings.LastIndex(kind, ":")
	if at < 0 {
		return kind
	}
	return palette.Muted(kind[:at+1]) + kind[at+1:]
}

func markedID(resource *contractv1.PhysicalResource, id string) string {
	if resource.GetConsoleUrl() == "" {
		return id
	}
	return id + consoleMark
}

func renderShared(out io.Writer, palette terminal.Palette, slug string, shared []*contractv1.SharedPart) {
	var tiers []environmentv1.Tier
	for _, each := range shared {
		if !slices.Contains(tiers, each.GetTier()) {
			tiers = append(tiers, each.GetTier())
		}
	}
	for _, tier := range tiers {
		fmt.Fprintf(out, "\n%s\n", palette.Heading("Shared", readiness.TierName(tier)))
		table := terminal.NewTable(
			terminal.Column{Head: "PART"},
			terminal.Column{Head: "SHARED WITH"},
			terminal.Column{Head: "KIND"},
			terminal.Column{Head: "ID"},
		)
		for _, each := range shared {
			if each.GetTier() != tier {
				continue
			}
			part := each.GetPart()
			name, with := palette.Bold(part.GetName()), sharedWith(slug, part)
			if len(part.GetResources()) == 0 {
				table.Row(name, with, palette.Muted("no resources"))
				continue
			}
			for _, resource := range part.GetResources() {
				table.Row(name, with, kindCell(palette, resource.GetKind()), resource.GetId())
				name, with = "", ""
			}
		}
		fmt.Fprint(out, table.Render(palette, blockIndent))
	}
}

func sharedWith(slug string, part *contractv1.BootstrapPart) string {
	others := len(slices.DeleteFunc(slices.Clone(part.GetUsedBy()), func(user string) bool { return user == slug }))
	if others == 0 {
		return unknownCell
	}
	return english.Count(others, "other project")
}
