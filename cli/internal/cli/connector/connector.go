package connector

import (
	"context"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"

	"github.com/ocelhq/ocel/cli/internal/terminal"

	"github.com/spf13/cobra"

	"github.com/ocelhq/ocel/cli/internal/cli/cmddeps"
	"github.com/ocelhq/ocel/cli/internal/console"
	"github.com/ocelhq/ocel/cli/internal/exitcode"
	"github.com/ocelhq/ocel/cli/internal/project"
	"github.com/ocelhq/ocel/cli/internal/providerclient"
	"github.com/ocelhq/ocel/cli/internal/run"
	"github.com/ocelhq/ocel/pkg/connectorserver"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/proto/provider/contract/v1/contractv1connect"
)

const reachDial = "dial"

type options struct {
	compute string
	target  string
	reveal  bool
	write   bool
	apiURL  string
	console *console.Client
}

func (o options) grants() []string {
	capabilities := []string{connectorserver.CapabilityEnvVarsRead}
	if o.write {
		capabilities = append(capabilities, connectorserver.CapabilityEnvVarsWrite)
	}
	if o.reveal {
		capabilities = append(capabilities, connectorserver.CapabilityEnvVarsReveal)
	}
	return capabilities
}

func NewCommand(deps cmddeps.Deps) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "connector <command>",
		Short:   "Manage the connector the console reads this target's variables through",
		Example: "  $ ocel connector add --config ocel.staging.json\n  $ ocel connector status\n  $ ocel connector rm --config ocel.staging.json",
		RunE: func(cmd *cobra.Command, _ []string) error {
			_ = cmd.Help()
			return &exitcode.ExitError{Code: 1}
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
		Example: "  $ ocel connector add --config ocel.staging.json\n  $ ocel connector add --config ocel.staging.json --allow-reveal",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return withOptions(cmd, deps, &opts, func(ctx context.Context, cfg *project.Project, link *console.Link) error {
				return runAdd(ctx, deps, cfg, link, opts)
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
		Example: "  $ ocel connector rm --config ocel.staging.json\n  $ ocel connector rm --target <fingerprint>",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return withOptions(cmd, deps, &opts, func(ctx context.Context, cfg *project.Project, link *console.Link) error {
				return runRemove(ctx, deps, cfg, link, opts)
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
		Short:   "Say what the console has registered for the connectors of this organization",
		Example: "  $ ocel connector status\n  $ ocel connector status --config ocel.staging.json",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return withOptions(cmd, deps, &opts, func(ctx context.Context, cfg *project.Project, link *console.Link) error {
				return runStatus(ctx, deps, cfg, link, opts, cmd.OutOrStdout())
			})
		},
	}
	return cmddeps.ReserveStdout(cmd)
}

func withOptions(cmd *cobra.Command, deps cmddeps.Deps, opts *options,
	run func(context.Context, *project.Project, *console.Link) error) error {
	cwd, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("determine working directory: %w", err)
	}
	ctx := cmd.Context()
	cfg, err := deps.LoadProject(ctx, cwd)
	if err != nil {
		return err
	}
	creds, err := console.RequireLogin(deps.LoadCredentials, cmd.ErrOrStderr())
	if err != nil {
		return err
	}
	opts.apiURL = console.BaseURL(creds.APIURL)
	opts.console = console.New(opts.apiURL)

	link, err := console.ReadLink(cfg.Dir, opts.apiURL)
	if err != nil {
		return err
	}
	if link == nil {
		fmt.Fprintln(cmd.ErrOrStderr(), "This directory isn't linked to a console project. Run `ocel link` first.")
		return &exitcode.ExitError{Code: 1}
	}
	return run(ctx, cfg, link)
}

func reachTarget(ctx context.Context, deps cmddeps.Deps, cfg *project.Project, check *run.Span) (*providerclient.Provider, *contractv1.DescribeConnectorTargetResponse, error) {
	prov, err := providerclient.Start(ctx, cfg, check, deps.Questions, providerclient.PinToLock)
	if err != nil {
		return nil, nil, err
	}
	var described *contractv1.DescribeConnectorTargetResponse
	err = prov.Call(ctx, func(client contractv1connect.ProviderServiceClient) (err error) {
		described, err = client.DescribeConnectorTarget(ctx, &contractv1.DescribeConnectorTargetRequest{})
		return err
	})
	if err != nil {
		prov.Close()
		return nil, nil, err
	}
	return prov, described, nil
}

func token(deps cmddeps.Deps) (string, error) {
	creds, err := deps.LoadCredentials()
	if err != nil {
		return "", err
	}
	return creds.AccessToken, nil
}

func vendored(cfg *project.Project) (string, error) {
	desc, err := cfg.RequireProvider()
	if err != nil {
		return "", err
	}
	return desc.ID, nil
}

func printed(out io.Writer, registered console.Connector, live console.Liveness) {
	fmt.Fprintf(out, "%s\n", terminal.PaletteFor(out).Bold(registered.Target))
	fmt.Fprintf(out, "  compute %s over %s, %s\n", named(registered.Compute, "unset"), registered.Reach, live)
	fmt.Fprintf(out, "  url %s\n", named(registered.URL, "none"))
	fmt.Fprintf(out, "  can %s\n", listed(registered.Capabilities))
	if registered.LastDenied != nil {
		fmt.Fprintf(out, "  last refused %s at %s: %s\n", registered.LastDenied.Verb, registered.LastDenied.At, registered.LastDenied.Message)
	}
}

func named(value *string, absent string) string {
	if value == nil || *value == "" {
		return absent
	}
	return *value
}

func listed(values []string) string {
	if len(values) == 0 {
		return "nothing yet"
	}
	written := slices.Clone(values)
	slices.Sort(written)
	return strings.Join(written, ", ")
}
