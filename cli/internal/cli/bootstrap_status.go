package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"text/tabwriter"

	"connectrpc.com/connect"
	"github.com/spf13/cobra"

	"github.com/ocelhq/ocel/cli/internal/projectconfig"
	"github.com/ocelhq/ocel/cli/internal/providerrunner"
	"github.com/ocelhq/ocel/cli/node"
	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
)

type bootstrapStatusOptions struct {
	check bool
}

var bootstrapStatusOpts bootstrapStatusOptions

var bootstrapStatusCmd = &cobra.Command{
	Use:   "status",
	Short: "Report what each class's bootstrap carries and whether this CLI can use it",
	Long: "Report what each class's bootstrap carries and whether this CLI can use it.\n\n" +
		"Both classes are listed, stack by stack: the schema each one is at, whether its " +
		"content is what this build would write, which version wrote it last, and whether " +
		"the account has opted into refreshing stale stacks itself.\n\n" +
		"--check turns the report into a gate: it exits non-zero when any class is at a " +
		"schema this build cannot use or carries a stack whose content has moved on.",
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		cwd, err := os.Getwd()
		if err != nil {
			return fmt.Errorf("determine working directory: %w", err)
		}
		ctx, stop := installInterruptHandler(cmd.Context(), cmd.ErrOrStderr())
		defer stop()

		return runBootstrapStatus(ctx, defaultDeps(), cwd, bootstrapStatusOpts, cmd.OutOrStdout(), cmd.ErrOrStderr())
	},
}

func init() {
	bootstrapStatusCmd.Flags().BoolVar(&bootstrapStatusOpts.check, "check", false, "Exit non-zero when any class is at an unusable schema or carries stale content")
	bootstrapCmd.AddCommand(bootstrapStatusCmd)
}

func runBootstrapStatus(ctx context.Context, d deps, cwd string, opts bootstrapStatusOptions, stdout, stderr io.Writer) error {
	cfg, err := projectconfig.Resolve(ctx, cwd, explicitConfigPath())
	if err != nil {
		return err
	}
	if err := node.Ensure(cfg.Dir); err != nil {
		return err
	}
	provider, err := cfg.RequireProvider()
	if err != nil {
		return err
	}

	var statuses []*contractv1.BootstrapStatus
	err = runProviderSession(ctx, d, cfg, provider, stderr, stderr, func(runner *providerrunner.Runner) error {
		client, err := runner.Deployments()
		if err != nil {
			return err
		}
		for _, tier := range []environmentv1.Tier{environmentv1.Tier_TIER_PRODUCTION, environmentv1.Tier_TIER_PREVIEW} {
			described, err := client.DescribeBootstrap(ctx, &contractv1.DescribeBootstrapRequest{Tier: tier})
			if err != nil {
				if connect.CodeOf(err) == connect.CodeUnimplemented {
					return fmt.Errorf("%s cannot report what a bootstrap carries; it predates the report. Upgrade the provider pinned in this project and try again", provider.Package)
				}
				return err
			}
			status := described.GetBootstrap()
			if status == nil {
				return fmt.Errorf("%s answered without saying anything about the %s bootstrap. Upgrade the provider pinned in this project and try again", provider.Package, bootstrapName(tier))
			}
			statuses = append(statuses, status)
		}
		return nil
	})
	if err != nil {
		return err
	}

	renderBootstrapStatuses(stdout, statuses)
	if !opts.check {
		return nil
	}
	return bootstrapCheck(statuses)
}

func renderBootstrapStatuses(out io.Writer, statuses []*contractv1.BootstrapStatus) {
	for i, status := range statuses {
		if i > 0 {
			fmt.Fprintln(out)
		}
		renderBootstrapStatus(out, status)
	}
}

func renderBootstrapStatus(out io.Writer, status *contractv1.BootstrapStatus) {
	name := bootstrapName(status.GetTier())
	if !status.GetPresent() {
		fmt.Fprintf(out, "%s: not bootstrapped\n", name)
		return
	}
	fmt.Fprintf(out, "%s: schema %d, this CLI speaks schema %d\n", name, status.GetSchema(), status.GetRequiredSchema())

	tw := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "  STACK\tFEATURE\tPRESENT\tSCHEMA\tDIGEST\tWRITTEN BY\tAUTO-HEAL")
	for _, stack := range status.GetStacks() {
		fmt.Fprintf(tw, "  %s\t%s\t%s\t%d\t%s\t%s\t%s\n",
			stack.GetName(),
			orDash(stack.GetFeature()),
			yesNo(stack.GetPresent()),
			stack.GetSchema(),
			digestState(stack),
			orDash(stack.GetWrittenBy()),
			autoHealState(status, stack),
		)
	}
	tw.Flush()

	if problem := bootstrapProblem(status); problem != "" {
		fmt.Fprintf(out, "  %s\n", problem)
	}
}

func bootstrapProblem(status *contractv1.BootstrapStatus) string {
	switch {
	case !status.GetPresent():
		return ""
	case status.GetSchema() < status.GetRequiredSchema():
		return "this bootstrap is an older shape than this CLI speaks; run `ocel bootstrap` to upgrade it"
	case status.GetSchema() > status.GetRequiredSchema():
		return "this bootstrap is a newer shape than this CLI speaks; upgrade the Ocel CLI"
	}
	stale := staleStacks(status)
	if len(stale) == 0 {
		return ""
	}
	return fmt.Sprintf("stale, and refreshed by the next `ocel bootstrap`: %s", strings.Join(stale, ", "))
}

func staleStacks(status *contractv1.BootstrapStatus) []string {
	var out []string
	for _, stack := range status.GetStacks() {
		if stack.GetPresent() && !stack.GetDigestCurrent() {
			out = append(out, stack.GetName())
		}
	}
	return out
}

func bootstrapCheck(statuses []*contractv1.BootstrapStatus) error {
	var problems []string
	for _, status := range statuses {
		if problem := bootstrapProblem(status); problem != "" {
			problems = append(problems, fmt.Sprintf("%s: %s", bootstrapName(status.GetTier()), problem))
		}
	}
	if len(problems) == 0 {
		return nil
	}
	return errors.New(strings.Join(problems, "\n"))
}

func digestState(stack *contractv1.BootstrapStack) string {
	if !stack.GetPresent() {
		return "-"
	}
	if stack.GetDigestCurrent() {
		return "ok"
	}
	return "stale"
}

func autoHealState(status *contractv1.BootstrapStatus, stack *contractv1.BootstrapStack) string {
	if stack.GetFeature() != "" {
		return "-"
	}
	return onOff(status.GetAutoHeal())
}

func yesNo(v bool) string {
	if v {
		return "yes"
	}
	return "no"
}

func onOff(v bool) string {
	if v {
		return "on"
	}
	return "off"
}

func orDash(v string) string {
	if v == "" {
		return "-"
	}
	return v
}
