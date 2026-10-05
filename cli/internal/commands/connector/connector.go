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

	"github.com/ocelhq/ocel/cli/internal/clierror"
	"github.com/ocelhq/ocel/cli/internal/commands"
	"github.com/ocelhq/ocel/cli/internal/console"
	"github.com/ocelhq/ocel/cli/internal/exitcode"
	"github.com/ocelhq/ocel/cli/internal/project"
	"github.com/ocelhq/ocel/cli/internal/providerprocess"
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
	capabilities := []string{connectorserver.CapabilityVariablesRead}
	if o.write {
		capabilities = append(capabilities, connectorserver.CapabilityVariablesWrite)
	}
	if o.reveal {
		capabilities = append(capabilities, connectorserver.CapabilityVariablesReveal)
	}
	return capabilities
}

type Dependencies struct {
	commands.Invocation
	LoadCredentials func() (console.Credentials, error)
}

func NewCommand(dependencies Dependencies) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "connector <command>",
		Short:   "Manage the connector the console reads this target's variables through",
		Example: "  $ ocel connector add --config ocel.staging.json\n  $ ocel connector status\n  $ ocel connector rm --config ocel.staging.json",
		RunE: func(cmd *cobra.Command, _ []string) error {
			_ = cmd.Help()
			return &clierror.Error{Code: clierror.CodeUsage, Cause: &exitcode.ExitError{Code: 1}}
		},
	}
	cmd.AddCommand(newAddCommand(dependencies), newRemoveCommand(dependencies), newStatusCommand(dependencies))
	return commands.DeclareReadOnly(cmd)
}

func newAddCommand(dependencies Dependencies) *cobra.Command {
	var opts options
	cmd := &cobra.Command{
		Use:     "add",
		Short:   "Put a connector on this target and pair it with the console",
		Example: "  $ ocel connector add --config ocel.staging.json\n  $ ocel connector add --config ocel.staging.json --allow-reveal",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return withOptions(cmd, dependencies, &opts, func(ctx context.Context, cfg *project.Project, link *console.Link) error {
				return runAdd(ctx, dependencies, cfg, link, opts)
			})
		},
	}
	cmd.Flags().StringVar(&opts.compute, "compute", "", "What the connector runs on: `serverless` or container, and the provider's own default when this names neither")
	cmd.Flags().BoolVar(&opts.reveal, "allow-reveal", false, "Let the console read secret values back in the clear")
	cmd.Flags().BoolVar(&opts.write, "allow-write", true, "Let the console write values")
	return commands.DeclareMutating(cmd)
}

func newRemoveCommand(dependencies Dependencies) *cobra.Command {
	var opts options
	cmd := &cobra.Command{
		Use:     "rm",
		Short:   "Take the connector off this target and forget it in the console",
		Example: "  $ ocel connector rm --config ocel.staging.json\n  $ ocel connector rm --target <fingerprint>",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return withOptions(cmd, dependencies, &opts, func(ctx context.Context, cfg *project.Project, link *console.Link) error {
				return runRemove(ctx, dependencies, cfg, link, opts)
			})
		},
	}
	cmd.Flags().StringVar(&opts.target, "target", "", "The `fingerprint` ocel connector status prints, to forget a connector whose target will not answer")
	return commands.DeclareMutating(cmd)
}

func newStatusCommand(dependencies Dependencies) *cobra.Command {
	var opts options
	cmd := &cobra.Command{
		Use:     "status",
		Short:   "Say what the console has registered for the connectors of this organization",
		Example: "  $ ocel connector status\n  $ ocel connector status --config ocel.staging.json",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return withOptions(cmd, dependencies, &opts, func(ctx context.Context, cfg *project.Project, link *console.Link) error {
				return runStatus(ctx, dependencies, cfg, link, opts, cmd.OutOrStdout())
			})
		},
	}
	return commands.DeclareReadOnly(commands.ReserveStdout(cmd))
}

func withOptions(cmd *cobra.Command, dependencies Dependencies, opts *options,
	run func(context.Context, *project.Project, *console.Link) error) error {
	cwd, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("determine working directory: %w", err)
	}
	ctx := cmd.Context()
	cfg, err := dependencies.LoadProject(ctx, cwd)
	if err != nil {
		return err
	}
	credentials, err := console.RequireLogin(dependencies.LoadCredentials, cmd.ErrOrStderr())
	if err != nil {
		return err
	}
	opts.apiURL = console.BaseURL(credentials.APIURL)
	opts.console = console.New(opts.apiURL)

	link, err := console.ReadLink(cfg.Dir, opts.apiURL)
	if err != nil {
		return err
	}
	if link == nil {
		fmt.Fprintln(cmd.ErrOrStderr(), "This directory isn't linked to a console project. Run `ocel link` first.")
		return &clierror.Error{Code: "console.not_linked", Hint: "ocel link", Cause: &exitcode.ExitError{Code: 1, Err: console.ErrNotLinked}}
	}
	return run(ctx, cfg, link)
}

func reachTarget(ctx context.Context, dependencies Dependencies, cfg *project.Project, check *run.Span) (*providerprocess.Provider, *contractv1.DescribeConnectorTargetResponse, error) {
	provider, _, err := dependencies.OpenProvider(ctx, check, cfg, commands.OpenOptions{})
	if err != nil {
		return nil, nil, err
	}
	var described *contractv1.DescribeConnectorTargetResponse
	err = provider.Call(ctx, func(client contractv1connect.ProviderServiceClient) (err error) {
		described, err = client.DescribeConnectorTarget(ctx, &contractv1.DescribeConnectorTargetRequest{})
		return err
	})
	if err != nil {
		provider.Close()
		return nil, nil, err
	}
	return provider, described, nil
}

func readAccessToken(dependencies Dependencies) (string, error) {
	credentials, err := dependencies.LoadCredentials()
	if err != nil {
		return "", err
	}
	return credentials.AccessToken, nil
}

func requireProviderID(cfg *project.Project) (string, error) {
	declared, err := cfg.RequireProvider()
	if err != nil {
		return "", err
	}
	return declared.ID, nil
}

func printConnector(out io.Writer, registered console.Connector, live console.Liveness) {
	fmt.Fprintf(out, "%s\n", terminal.PaletteFor(out).Bold(registered.Target))
	fmt.Fprintf(out, "  compute %s over %s, %s\n", formatOptional(registered.Compute, "unset"), registered.Reach, live)
	fmt.Fprintf(out, "  url %s\n", formatOptional(registered.URL, "none"))
	fmt.Fprintf(out, "  can %s\n", formatCapabilities(registered.Capabilities))
	if registered.LastDenied != nil {
		fmt.Fprintf(out, "  last refused %s at %s: %s\n", registered.LastDenied.Verb, registered.LastDenied.At, registered.LastDenied.Message)
	}
}

func formatOptional(value *string, absent string) string {
	if value == nil || *value == "" {
		return absent
	}
	return *value
}

func formatCapabilities(values []string) string {
	if len(values) == 0 {
		return "nothing yet"
	}
	written := slices.Clone(values)
	slices.Sort(written)
	return strings.Join(written, ", ")
}
