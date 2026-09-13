package connector

import (
	"context"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"

	"github.com/fatih/color"
	"github.com/spf13/cobra"

	"github.com/ocelhq/ocel/cli/internal/cli/cmddeps"
	"github.com/ocelhq/ocel/cli/internal/console"
	consoleconnector "github.com/ocelhq/ocel/cli/internal/console/connector"
	consolelink "github.com/ocelhq/ocel/cli/internal/console/link"
	"github.com/ocelhq/ocel/cli/internal/exitsig"
	"github.com/ocelhq/ocel/cli/internal/projectconfig"
	"github.com/ocelhq/ocel/pkg/connectorkit"
)

var (
	check = color.New(color.FgGreen).Sprint("✓")
	bold  = color.New(color.Bold).SprintFunc()
)

const reachDial = "dial"

type options struct {
	compute string
	target  string
	reveal  bool
	write   bool
	apiURL  string
	console *consoleconnector.Client
}

func (o options) grants() []string {
	held := []string{connectorkit.CapabilityEnvVarsRead}
	if o.write {
		held = append(held, connectorkit.CapabilityEnvVarsWrite)
	}
	if o.reveal {
		held = append(held, connectorkit.CapabilityEnvVarsReveal)
	}
	return held
}

func NewCommand(deps cmddeps.Deps) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "connector <command>",
		Short:   "Manage the connector the console reads this target's variables through",
		Example: "  $ ocel connector add --config ocel.vps.json\n  $ ocel connector status\n  $ ocel connector rm --config ocel.vps.json",
		RunE: func(cmd *cobra.Command, _ []string) error {
			_ = cmd.Help()
			return &exitsig.ExitError{Code: 1}
		},
	}
	cmd.AddCommand(newAddCommand(deps), newRemoveCommand(deps), newStatusCommand(deps))
	return cmd
}

func newAddCommand(deps cmddeps.Deps) *cobra.Command {
	var opts options
	cmd := &cobra.Command{
		Use:     "add",
		Short:   "Put a connector on this target and pair it with the console",
		Example: "  $ ocel connector add --config ocel.vps.json\n  $ ocel connector add --config ocel.vps.json --allow-reveal",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return withOptions(cmd, deps, &opts, func(ctx context.Context, cfg *projectconfig.Config, link *consolelink.Link) error {
				return runAdd(ctx, deps, cfg, link, opts, cmd.OutOrStdout(), cmd.ErrOrStderr())
			})
		},
	}
	cmd.Flags().StringVar(&opts.compute, "compute", "", "What the connector runs on: `serverless` or container, and the provider's own default when this names neither")
	cmd.Flags().BoolVar(&opts.reveal, "allow-reveal", false, "Let the console read secret values back in the clear")
	cmd.Flags().BoolVar(&opts.write, "allow-write", true, "Let the console write values")
	return cmd
}

func newRemoveCommand(deps cmddeps.Deps) *cobra.Command {
	var opts options
	cmd := &cobra.Command{
		Use:     "rm",
		Short:   "Take the connector off this target and forget it in the console",
		Example: "  $ ocel connector rm --config ocel.vps.json\n  $ ocel connector rm --target vps/sha256:abc/ocel",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return withOptions(cmd, deps, &opts, func(ctx context.Context, cfg *projectconfig.Config, link *consolelink.Link) error {
				return runRemove(ctx, deps, cfg, link, opts, cmd.OutOrStdout(), cmd.ErrOrStderr())
			})
		},
	}
	cmd.Flags().StringVar(&opts.target, "target", "", "The `fingerprint` ocel connector status prints, to forget a connector whose target will not answer")
	return cmd
}

func newStatusCommand(deps cmddeps.Deps) *cobra.Command {
	var opts options
	cmd := &cobra.Command{
		Use:     "status",
		Short:   "Say what the console holds for the connectors of this organization",
		Example: "  $ ocel connector status\n  $ ocel connector status --config ocel.vps.json",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return withOptions(cmd, deps, &opts, func(ctx context.Context, cfg *projectconfig.Config, link *consolelink.Link) error {
				return runStatus(ctx, deps, cfg, link, opts, cmd.OutOrStdout())
			})
		},
	}
	return cmd
}

func withOptions(cmd *cobra.Command, deps cmddeps.Deps, opts *options,
	run func(context.Context, *projectconfig.Config, *consolelink.Link) error) error {
	cwd, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("determine working directory: %w", err)
	}
	ctx := cmd.Context()
	cfg, err := projectconfig.Resolve(ctx, cwd, deps.ConfigPath())
	if err != nil {
		return err
	}
	creds, credErr := deps.LoadCredentials()
	if credErr != nil {
		fmt.Fprintln(cmd.ErrOrStderr(), "You're not logged in. Run `ocel login` first.")
		return &exitsig.ExitError{Code: 1}
	}
	opts.apiURL = console.EffectiveBaseURL(creds.APIURL)
	opts.console = consoleconnector.New(opts.apiURL)

	link, err := consolelink.Read(cfg.Dir, opts.apiURL)
	if err != nil {
		return err
	}
	if link == nil {
		fmt.Fprintln(cmd.ErrOrStderr(), "This directory isn't linked to a console project. Run `ocel link` first.")
		return &exitsig.ExitError{Code: 1}
	}
	return run(ctx, cfg, link)
}

func token(deps cmddeps.Deps) (string, error) {
	creds, err := deps.LoadCredentials()
	if err != nil {
		return "", err
	}
	return creds.AccessToken, nil
}

func vendored(cfg *projectconfig.Config) (string, error) {
	desc, err := cfg.RequireProvider()
	if err != nil {
		return "", err
	}
	return desc.Name, nil
}

func printed(out io.Writer, held consoleconnector.Connector, live consoleconnector.Liveness) {
	fmt.Fprintf(out, "%s\n", bold(held.Target))
	fmt.Fprintf(out, "  compute %s over %s, %s\n", named(held.Compute, "unset"), held.Reach, live)
	fmt.Fprintf(out, "  url %s\n", named(held.URL, "none"))
	fmt.Fprintf(out, "  can %s\n", listed(held.Capabilities))
	if held.LastDenied != nil {
		fmt.Fprintf(out, "  last refused %s at %s: %s\n", held.LastDenied.Verb, held.LastDenied.At, held.LastDenied.Message)
	}
}

func named(held *string, absent string) string {
	if held == nil || *held == "" {
		return absent
	}
	return *held
}

func listed(held []string) string {
	if len(held) == 0 {
		return "nothing yet"
	}
	written := slices.Clone(held)
	slices.Sort(written)
	return strings.Join(written, ", ")
}
