package env

import (
	"context"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/ocelhq/ocel/cli/internal/commands"
	"github.com/ocelhq/ocel/cli/internal/project"
	"github.com/ocelhq/ocel/cli/internal/providerprocess"
	"github.com/ocelhq/ocel/cli/internal/run"
	"github.com/ocelhq/ocel/cli/internal/terminal"
	"github.com/ocelhq/ocel/cli/internal/variables"
	"github.com/ocelhq/ocel/pkg/envsource"
	resultv1 "github.com/ocelhq/ocel/pkg/proto/cli/result/v1"
	cloudv1 "github.com/ocelhq/ocel/pkg/proto/cloud/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	variablestorev1 "github.com/ocelhq/ocel/pkg/proto/provider/variablestore/v1"
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
	return commands.DeclareResult(commands.DeclareReadOnly(cmd), &resultv1.EnvSourceResult{})
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
	return commands.DeclareMutating(cmd)
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
	return withEnvProvider(ctx, dependencies, cwd, opts, "ocel env sync", []cloudv1.Scope{cloudv1.Scope_SCOPE_VARIABLES_WRITE}, stderr, func(ctx context.Context, _ *run.Run, provider *providerprocess.Provider, cfg *project.Project, _ *contractv1.PreflightResponse) error {
		synced, err := variables.Store{Provider: provider, Project: cfg, Tier: opts.tier()}.SyncRegisteredEnvSource(ctx)
		if err != nil {
			return err
		}
		status := synced.GetStatus()
		if variables.EnvSourceOfStatus(status).OwnsValues() {
			fmt.Fprintf(stdout, "Synced %s from %s: %d written, %d removed, %d found there.\n",
				tierName(opts), status.GetEnvSource(), synced.GetWritten(), synced.GetRemoved(), len(synced.GetPresent()))
			for _, refused := range synced.GetRefused() {
				fmt.Fprintf(stdout, "  %s in %s was not copied: %s\n", refused.GetCell().GetKey(), folderOrRoot(refused.GetCell().GetFolder()), refused.GetReason())
			}
		} else {
			fmt.Fprintf(stdout, "Nothing to sync: %s reads ocel's own store (%s).\n", tierName(opts), status.GetEnvSource())
		}
		renderConfiguredEnvSource(stdout, dependencies.Presentation(stdout).Palette(), status, variables.EnvSourceDescriptor(cfg, opts.tier()), opts)
		return nil
	})
}

func runEnvSource(ctx context.Context, dependencies Dependencies, cwd string, opts envOptions, stdout, stderr io.Writer) error {
	return withEnvProvider(ctx, dependencies, cwd, opts, "ocel env source", nil, stderr, func(ctx context.Context, _ *run.Run, provider *providerprocess.Provider, cfg *project.Project, _ *contractv1.PreflightResponse) error {
		variableStore, err := provider.VariableStore()
		if err != nil {
			return err
		}
		req := &variablestorev1.DescribeEnvSourceRequest{Tier: opts.tier(), Slug: cfg.Slug}
		described, err := variableStore.DescribeEnvSource(ctx, req)
		if err != nil {
			return err
		}
		configured := variables.EnvSourceDescriptor(cfg, opts.tier())
		present := dependencies.Presentation(stdout)
		if present.Format == terminal.FormatJSON {
			return writeEnvSourceJSON(stdout, req, described.GetStatus(), configured, cfg.EnvSource.Dev.ID())
		}
		palette := present.Palette()
		renderEnvSource(stdout, palette, tierName(opts), described.GetStatus(), time.Now())
		renderConfiguredEnvSource(stdout, palette, described.GetStatus(), configured, opts)
		fmt.Fprintf(stdout, "%s reads from %s, then %s on top\n", palette.Bold("dev"), cfg.EnvSource.Dev.ID(), project.LocalDotenvFileName)
		return nil
	})
}

func writeEnvSourceJSON(stdout io.Writer, req *variablestorev1.DescribeEnvSourceRequest, status *variablestorev1.EnvSourceStatus, configured envsource.Descriptor, devEnvSource string) error {
	result := &resultv1.EnvSourceResult{
		Tier:                req.GetTier(),
		EnvSource:           status.GetEnvSource(),
		OwnsValues:          variables.EnvSourceOfStatus(status).OwnsValues(),
		Scheduled:           status.GetScheduled(),
		CanCreate:           status.GetCanCreate(),
		CanUpdate:           status.GetCanUpdate(),
		LastAttemptAt:       terminal.EpochRFC3339(status.GetLastAttemptAt()),
		LastSuccessAt:       terminal.EpochRFC3339(status.GetLastSuccessAt()),
		LastError:           status.GetLastError(),
		Links:               make([]*resultv1.EnvSourceLink, 0, len(status.GetLinks())),
		Credentials:         status.GetCredentials(),
		ConfiguredEnvSource: configured.ID(),
		DevEnvSource:        devEnvSource,
		DevLocalFile:        project.LocalDotenvFileName,
	}
	for _, link := range status.GetLinks() {
		result.Links = append(result.Links, &resultv1.EnvSourceLink{Folder: link.GetFolder(), Url: link.GetUrl()})
	}
	return terminal.WriteResultJSON(stdout, result)
}

func renderEnvSource(stdout io.Writer, palette terminal.Palette, tier string, status *variablestorev1.EnvSourceStatus, now time.Time) {
	fmt.Fprintf(stdout, "%s reads from %s\n", palette.Bold(tier), status.GetEnvSource())
	if !variables.EnvSourceOfStatus(status).OwnsValues() {
		return
	}
	fields := terminal.NewTable(terminal.Column{}, terminal.Column{})
	field := func(name, value string) { fields.Row(palette.Muted(name), value) }
	schedule := "no schedule: read at each deploy"
	if status.GetScheduled() {
		schedule = "synced every minute"
	}
	field("schedule", schedule)
	field("last sync", syncedAt(status.GetLastSuccessAt())+lag(now, status.GetLastSuccessAt()))
	if status.GetLastAttemptAt() != status.GetLastSuccessAt() {
		field("last try", syncedAt(status.GetLastAttemptAt()))
	}
	if lastError := status.GetLastError(); lastError != "" {
		field("last error", palette.Failure(lastError))
	}
	switch {
	case status.GetCanUpdate():
		field("writes", "creates and updates values, never deletes one")
	case status.GetCanCreate():
		field("writes", "creates the declared keys it lacks, never overwrites one")
	}
	for _, link := range status.GetLinks() {
		field(folderOrRoot(link.GetFolder()), link.GetUrl())
	}
	if credentials := status.GetCredentials(); len(credentials) > 0 {
		field("logs in with", strings.Join(credentials, " and ")+palette.Muted(", kept in ocel's store"))
	}
	fmt.Fprint(stdout, fields.Render(palette, "  "))
}

func renderConfiguredEnvSource(stdout io.Writer, palette terminal.Palette, status *variablestorev1.EnvSourceStatus, configured envsource.Descriptor, opts envOptions) {
	if configured.ID() == status.GetEnvSource() {
		return
	}
	fmt.Fprintf(stdout, "%s %s\n", palette.WarnMark(), palette.Commands(fmt.Sprintf("The config names %s instead; `%s` reads from it.", configured.ID(), deployCommand(opts))))
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
