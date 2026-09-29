package env

import (
	"context"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/ocelhq/ocel/cli/internal/dotfile"
	"github.com/ocelhq/ocel/cli/internal/project"
	"github.com/ocelhq/ocel/cli/internal/providerprocess"
	"github.com/ocelhq/ocel/cli/internal/run"
	"github.com/ocelhq/ocel/cli/internal/terminal"
	"github.com/ocelhq/ocel/cli/internal/valuestore"
	"github.com/ocelhq/ocel/cli/internal/variablescope"
	"github.com/ocelhq/ocel/pkg/envsource"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	envvarsv1 "github.com/ocelhq/ocel/pkg/proto/provider/envvars/v1"
)

func newEnvSourceCommand(dependencies Dependencies) *cobra.Command {
	var opts envOptions
	cmd := &cobra.Command{
		Use:     "source",
		Short:   "Show where a tier's values are read from, and how its last sync went",
		Example: "  $ ocel env source\n  $ ocel env source --preview",
		Args:    cobra.NoArgs,
	}
	cmd.RunE = func(cmd *cobra.Command, _ []string) error {
		return withCommand(cmd, dependencies, func(ctx context.Context, cwd string) error {
			return runEnvSource(ctx, dependencies, cwd, opts, cmd.OutOrStdout(), cmd.ErrOrStderr())
		})
	}
	previewFlag(cmd, &opts)
	return cmd
}

func newSyncCommand(dependencies Dependencies) *cobra.Command {
	var opts envOptions
	cmd := &cobra.Command{
		Use:     "sync",
		Short:   "Read the env source a tier's last deploy registered into its values now",
		Example: "  $ ocel env sync\n  $ ocel env sync --preview",
		Args:    cobra.NoArgs,
	}
	cmd.RunE = func(cmd *cobra.Command, _ []string) error {
		return withCommand(cmd, dependencies, func(ctx context.Context, cwd string) error {
			return runEnvSync(ctx, dependencies, cwd, opts, cmd.OutOrStdout(), cmd.ErrOrStderr())
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

func deployCommand(opts envOptions) string {
	if opts.preview {
		return "ocel preview up"
	}
	return "ocel deploy"
}

func runEnvSync(ctx context.Context, dependencies Dependencies, cwd string, opts envOptions, stdout, stderr io.Writer) error {
	return withEnvProvider(ctx, dependencies, cwd, opts, "ocel env sync", stderr, func(ctx context.Context, _ *run.Run, provider *providerprocess.Provider, cfg *project.Project, _ *contractv1.PreflightResponse) error {
		synced, err := valuestore.Store{Provider: provider, Project: cfg, Tier: opts.tier()}.SyncRegisteredEnvSource(ctx)
		if err != nil {
			return err
		}
		status := synced.GetStatus()
		if valuestore.EnvSourceOfStatus(status).OwnsValues() {
			fmt.Fprintf(stdout, "Synced %s from %s: %d written, %d removed, %d found there.\n",
				tierName(opts), status.GetEnvSource(), synced.GetWritten(), synced.GetRemoved(), len(synced.GetPresent()))
			for _, refused := range synced.GetRefused() {
				fmt.Fprintf(stdout, "  %s in %s was not copied: %s\n", refused.GetCell().GetKey(), folderOrRoot(refused.GetCell().GetFolder()), refused.GetReason())
			}
		} else {
			fmt.Fprintf(stdout, "%s reads ocel's own store (%s), so there is nothing to sync.\n", tierName(opts), status.GetEnvSource())
		}
		renderConfiguredEnvSource(stdout, status, variablescope.EnvSourceDescriptor(cfg, opts.tier()), opts)
		return nil
	})
}

func runEnvSource(ctx context.Context, dependencies Dependencies, cwd string, opts envOptions, stdout, stderr io.Writer) error {
	return withEnvProvider(ctx, dependencies, cwd, opts, "ocel env source", stderr, func(ctx context.Context, _ *run.Run, provider *providerprocess.Provider, cfg *project.Project, _ *contractv1.PreflightResponse) error {
		envVars, err := provider.EnvVars()
		if err != nil {
			return err
		}
		described, err := envVars.DescribeEnvSource(ctx, &envvarsv1.DescribeEnvSourceRequest{Tier: opts.tier(), Slug: cfg.Slug})
		if err != nil {
			return err
		}
		renderEnvSource(stdout, tierName(opts), described.GetStatus(), time.Now())
		renderConfiguredEnvSource(stdout, described.GetStatus(), variablescope.EnvSourceDescriptor(cfg, opts.tier()), opts)
		fmt.Fprintf(stdout, "dev reads from %s, then %s on top\n", cfg.EnvSource.Dev.ID(), dotfile.LocalFileName)
		return nil
	})
}

func renderEnvSource(stdout io.Writer, tier string, status *envvarsv1.EnvSourceStatus, now time.Time) {
	fmt.Fprintf(stdout, "%s reads from %s\n", tier, status.GetEnvSource())
	if !valuestore.EnvSourceOfStatus(status).OwnsValues() {
		return
	}
	schedule := "no schedule: read at each deploy"
	if status.GetScheduled() {
		schedule = "synced every minute"
	}
	fmt.Fprintf(stdout, "  schedule    %s\n", schedule)
	fmt.Fprintf(stdout, "  last sync   %s%s\n", syncedAt(status.GetLastSuccessAt()), lag(now, status.GetLastSuccessAt()))
	if status.GetLastAttemptAt() != status.GetLastSuccessAt() {
		fmt.Fprintf(stdout, "  last try    %s\n", syncedAt(status.GetLastAttemptAt()))
	}
	if lastError := status.GetLastError(); lastError != "" {
		fmt.Fprintf(stdout, "  last error  %s\n", lastError)
	}
	switch {
	case status.GetCanUpdate():
		fmt.Fprintln(stdout, "  writes      ocel env set and the variables page create or update a value there, never delete one")
	case status.GetCanCreate():
		fmt.Fprintln(stdout, "  writes      a key a declaration names and the env source lacks is created there, never overwritten")
	}
	for _, link := range status.GetLinks() {
		fmt.Fprintf(stdout, "  %-10s  %s\n", folderOrRoot(link.GetFolder()), link.GetUrl())
	}
	if credentials := status.GetCredentials(); len(credentials) > 0 {
		fmt.Fprintf(stdout, "  logs in with %s, stored in ocel's own store\n", strings.Join(credentials, " and "))
	}
}

func renderConfiguredEnvSource(stdout io.Writer, status *envvarsv1.EnvSourceStatus, configured envsource.Descriptor, opts envOptions) {
	if configured.ID() == status.GetEnvSource() {
		return
	}
	fmt.Fprintf(stdout, "The config names %s for %s instead; `%s` reads from it.\n", configured.ID(), tierName(opts), deployCommand(opts))
}

func syncedAt(unixSeconds int64) string {
	if unixSeconds == 0 {
		return "never"
	}
	return terminal.EpochDateTime(unixSeconds)
}

func lag(now time.Time, unixSeconds int64) string {
	if unixSeconds == 0 {
		return ""
	}
	behind := max(now.Sub(time.Unix(unixSeconds, 0)).Round(time.Second), 0)
	return fmt.Sprintf(" (%s ago)", behind)
}
