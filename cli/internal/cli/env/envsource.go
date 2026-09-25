package env

import (
	"context"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/ocelhq/ocel/cli/internal/cli/cmddeps"
	"github.com/ocelhq/ocel/cli/internal/dotenv"
	"github.com/ocelhq/ocel/cli/internal/envwire"
	"github.com/ocelhq/ocel/cli/internal/projectconfig"
	"github.com/ocelhq/ocel/cli/internal/provider"
	"github.com/ocelhq/ocel/cli/internal/runui"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	envvarsv1 "github.com/ocelhq/ocel/pkg/proto/provider/envvars/v1"
	"github.com/ocelhq/ocel/pkg/providerkit/envsource"
)

func newSourceCommand(deps cmddeps.Deps) *cobra.Command {
	var opts envOptions
	cmd := &cobra.Command{
		Use:     "source",
		Short:   "Show where a tier's values are read from, and how its last sync went",
		Example: "  $ ocel env source\n  $ ocel env source --preview",
		Args:    cobra.NoArgs,
	}
	cmd.RunE = func(cmd *cobra.Command, _ []string) error {
		return withCommand(cmd, deps, func(ctx context.Context, cwd string) error {
			return runEnvSource(ctx, deps, cwd, opts, cmd.OutOrStdout(), cmd.ErrOrStderr())
		})
	}
	previewFlag(cmd, &opts)
	return cmd
}

func newSyncCommand(deps cmddeps.Deps) *cobra.Command {
	var opts envOptions
	cmd := &cobra.Command{
		Use:     "sync",
		Short:   "Read a tier's env source into its values now",
		Example: "  $ ocel env sync\n  $ ocel env sync --preview",
		Args:    cobra.NoArgs,
	}
	cmd.RunE = func(cmd *cobra.Command, _ []string) error {
		return withCommand(cmd, deps, func(ctx context.Context, cwd string) error {
			return runEnvSync(ctx, deps, cwd, opts, cmd.OutOrStdout(), cmd.ErrOrStderr())
		})
	}
	previewFlag(cmd, &opts)
	return cmd
}

func tierName(opts envOptions) string {
	if opts.preview {
		return "preview"
	}
	return "production"
}

func runEnvSync(ctx context.Context, deps cmddeps.Deps, cwd string, opts envOptions, stdout, stderr io.Writer) error {
	return withEnvProvider(ctx, deps, cwd, opts, stderr, func(runner *provider.Runner, cfg *projectconfig.Config, _ *contractv1.PreflightResponse) error {
		synced, err := envwire.SyncEnvSource(ctx, runner, cfg, opts.preview)
		if err != nil {
			return err
		}
		status := synced.GetStatus()
		if !envwire.StatusSource(status).Owns() {
			fmt.Fprintf(stdout, "%s reads ocel's own store (builtin), so there is nothing to sync.\n", tierName(opts))
			return nil
		}
		fmt.Fprintf(stdout, "Synced %s from %s: %d written, %d removed, %d held there.\n",
			tierName(opts), status.GetEnvSource(), synced.GetWritten(), synced.GetRemoved(), len(synced.GetPresent()))
		for _, refused := range synced.GetRefused() {
			fmt.Fprintf(stdout, "%s holds %s in %s, and ocel could not keep it: it is over ocel's size limit, or a reference where it would land.\n",
				status.GetEnvSource(), refused.GetKey(), folderOrRoot(refused.GetFolder()))
		}
		return nil
	})
}

func runEnvSource(ctx context.Context, deps cmddeps.Deps, cwd string, opts envOptions, stdout, stderr io.Writer) error {
	return withEnvProvider(ctx, deps, cwd, opts, stderr, func(runner *provider.Runner, cfg *projectconfig.Config, _ *contractv1.PreflightResponse) error {
		vars, err := runner.Vars()
		if err != nil {
			return err
		}
		described, err := vars.DescribeEnvSource(ctx, &envvarsv1.DescribeEnvSourceRequest{Tier: envwire.Tier(opts.preview), Slug: cfg.Slug})
		if err != nil {
			return err
		}
		renderSource(stdout, tierName(opts), described.GetStatus(), envwire.DeployedSource(cfg, opts.preview), time.Now())
		renderDevSource(stdout, cfg.EnvSource.Dev)
		return nil
	})
}

func renderSource(stdout io.Writer, tier string, status *envvarsv1.EnvSourceStatus, configured envsource.Descriptor, now time.Time) {
	fmt.Fprintf(stdout, "%s reads from %s\n", tier, status.GetEnvSource())
	if configured.ID() != status.GetEnvSource() {
		fmt.Fprintf(stdout, "  configured  %s — deploy, or run `ocel env sync`, to read from it\n", configured.ID())
	}
	if !envwire.StatusSource(status).Owns() {
		return
	}
	standing := "no — read at deploy and on `ocel env sync` alone"
	if status.GetStanding() {
		standing = fmt.Sprintf("yes — the target re-reads it about every %s", envsource.PollInterval)
	}
	fmt.Fprintf(stdout, "  standing    %s\n", standing)
	fmt.Fprintf(stdout, "  last sync   %s%s\n", runui.EpochDateTime(status.GetLastSuccessAt()), lag(now, status.GetLastSuccessAt()))
	if status.GetLastAttemptAt() != status.GetLastSuccessAt() {
		fmt.Fprintf(stdout, "  last try    %s\n", runui.EpochDateTime(status.GetLastAttemptAt()))
	}
	if said := status.GetLastError(); said != "" {
		fmt.Fprintf(stdout, "  last error  %s\n", said)
	}
	if status.GetWritable() {
		fmt.Fprintln(stdout, "  writes      a key a declaration names and the source lacks is created there, never overwritten")
	}
	for _, link := range status.GetLinks() {
		fmt.Fprintf(stdout, "  %-10s  %s\n", folderOrRoot(link.GetFolder()), link.GetLink())
	}
	if credentials := status.GetCredentials(); len(credentials) > 0 {
		fmt.Fprintf(stdout, "  signs in as %s, held in ocel's own store\n", strings.Join(credentials, " and "))
	}
}

func lag(now time.Time, success int64) string {
	if success == 0 {
		return ""
	}
	behind := now.Sub(time.Unix(success, 0)).Round(time.Second)
	if behind < 0 {
		behind = 0
	}
	return fmt.Sprintf(" (%s ago)", behind)
}

func renderDevSource(stdout io.Writer, dev envsource.Descriptor) {
	fmt.Fprintf(stdout, "dev reads from %s, then %s on top\n", dev.ID(), dotenv.LocalFileName)
}
