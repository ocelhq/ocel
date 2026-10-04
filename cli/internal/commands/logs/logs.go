package logs

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/spf13/cobra"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/ocelhq/ocel/cli/internal/commands"
	"github.com/ocelhq/ocel/cli/internal/logview"
	"github.com/ocelhq/ocel/cli/internal/previewid"
	"github.com/ocelhq/ocel/cli/internal/providerprocess"
	"github.com/ocelhq/ocel/cli/internal/readiness"
	"github.com/ocelhq/ocel/cli/internal/terminal"
	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
)

const (
	defaultLines = 100
	maxLines     = 10000
	defaultSince = "1h"
)

type Dependencies struct {
	commands.Invocation
	ReadGitBranch    func(dir string) (string, error)
	DiscoverPRNumber func() string
	Now              func() time.Time
}

type logsOptions struct {
	preview     bool
	environment string
	allReleases bool
	lines       int
	since       string
	until       string
	level       string
	grep        string
	json        bool
	raw         bool
}

func defaultLogsOptions() logsOptions {
	return logsOptions{lines: defaultLines, since: defaultSince}
}

func NewCommand(dependencies Dependencies) *cobra.Command {
	opts := defaultLogsOptions()
	cmd := &cobra.Command{
		Use:   "logs [app...]",
		Short: "Print the logs of this project's apps",
		Long: "Print the logs of this project's apps.\n\n" +
			"Reads what each app wrote to stdout and stderr from your own provider account, " +
			"newest -n lines of the --since to --until window, oldest first. Names no app, it reads " +
			"every app; names some, it reads those. Only the release each app is serving is read, " +
			"unless --all-releases adds the ones still running but no longer promoted.\n\n" +
			"--grep is a substring match the provider does where it can. --level is applied here, " +
			"after -n has chosen its lines, so it can leave you fewer than -n; a line whose level " +
			"cannot be told is dropped by it.",
		Example: "  $ ocel logs\n" +
			"  $ ocel logs web --since 15m --level warn\n" +
			"  $ ocel logs --preview --grep timeout --json",
		RunE: func(cmd *cobra.Command, args []string) error {
			cwd, err := os.Getwd()
			if err != nil {
				return fmt.Errorf("determine working directory: %w", err)
			}
			return runLogs(cmd.Context(), dependencies, cwd, args, opts, cmd.OutOrStdout())
		},
	}
	cmd.Flags().BoolVar(&opts.preview, "preview", false, "Read a preview instead of production")
	cmd.Flags().StringVar(&opts.environment, "environment", "", "Read this named preview environment instead of the one the current branch names (with --preview)")
	cmd.Flags().BoolVar(&opts.allReleases, "all-releases", false, "Include releases still running but no longer promoted")
	cmd.Flags().IntVarP(&opts.lines, "lines", "n", opts.lines, "Newest `N` entries to read, before --level is applied")
	cmd.Flags().StringVar(&opts.since, "since", opts.since, "Start of the window: a duration back from now (15m, 2h) or an RFC3339 time")
	cmd.Flags().StringVar(&opts.until, "until", opts.until, "End of the window, as --since (default: now)")
	cmd.Flags().StringVar(&opts.level, "level", "", "Minimum `level` to print: debug, info, warn or error")
	cmd.Flags().StringVar(&opts.grep, "grep", "", "Print only entries containing this `text`")
	cmd.Flags().BoolVar(&opts.json, "json", false, "Print one JSON object per line (also: --log-format json)")
	cmd.Flags().BoolVar(&opts.raw, "raw", false, "Print messages as sent, without parsing their level and fields")
	return commands.ReserveStdout(cmd)
}

func runLogs(ctx context.Context, dependencies Dependencies, cwd string, apps []string, opts logsOptions, stdout io.Writer) error {
	request, minLevel, err := opts.resolve(dependencies, cwd)
	if err != nil {
		return err
	}
	cfg, err := dependencies.LoadProject(ctx, cwd)
	if err != nil {
		return err
	}
	request.Slug, request.Edge, request.Apps = cfg.Slug, cfg.EdgeSelection(), apps

	asJSON := opts.json || dependencies.Presentation(stdout).Format == terminal.FormatJSON
	open := commands.OpenOptions{Tier: request.GetEnvironment().GetTier(), Require: readiness.Infrastructure}
	return dependencies.WithProvider(ctx, cfg, "ocel logs", open, func(ctx context.Context, p commands.ProviderRun) error {
		p.Check.End(nil)
		out := newOutput(stdout, asJSON, opts.raw, minLevel, p.Check.Warn)
		return providerprocess.ReadLogs(ctx, p.Provider, request, out.accept)
	})
}

func (o logsOptions) resolve(dependencies Dependencies, cwd string) (*contractv1.ReadLogsRequest, logview.Level, error) {
	var minLevel logview.Level
	if o.level != "" {
		level, ok := logview.ParseLevel(o.level)
		if !ok {
			return nil, 0, fmt.Errorf("--level %q is not one of debug, info, warn or error", o.level)
		}
		if o.raw {
			return nil, 0, errors.New("--level reads the level out of each message and --raw leaves messages unparsed; use one or the other")
		}
		minLevel = level
	}
	if o.lines < 1 || o.lines > maxLines {
		return nil, 0, fmt.Errorf("--lines %d is outside 1 to %d", o.lines, maxLines)
	}
	now := dependencies.Now()
	since, err := parseMoment(now, "--since", o.since)
	if err != nil {
		return nil, 0, err
	}
	request := &contractv1.ReadLogsRequest{
		AllReleases: o.allReleases,
		Since:       timestamppb.New(since),
		Limit:       uint32(o.lines),
		Contains:    o.grep,
	}
	if o.until != "" {
		until, err := parseMoment(now, "--until", o.until)
		if err != nil {
			return nil, 0, err
		}
		if until.Before(since) {
			return nil, 0, fmt.Errorf("--until %s is before --since %s", until.Format(time.RFC3339), since.Format(time.RFC3339))
		}
		request.Until = timestamppb.New(until)
	}
	if request.Environment, err = o.resolveEnvironment(dependencies, cwd); err != nil {
		return nil, 0, err
	}
	return request, minLevel, nil
}

func parseMoment(now time.Time, flag, value string) (time.Time, error) {
	if elapsed, err := time.ParseDuration(value); err == nil {
		if elapsed < 0 {
			return time.Time{}, fmt.Errorf("%s %q counts back from now, so it cannot be negative", flag, value)
		}
		return now.Add(-elapsed), nil
	}
	if moment, err := time.Parse(time.RFC3339, value); err == nil {
		return moment, nil
	}
	return time.Time{}, fmt.Errorf("%s %q is neither a duration such as 15m nor an RFC3339 time such as 2026-01-05T12:00:00Z", flag, value)
}

func (o logsOptions) resolveEnvironment(dependencies Dependencies, cwd string) (*environmentv1.Environment, error) {
	if !o.preview {
		if o.environment != "" {
			return nil, errors.New("--environment names a preview environment, so pass --preview with it")
		}
		return &environmentv1.Environment{Tier: environmentv1.Tier_TIER_PRODUCTION}, nil
	}
	if o.environment != "" {
		if err := previewid.ValidateLabel(o.environment); err != nil {
			return nil, err
		}
	}
	return commands.ResolvePreviewEnvironment(cwd, o.environment, environmentv1.Lifecycle_LIFECYCLE_UNSPECIFIED, dependencies.ReadGitBranch, dependencies.DiscoverPRNumber)
}
