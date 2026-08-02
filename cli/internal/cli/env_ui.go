package cli

import (
	"context"
	"fmt"
	"io"

	connect "connectrpc.com/connect"
	"github.com/pkg/browser"
	"github.com/spf13/cobra"

	"github.com/ocelhq/ocel/cli/internal/deploycollector"
	"github.com/ocelhq/ocel/cli/internal/envgate"
	"github.com/ocelhq/ocel/cli/internal/manifestbuilder"
	"github.com/ocelhq/ocel/cli/internal/projectconfig"
	"github.com/ocelhq/ocel/cli/internal/providerrunner"
	"github.com/ocelhq/ocel/cli/internal/varsui"
	"github.com/ocelhq/ocel/cli/platform"
	envv1 "github.com/ocelhq/ocel/pkg/proto/env/v1"
)

var envUICmd = &cobra.Command{
	Use:   "ui",
	Short: "Open the variables matrix in a browser",
	Long: "Open the required-cell matrix for this project: a row per variable your code declares, " +
		"a column per folder, and the cells that are still owed.\n\n" +
		"The page ships inside this binary and is served over loopback from the provider session " +
		"this command already holds, so it needs no hosted service and no network beyond the " +
		"provider's own calls.",
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		return withEnvCommand(cmd, func(ctx context.Context, cwd string) error {
			return runEnvUI(ctx, cwd, envOpts, cmd.OutOrStdout(), cmd.ErrOrStderr())
		})
	},
}

func init() {
	envUICmd.Flags().BoolVar(&envOpts.preview, "preview", false, "Manage the preview substrate instead of production")
	envCmd.AddCommand(envUICmd)
}

func runEnvUI(ctx context.Context, cwd string, opts envOptions, stdout, stderr io.Writer) error {
	return envSession(ctx, cwd, opts, stdout, stderr, func(runner *providerrunner.Runner, cfg *projectconfig.Config, provider *projectconfig.ProviderDescriptor) error {
		gate, err := discoverVariables(ctx, cfg, runner, provider, opts, stderr)
		if err != nil {
			return err
		}

		session, err := OpenVarsUI(ctx, cfg, provider, runner, opts.preview, gate, stdout)
		if err != nil {
			return err
		}
		defer session.Close()
		return session.Wait(ctx)
	})
}

// openBrowser is a seam over browser.OpenURL. Without it every test that
// drives a command as far as the variables UI launches the developer's real
// browser, which is not something a `go test` run may do.
var openBrowser = browser.OpenURL

// OpenVarsUI starts the bundled variables UI over a provider session the caller
// already holds, prints and opens its URL, and returns the session to wait on.
func OpenVarsUI(
	ctx context.Context,
	cfg *projectconfig.Config,
	provider *projectconfig.ProviderDescriptor,
	runner *providerrunner.Runner,
	preview bool,
	gate *envgate.Gate,
	stdout io.Writer,
) (*varsui.Session, error) {
	session, err := serveVarsUI(ctx, cfg, provider, runner, preview, gate)
	if err != nil {
		return nil, err
	}

	fmt.Fprintf(stdout, "\nVariables for %s are at:\n\n  %s\n\n", cfg.Slug, session.URL)
	if err := openBrowser(session.URL); err != nil {
		fmt.Fprintln(stdout, "Couldn't open your browser automatically — open the link above manually.")
	}
	return session, nil
}

// serveVarsUI is a seam over startVarsUI. A session that ends any way but the
// developer marking the matrix done is an abandonment, and nothing inside this
// process ends one that way — the browser closing is not a signal the page
// sends today — so a test can reach that outcome only by holding the session
// itself.
var serveVarsUI = startVarsUI

// startVarsUI serves the bundled variables UI over a provider session the
// caller already holds and announces nothing. The gate is passed in rather than
// built here because the caller that needs this most is a deploy that already
// ran discovery and refused: it hands over the same gate, so the matrix opens
// describing precisely the cells that stopped it. That caller also renders its
// own terminal, so where the URL is printed is its decision, not this one's.
func startVarsUI(
	ctx context.Context,
	cfg *projectconfig.Config,
	provider *projectconfig.ProviderDescriptor,
	runner *providerrunner.Runner,
	preview bool,
	gate *envgate.Gate,
) (*varsui.Session, error) {
	assets, err := platform.VarsUI()
	if err != nil {
		return nil, fmt.Errorf("read the bundled variables UI: %w", err)
	}

	store := runnerValues{
		runner:  runner,
		options: []byte(provider.Options),
		slug:    cfg.Slug,
		class:   envClass(envOptions{preview: preview}),
	}

	// Only the preview substrate has named environments, and the enumeration is
	// read once per session: the picker names what exists when the page opened,
	// and a write against a name that has gone since is refused by the store's
	// own side rather than by a listing this held onto.
	var environments []string
	if preview {
		var err error
		if environments, err = namedEnvironments(ctx, runner, provider, cfg.Slug); err != nil {
			return nil, err
		}
	}

	return varsui.Serve(ctx, varsui.Options{
		Assets:       assets,
		Gate:         gate,
		Store:        store,
		Slug:         cfg.Slug,
		Preview:      preview,
		Environments: environments,
	})
}

// discoverVariables runs the project's discovery pass and returns the gate it
// filled: what the code declares, what the store holds for it, and what the
// declaring process refused to run with.
func discoverVariables(ctx context.Context, cfg *projectconfig.Config, runner *providerrunner.Runner, provider *projectconfig.ProviderDescriptor, opts envOptions, stderr io.Writer) (*envgate.Gate, error) {
	gate := envGate(cfg, runner, provider, opts)
	if _, err := deploycollector.Collect(ctx, cfg, gate, io.Discard, stderr); err != nil {
		return nil, err
	}
	return gate, nil
}

// envGate is the gate an `ocel env` command fills: the store reached through
// this session's provider, and the apps the flags' substrate is scoped to.
func envGate(cfg *projectconfig.Config, runner *providerrunner.Runner, provider *projectconfig.ProviderDescriptor, opts envOptions) *envgate.Gate {
	return envgate.New(runnerValues{
		runner:  runner,
		options: []byte(provider.Options),
		slug:    cfg.Slug,
		class:   envClass(opts),
	}, envScope(cfg, opts.preview, ""))
}

// The write half of the store, alongside the reads runnerValues already
// answers for the gate. Both go through the provider binary: the CLI has no
// cloud SDK dependency and must not gain one.

// coordinate is one address on the wire: the cell, and the named environment
// whose override the page addressed, empty for the class-wide value.
func (v runnerValues) coordinate(at envgate.Address) *envv1.Coordinate {
	return &envv1.Coordinate{Slug: v.slug, Folder: at.Cell.Folder, Key: at.Cell.Key, Environment: at.Environment}
}

func (v runnerValues) Set(ctx context.Context, at envgate.Address, value string, expected *int64) error {
	_, err := v.runner.SetValue(ctx, &envv1.SetValueRequest{
		Options:         v.options,
		ProtocolVersion: manifestbuilder.SchemaVersion,
		Class:           v.class,
		Coordinate:      v.coordinate(at),
		Value:           value,
		ExpectedVersion: expected,
	})
	return staleOrBroken(err)
}

func (v runnerValues) Delete(ctx context.Context, at envgate.Address, expected *int64) error {
	_, err := v.runner.DeleteValue(ctx, &envv1.DeleteValueRequest{
		Options:         v.options,
		ProtocolVersion: manifestbuilder.SchemaVersion,
		Class:           v.class,
		Coordinate:      v.coordinate(at),
		ExpectedVersion: expected,
	})
	return staleOrBroken(err)
}

// staleOrBroken separates the store's refusal of an expectation that no longer
// holds from a store the UI could not reach. FAILED_PRECONDITION is the code
// the wire contract reserves for it, so this reads a code rather than a
// message and every provider answers it the same way.
func staleOrBroken(err error) error {
	if err != nil && connect.CodeOf(err) == connect.CodeFailedPrecondition {
		return varsui.ErrStaleValue
	}
	return err
}

func (v runnerValues) History(ctx context.Context, at envgate.Address) ([]varsui.Version, error) {
	resp, err := v.runner.ListVersions(ctx, &envv1.ListVersionsRequest{
		Options:         v.options,
		ProtocolVersion: manifestbuilder.SchemaVersion,
		Class:           v.class,
		Coordinate:      v.coordinate(at),
	})
	if err != nil {
		return nil, err
	}

	versions := make([]varsui.Version, 0, len(resp.GetVersions()))
	for _, entry := range resp.GetVersions() {
		versions = append(versions, varsui.Version{
			Version:   entry.GetVersion(),
			CreatedAt: entry.GetCreatedAt(),
			Size:      entry.GetSize(),
		})
	}
	return versions, nil
}
