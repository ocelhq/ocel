package bootstrap

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

	"github.com/ocelhq/ocel/cli/internal/cli/session"
	"github.com/ocelhq/ocel/cli/internal/provider"
	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
)

type StatusOptions struct {
	Check bool
}

func newStatusCommand(sess session.Session) *cobra.Command {
	var opts StatusOptions

	cmd := &cobra.Command{
		Use:   "status",
		Short: "Show what each environment has set up",
		Long: "Show what each environment has set up.\n\n" +
			"Lists both environments stack by stack: schema, whether content matches what this " +
			"build would write, which version wrote it last, and whether stale stacks auto-heal.\n\n",
		Example: "  $ ocel bootstrap status --check",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			cwd, err := os.Getwd()
			if err != nil {
				return fmt.Errorf("determine working directory: %w", err)
			}
			ctx, stop := sess.Interrupt(cmd.Context(), cmd.ErrOrStderr())
			defer stop()

			return RunStatus(ctx, sess, cwd, opts, cmd.OutOrStdout(), cmd.ErrOrStderr())
		},
	}

	cmd.Flags().BoolVar(&opts.Check, "check", false, "Exit non-zero on an unusable schema or stale content")

	return cmd
}

func RunStatus(ctx context.Context, sess session.Session, cwd string, opts StatusOptions, stdout, stderr io.Writer) error {
	cfg, err := resolveProject(ctx, sess, cwd)
	if err != nil {
		return err
	}

	var statuses []*contractv1.BootstrapStatus
	err = provider.Drive(ctx, cfg, stderr, stderr, func(runner *provider.Runner) error {
		client, err := runner.Deployments()
		if err != nil {
			return err
		}
		for _, tier := range []environmentv1.Tier{environmentv1.Tier_TIER_PRODUCTION, environmentv1.Tier_TIER_PREVIEW} {
			described, err := client.DescribeBootstrap(ctx, &contractv1.DescribeBootstrapRequest{Tier: tier})
			if err != nil {
				if connect.CodeOf(err) == connect.CodeUnimplemented {
					return fmt.Errorf("%s cannot report what a bootstrap has; it predates the report. Upgrade the provider pinned in this project and try again", runner.Package())
				}
				return err
			}
			status := described.GetBootstrap()
			if status == nil {
				return fmt.Errorf("%s answered without saying anything about the %s bootstrap. Upgrade the provider pinned in this project and try again", runner.Package(), Name(tier))
			}
			statuses = append(statuses, status)
		}
		return nil
	})
	if err != nil {
		return err
	}

	renderStatuses(stdout, statuses)
	if !opts.Check {
		return nil
	}
	return check(statuses)
}

func renderStatuses(out io.Writer, statuses []*contractv1.BootstrapStatus) {
	for i, status := range statuses {
		if i > 0 {
			fmt.Fprintln(out)
		}
		renderStatus(out, status)
	}
}

func renderStatus(out io.Writer, status *contractv1.BootstrapStatus) {
	name := Name(status.GetTier())
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

	if problem := statusProblem(status); problem != "" {
		fmt.Fprintf(out, "  %s\n", problem)
	}
}

func statusProblem(status *contractv1.BootstrapStatus) string {
	switch {
	case !status.GetPresent():
		return ""
	case status.GetSchema() < status.GetRequiredSchema():
		return fmt.Sprintf("this bootstrap is an older shape than this CLI speaks; run `ocel bootstrap %s` to upgrade it", Name(status.GetTier()))
	case status.GetSchema() > status.GetRequiredSchema():
		return "this bootstrap is a newer shape than this CLI speaks; upgrade the Ocel CLI"
	}
	stale := staleStacks(status)
	if len(stale) == 0 {
		return ""
	}
	return fmt.Sprintf("stale, and refreshed by the next `ocel bootstrap %s`: %s", Name(status.GetTier()), strings.Join(stale, ", "))
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

func check(statuses []*contractv1.BootstrapStatus) error {
	var problems []string
	for _, status := range statuses {
		if problem := statusProblem(status); problem != "" {
			problems = append(problems, fmt.Sprintf("%s: %s", Name(status.GetTier()), problem))
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
