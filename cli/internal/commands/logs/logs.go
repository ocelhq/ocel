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
	tail        bool
	stopAfter   string
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
			"cannot be told is dropped by it. A line's level is the one its message names, else the " +
			"severity the provider recorded; a failure the provider reports, such as a timeout, is an error. " +
			"With --raw, messages are not read, so the level is the provider's alone.\n\n" +
			"On a terminal, each entry is one coloured line stamped in local time and errors are boxed, with long field values " +
			"shortened unless -v is set. A stack trace whose lines arrive as separate entries is shown as one. " +
			"--json and --raw print the same lines a pipe gets.\n\n" +
			"--tail keeps streaming new entries once the history is printed; a terminal marks the switch with a " +
			"live line. Ctrl-C stops it, and so does --for, both with exit status 0. A tail reads no window, so " +
			"it refuses --until. Notices that entries were left out or the stream reconnected go to stderr.",
		Example: "  $ ocel logs\n" +
			"  $ ocel logs web --since 15m --level warn\n" +
			"  $ ocel logs --tail --for 5m\n" +
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
	cmd.Flags().BoolVarP(&opts.tail, "tail", "t", false, "Keep streaming new entries after the history")
	cmd.Flags().StringVar(&opts.stopAfter, "for", "", "Stop a --tail after this `duration` (30s, 5m)")
	cmd.Flags().BoolVar(&opts.raw, "raw", false, "Print messages as sent, without reading a level or fields out of them")
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

	present := dependencies.Presentation(stdout)
	asJSON := opts.json || present.Format == terminal.FormatJSON
	open := commands.OpenOptions{Tier: request.GetEnvironment().GetTier(), Require: readiness.Infrastructure}
	return dependencies.WithProvider(ctx, cfg, "ocel logs", open, func(ctx context.Context, p commands.ProviderRun) error {
		p.Check.End(nil)
		out := newOutput(stdout, present, asJSON, opts.raw, minLevel, opts.tail, notices{warn: p.Check.Warn, note: p.Check.Say})
		return opts.read(ctx, p.Provider, request, out)
	})
}

func (o logsOptions) read(ctx context.Context, provider *providerprocess.Provider, request *contractv1.ReadLogsRequest, out *output) error {
	if !o.tail {
		return providerprocess.ReadLogs(ctx, provider, request, out.printResponse)
	}
	stopAfter, _ := o.parseStopAfter()
	if stopAfter > 0 {
		var stop context.CancelFunc
		ctx, stop = context.WithTimeout(ctx, stopAfter)
		defer stop()
	}
	err := providerprocess.ReadLogs(ctx, provider, request, out.printResponse)
	if ctx.Err() != nil && (err == nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)) {
		return nil
	}
	return err
}

func (o logsOptions) parseStopAfter() (time.Duration, error) {
	if o.stopAfter == "" {
		return 0, nil
	}
	stopAfter, err := time.ParseDuration(o.stopAfter)
	if err != nil || stopAfter <= 0 {
		return 0, fmt.Errorf("--for %q is not a positive duration such as 30s or 5m", o.stopAfter)
	}
	return stopAfter, nil
}

func (o logsOptions) resolve(dependencies Dependencies, cwd string) (*contractv1.ReadLogsRequest, logview.Level, error) {
	var minLevel logview.Level
	if o.level != "" {
		level, ok := logview.ParseLevel(o.level)
		if !ok {
			return nil, 0, fmt.Errorf("--level %q is not one of debug, info, warn or error", o.level)
		}
		minLevel = level
	}
	if _, err := o.parseStopAfter(); err != nil {
		return nil, 0, err
	}
	if o.stopAfter != "" && !o.tail {
		return nil, 0, errors.New("--for stops a --tail, so pass --tail with it")
	}
	if o.tail && o.until != "" {
		return nil, 0, errors.New("--until ends a window, which a --tail never has, so drop one of them")
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
		Tail:        o.tail,
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
