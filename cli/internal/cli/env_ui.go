package cli

import (
	"context"
	"fmt"
	"io"

	connect "connectrpc.com/connect"
	"github.com/spf13/cobra"

	"github.com/ocelhq/ocel/cli/internal/deploycollector"
	"github.com/ocelhq/ocel/cli/internal/envgate"
	"github.com/ocelhq/ocel/cli/internal/projectconfig"
	"github.com/ocelhq/ocel/cli/internal/providerrunner"
	"github.com/ocelhq/ocel/cli/internal/varsui"
	"github.com/ocelhq/ocel/cli/node"
	envvarsv1 "github.com/ocelhq/ocel/pkg/proto/envvars/v1"
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
			return runEnvUI(ctx, defaultDeps(), cwd, envOpts, cmd.OutOrStdout(), cmd.ErrOrStderr())
		})
	},
}

func init() {
	envUICmd.Flags().BoolVar(&envOpts.preview, "preview", false, "Manage the preview substrate instead of production")
	envCmd.AddCommand(envUICmd)
}

func runEnvUI(ctx context.Context, d deps, cwd string, opts envOptions, stdout, stderr io.Writer) error {
	return envSession(ctx, d, cwd, opts, stdout, stderr, func(runner *providerrunner.Runner, cfg *projectconfig.Config, _ *projectconfig.ProviderDescriptor) error {
		gate, err := discoverVariables(ctx, cfg, runner, opts, stderr)
		if err != nil {
			return err
		}

		session, err := d.openVarsUI(ctx, cfg, runner, opts.preview, gate, stdout)
		if err != nil {
			return err
		}
		defer session.Close()
		return session.Wait(ctx)
	})
}

func (d deps) openVarsUI(
	ctx context.Context,
	cfg *projectconfig.Config,
	runner *providerrunner.Runner,
	preview bool,
	gate *envgate.Gate,
	stdout io.Writer,
) (*varsui.Session, error) {
	session, err := d.serveVarsUI(ctx, cfg, runner, preview, gate)
	if err != nil {
		return nil, err
	}

	fmt.Fprintf(stdout, "\nVariables for %s are at:\n\n  %s\n\n", cfg.Slug, session.URL)
	if err := d.openBrowser(session.URL); err != nil {
		fmt.Fprintln(stdout, "Couldn't open your browser automatically — open the link above manually.")
	}
	return session, nil
}

func startVarsUI(
	ctx context.Context,
	cfg *projectconfig.Config,
	runner *providerrunner.Runner,
	preview bool,
	gate *envgate.Gate,
) (*varsui.Session, error) {
	assets, err := node.VarsUI()
	if err != nil {
		return nil, fmt.Errorf("read the bundled variables UI: %w", err)
	}

	store := runnerValues{
		runner: runner,
		slug:   cfg.Slug,
		tier:   envTier(envOptions{preview: preview}),
	}

	var environments []string
	if preview {
		var err error
		if environments, err = namedEnvironments(ctx, runner, cfg.Slug); err != nil {
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

func discoverVariables(ctx context.Context, cfg *projectconfig.Config, runner *providerrunner.Runner, opts envOptions, stderr io.Writer) (*envgate.Gate, error) {
	gate := envGate(cfg, runner, opts)
	if _, err := deploycollector.Collect(ctx, cfg, gate, io.Discard, stderr); err != nil {
		return nil, err
	}
	return gate, nil
}

func envGate(cfg *projectconfig.Config, runner *providerrunner.Runner, opts envOptions) *envgate.Gate {
	return envgate.New(runnerValues{
		runner: runner,
		slug:   cfg.Slug,
		tier:   envTier(opts),
	}, envScope(cfg, opts.preview, ""))
}

func (v runnerValues) coordinate(at envgate.Address) *envvarsv1.Coordinate {
	return &envvarsv1.Coordinate{Slug: v.slug, Folder: at.Cell.Folder, Key: at.Cell.Key, Environment: at.Environment}
}

func (v runnerValues) Set(ctx context.Context, at envgate.Address, value string, expected *int64) error {
	vars, err := v.runner.Vars()
	if err != nil {
		return err
	}
	_, err = vars.SetValue(ctx, &envvarsv1.SetValueRequest{
		Tier:            v.tier,
		Coordinate:      v.coordinate(at),
		Value:           value,
		ExpectedVersion: expected,
	})
	return staleOrBroken(err)
}

func (v runnerValues) Delete(ctx context.Context, at envgate.Address, expected *int64) error {
	vars, err := v.runner.Vars()
	if err != nil {
		return err
	}
	_, err = vars.DeleteValue(ctx, &envvarsv1.DeleteValueRequest{
		Tier:            v.tier,
		Coordinate:      v.coordinate(at),
		ExpectedVersion: expected,
	})
	return staleOrBroken(err)
}

func staleOrBroken(err error) error {
	if err != nil && connect.CodeOf(err) == connect.CodeFailedPrecondition {
		return varsui.ErrStaleValue
	}
	return err
}

func (v runnerValues) History(ctx context.Context, at envgate.Address) ([]varsui.Version, error) {
	vars, err := v.runner.Vars()
	if err != nil {
		return nil, err
	}
	resp, err := vars.ListVersions(ctx, &envvarsv1.ListVersionsRequest{
		Tier:       v.tier,
		Coordinate: v.coordinate(at),
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
