package bootstrap

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/ocelhq/ocel/cli/internal/commands"
	"github.com/ocelhq/ocel/cli/internal/readiness"
	"github.com/ocelhq/ocel/cli/internal/terminal"
	"github.com/ocelhq/ocel/pkg/progress"
	resultv1 "github.com/ocelhq/ocel/pkg/proto/cli/result/v1"
	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/proto/provider/contract/v1/contractv1connect"
	"github.com/ocelhq/ocel/pkg/provider"
)

const (
	unknownCell   = "—"
	everyProject  = "every project"
	usersShown    = 3
	partCreating  = "creating"
	partReady     = "ready"
	partCurrent   = "current"
	partBehind    = "behind"
	addedDate     = "2006-01-02"
	statusCommand = "ocel bootstrap status"
)

type StatusOptions struct {
	Resources bool
}

type tierStatus struct {
	tier      environmentv1.Tier
	identity  *contractv1.Identity
	described *contractv1.DescribeBootstrapResponse
}

func newStatusCommand(invocation commands.Invocation) *cobra.Command {
	var opts StatusOptions
	cmd := &cobra.Command{
		Use:   "status [production|preview]",
		Short: "Show every part of a bootstrap: who added it, who uses it, what it costs idle and whether it is current",
		Long: "Show every part of a bootstrap: who added it, who uses it, what it costs idle and whether it is current.\n\n" +
			"Reads both environments unless one is named. --resources lists what each part holds.",
		Example: "  $ ocel bootstrap status\n" +
			"  $ ocel bootstrap status production --resources",
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			tiers, err := statusTiers(args)
			if err != nil {
				_ = cmd.Help()
				return err
			}
			cwd, err := os.Getwd()
			if err != nil {
				return fmt.Errorf("determine working directory: %w", err)
			}
			return RunStatus(cmd.Context(), invocation, cwd, tiers, opts, cmd.OutOrStdout())
		},
	}
	cmd.Flags().BoolVar(&opts.Resources, "resources", false, "List the resources each part holds")
	return commands.DeclareResult(commands.DeclareReadOnly(commands.ReserveStdout(cmd)), &resultv1.BootstrapStatusResult{})
}

func statusTiers(args []string) ([]environmentv1.Tier, error) {
	if len(args) == 0 {
		return nil, nil
	}
	tier, err := tierNamed(args[0], "show")
	if err != nil {
		return nil, err
	}
	return []environmentv1.Tier{tier}, nil
}

func RunStatus(ctx context.Context, invocation commands.Invocation, cwd string, tiers []environmentv1.Tier, opts StatusOptions, stdout io.Writer) error {
	if len(tiers) == 0 {
		tiers = []environmentv1.Tier{environmentv1.Tier_TIER_PRODUCTION, environmentv1.Tier_TIER_PREVIEW}
	}
	cfg, err := resolveProject(ctx, invocation, cwd)
	if err != nil {
		return err
	}
	var read []tierStatus
	opened := commands.OpenOptions{Tier: tiers[0], Require: readiness.Credentials, Slug: cfg.Slug}
	err = invocation.WithProvider(ctx, cfg, statusCommand, opened, func(ctx context.Context, p commands.ProviderRun) error {
		for _, tier := range tiers {
			span := p.Check.Child(readiness.TierName(tier), progress.Reading.Title("the "+readiness.TierName(tier)+" bootstrap"))
			var described *contractv1.DescribeBootstrapResponse
			err := p.Provider.Call(ctx, func(client contractv1connect.ProviderServiceClient) (err error) {
				described, err = client.DescribeBootstrap(ctx, &contractv1.DescribeBootstrapRequest{
					Tier:      tier,
					WithParts: true,
					Edge:      cfg.EdgeSelection(),
				})
				return err
			})
			span.End(err)
			if err != nil {
				return err
			}
			read = append(read, tierStatus{tier: tier, identity: p.Preflight.Response.GetIdentity(), described: described})
		}
		return nil
	})
	if err != nil {
		return err
	}
	if invocation.Presentation(stdout).Format == terminal.FormatJSON {
		return terminal.WriteResultJSON(stdout, statusResult(read))
	}
	for i, status := range read {
		if i > 0 {
			fmt.Fprintln(stdout)
		}
		renderTierStatus(stdout, status, opts)
	}
	return nil
}

func statusResult(read []tierStatus) *resultv1.BootstrapStatusResult {
	result := &resultv1.BootstrapStatusResult{}
	for _, status := range read {
		tier := &resultv1.BootstrapTierStatus{Tier: status.tier, Present: status.described.GetBootstrap().GetPresent()}
		for _, part := range status.described.GetParts() {
			out := &resultv1.BootstrapPartStatus{
				Name:             part.GetName(),
				Summary:          part.GetSummary(),
				AddedBy:          part.GetAddedBy(),
				AddedAt:          terminal.EpochRFC3339(part.GetAddedAt()),
				State:            partState(part),
				Current:          part.GetCurrent(),
				UsedBy:           part.GetUsedBy(),
				IdleMonthlyFixed: part.IdleMonthlyFixed,
				Currency:         part.GetCurrency(),
			}
			for _, resource := range part.GetResources() {
				out.Resources = append(out.Resources, &resultv1.BootstrapPartResource{Kind: resource.GetKind(), Id: resource.GetId()})
			}
			tier.Parts = append(tier.Parts, out)
		}
		result.Tiers = append(result.Tiers, tier)
	}
	return result
}

func partState(part *contractv1.BootstrapPart) string {
	if part.GetState() == contractv1.BootstrapPartState_BOOTSTRAP_PART_STATE_CREATING {
		return partCreating
	}
	return partReady
}

func renderTierStatus(out io.Writer, status tierStatus, opts StatusOptions) {
	name := readiness.TierName(status.tier)
	header := strings.Join(append([]string{name}, identityCells(status.identity)...), " · ")
	if !status.described.GetBootstrap().GetPresent() {
		fmt.Fprintf(out, "%s · not bootstrapped\n  → run `%s` to set it up\n", header, readiness.BootstrapCommand(status.tier))
		return
	}
	fmt.Fprintln(out, header)
	tw := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "PART\tADDED\tUSED BY\tIDLE\tSTATE")
	for _, part := range status.described.GetParts() {
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", part.GetName(), addedCell(part), usedByCell(part), idleCell(part), stateCell(part, status.tier))
	}
	_ = tw.Flush()
	if opts.Resources {
		renderResources(out, status.described.GetParts())
	}
}

func renderResources(out io.Writer, parts []*contractv1.BootstrapPart) {
	for _, part := range parts {
		if len(part.GetResources()) == 0 {
			continue
		}
		fmt.Fprintf(out, "\n%s\n", part.GetName())
		tw := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
		for _, resource := range part.GetResources() {
			fmt.Fprintf(tw, "  %s\t%s\n", resource.GetKind(), resource.GetId())
		}
		_ = tw.Flush()
	}
}

func identityCells(identity *contractv1.Identity) []string {
	var account []string
	for _, value := range []string{identity.GetProvider(), identity.GetAccount(), identity.GetLocation()} {
		if value != "" {
			account = append(account, value)
		}
	}
	var cells []string
	if len(account) > 0 {
		cells = append(cells, strings.Join(account, " "))
	}
	for _, detail := range identity.GetDetails() {
		if detail.GetValue() == "" {
			continue
		}
		cells = append(cells, strings.TrimSpace(detail.GetLabel()+" "+detail.GetValue()))
	}
	return cells
}

func addedCell(part *contractv1.BootstrapPart) string {
	if part.GetAddedAt() == 0 {
		return unknownCell
	}
	by := "ocel bootstrap"
	if part.GetAddedBy() != "" {
		by = "deploy " + part.GetAddedBy()
	}
	return by + " · " + time.Unix(part.GetAddedAt(), 0).UTC().Format(addedDate)
}

func usedByCell(part *contractv1.BootstrapPart) string {
	users := part.GetUsedBy()
	switch {
	case len(users) == 0:
		return unknownCell
	case part.GetName() == provider.BasePart:
		return everyProject
	case len(users) > usersShown:
		return fmt.Sprintf("%s +%d", strings.Join(users[:usersShown], ", "), len(users)-usersShown)
	}
	return strings.Join(users, ", ")
}

func idleCell(part *contractv1.BootstrapPart) string {
	if part.IdleMonthlyFixed == nil {
		return unknownCell
	}
	amount := part.GetIdleMonthlyFixed()
	if strings.Trim(amount, "0.") == "" {
		return currencyOf(part) + "0"
	}
	return currencyOf(part) + amount + "/mo"
}

func currencyOf(part *contractv1.BootstrapPart) string {
	if part.GetCurrency() == "USD" || part.GetCurrency() == "" {
		return "$"
	}
	return part.GetCurrency() + " "
}

func stateCell(part *contractv1.BootstrapPart, tier environmentv1.Tier) string {
	switch {
	case part.GetState() == contractv1.BootstrapPartState_BOOTSTRAP_PART_STATE_CREATING:
		return partCreating
	case part.GetCurrent():
		return partCurrent
	}
	return partBehind + ", run `" + readiness.BootstrapCommand(tier) + "`"
}
