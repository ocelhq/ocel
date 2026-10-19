package projects

import (
	"cmp"
	"context"
	"fmt"
	"io"
	"maps"
	"slices"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/ocelhq/ocel/cli/internal/commands"
	"github.com/ocelhq/ocel/cli/internal/readiness"
	"github.com/ocelhq/ocel/cli/internal/terminal"
	"github.com/ocelhq/ocel/pkg/progress"
	resultv1 "github.com/ocelhq/ocel/pkg/proto/cli/result/v1"
	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/proto/provider/contract/v1/contractv1connect"
	"github.com/ocelhq/ocel/pkg/stackrecords"
)

const (
	showCommand = "ocel projects show"
	consoleMark = " ↗"
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
	cfg, err := loadProject(ctx, invocation)
	if err != nil {
		return err
	}
	slug := cmp.Or(opts.slug, cfg.Slug)
	req := &contractv1.DescribeProjectRequest{Slug: slug}
	var described *contractv1.DescribeProjectResponse
	var identity *contractv1.Identity
	err = invocation.WithProvider(ctx, cfg, showCommand, accountOptions(), func(ctx context.Context, p commands.ProviderRun) error {
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
	var edge []string
	if kind := described.GetProject().GetEdge(); kind != "" {
		edge = append(edge, "edge "+kind)
	}
	fmt.Fprintln(out, identityHeader(identity, []string{slug}, edge...))
	renderEnvironments(out, described.GetEnvironments())
	renderResources(out, present, described.GetResources(), opts)
	renderShared(out, slug, described.GetShared())
}

func renderEnvironments(out io.Writer, environments []*contractv1.ProjectEnvironment) {
	if len(environments) == 0 {
		return
	}
	fmt.Fprintln(out)
	tw := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "ENVIRONMENT\tTIER\tPROMOTION\tAPPS\tURL")
	for _, env := range environments {
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n",
			environmentTitle(env.GetEnvironment().GetTier(), env.GetEnvironment().GetIdentity()),
			readiness.TierName(env.GetEnvironment().GetTier()),
			orUnknown(env.GetPromotionId()),
			appsCell(env.GetReleases()),
			firstURL(env.GetUrls()))
	}
	_ = tw.Flush()
}

func environmentTitle(tier environmentv1.Tier, identity string) string {
	if tier == environmentv1.Tier_TIER_PREVIEW {
		return identity
	}
	return stackrecords.ProductionEnv
}

func appsCell(releases map[string]string) string {
	if len(releases) == 0 {
		return unknownCell
	}
	pairs := make([]string, 0, len(releases))
	for _, app := range slices.Sorted(maps.Keys(releases)) {
		pairs = append(pairs, app+" "+releases[app])
	}
	return strings.Join(pairs, ", ")
}

func firstURL(urls []string) string {
	if len(urls) == 0 {
		return unknownCell
	}
	return urls[0]
}

type resourceBlock struct {
	title  string
	groups []*contractv1.ResourceGroup
}

func groupResources(groups []*contractv1.ResourceGroup) []resourceBlock {
	var blocks []resourceBlock
	for _, group := range groups {
		title := resourceBlockTitle(group)
		at := slices.IndexFunc(blocks, func(block resourceBlock) bool { return block.title == title })
		if at < 0 {
			blocks = append(blocks, resourceBlock{title: title})
			at = len(blocks) - 1
		}
		blocks[at].groups = append(blocks[at].groups, group)
	}
	return blocks
}

func resourceBlockTitle(group *contractv1.ResourceGroup) string {
	if group.GetEnvironment() == "" {
		return readiness.TierName(group.GetTier()) + ", project-wide"
	}
	return environmentTitle(group.GetTier(), group.GetEnvironment())
}

func renderResources(out io.Writer, present terminal.Presentation, groups []*contractv1.ResourceGroup, opts showOptions) {
	if len(groups) == 0 {
		return
	}
	fmt.Fprintln(out, "\nRESOURCES")
	for _, block := range groupResources(groups) {
		fmt.Fprintf(out, "\n%s\n", block.title)
		tw := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
		if opts.links {
			fmt.Fprintln(tw, "STACK\tKIND\tID\tURL")
		} else {
			fmt.Fprintln(tw, "STACK\tKIND\tID")
		}
		for _, group := range block.groups {
			for _, resource := range group.GetResources() {
				stack := orUnknown(group.GetStack())
				if opts.links {
					fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", stack, resource.GetKind(), markedID(resource, resource.GetId()), orUnknown(resource.GetConsoleUrl()))
					continue
				}
				fmt.Fprintf(tw, "%s\t%s\t%s\n", stack, resource.GetKind(), markedID(resource, present.Hyperlink(resource.GetId(), resource.GetConsoleUrl())))
			}
		}
		_ = tw.Flush()
	}
}

func markedID(resource *contractv1.PhysicalResource, id string) string {
	if resource.GetConsoleUrl() == "" {
		return id
	}
	return id + consoleMark
}

func renderShared(out io.Writer, slug string, shared []*contractv1.SharedPart) {
	if len(shared) == 0 {
		return
	}
	fmt.Fprintln(out, "\nSHARED")
	for _, each := range shared {
		part := each.GetPart()
		fmt.Fprintf(out, "\n%s in %s\n", sharesLine(slug, part), readiness.TierName(each.GetTier()))
		tw := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
		for _, resource := range part.GetResources() {
			fmt.Fprintf(tw, "  %s\t%s\n", resource.GetKind(), resource.GetId())
		}
		_ = tw.Flush()
	}
}

func sharesLine(slug string, part *contractv1.BootstrapPart) string {
	others := len(slices.DeleteFunc(slices.Clone(part.GetUsedBy()), func(user string) bool { return user == slug }))
	switch others {
	case 0:
		return fmt.Sprintf("%s alone uses %s", slug, part.GetName())
	case 1:
		return fmt.Sprintf("%s shares %s with 1 other project", slug, part.GetName())
	}
	return fmt.Sprintf("%s shares %s with %d other projects", slug, part.GetName(), others)
}
