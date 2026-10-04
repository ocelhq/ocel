package logs

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"time"

	"connectrpc.com/connect"
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
			return runLogs(cmd.Context(), dependencies, cwd, args, opts, cmd.OutOrStdout(), cmd.ErrOrStderr())
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
	cmd.Flags().BoolVarP(&opts.tail, "tail", "t", false, "Keep streaming new entries after the history")
	cmd.Flags().StringVar(&opts.stopAfter, "for", "", "Stop a --tail after this `duration` (30s, 5m)")
	cmd.Flags().BoolVar(&opts.raw, "raw", false, "Print messages as sent, without reading a level or fields out of them")
	return commands.DeclareReadOnly(commands.ReserveStdout(cmd))
}

type logsQuery struct {
	request   *contractv1.ReadLogsRequest
	stopAfter time.Duration
}

var errStopAfterElapsed = errors.New("--for elapsed")

func runLogs(ctx context.Context, dependencies Dependencies, cwd string, apps []string, opts logsOptions, stdout, stderr io.Writer) error {
	query, mode, err := opts.resolve(dependencies, cwd)
	if err != nil {
		return err
	}
	cfg, err := dependencies.LoadProject(ctx, cwd)
	if err != nil {
		return err
	}
	query.request.Slug, query.request.Edge, query.request.Apps = cfg.Slug, cfg.EdgeSelection(), apps

	present := dependencies.Presentation(stdout)
	mode.json = present.Format == terminal.FormatJSON
	open := commands.OpenOptions{Tier: query.request.GetEnvironment().GetTier(), Require: readiness.Infrastructure}
	return dependencies.WithProvider(ctx, cfg, "ocel logs", open, func(ctx context.Context, p commands.ProviderRun) error {
		p.Check.End(nil)
		out := newOutput(stdout, present, mode, newNotices(stderr, dependencies.Presentation(stderr).Palette(), p.Check.Warn))
		return query.read(ctx, p.Provider, out)
	})
}

func (q logsQuery) read(ctx context.Context, provider *providerprocess.Provider, out *output) error {
	if !q.request.GetTail() {
		return providerprocess.ReadLogs(ctx, provider, q.request, out.printResponse)
	}
	tailCtx := ctx
	if q.stopAfter > 0 {
		var stop context.CancelFunc
		tailCtx, stop = context.WithTimeoutCause(ctx, q.stopAfter, errStopAfterElapsed)
		defer stop()
	}
	err := providerprocess.ReadLogs(tailCtx, provider, q.request, out.printResponse)
	if err != nil && isTailStopped(ctx, tailCtx, err) {
		return nil
	}
	return err
}

func isTailStopped(command, tail context.Context, err error) bool {
	switch {
	case command.Err() != nil:
		return errors.Is(context.Cause(command), context.Canceled) && providerprocess.IsCancelled(err)
	case errors.Is(context.Cause(tail), errStopAfterElapsed):
		return errors.Is(err, context.DeadlineExceeded) || connect.CodeOf(err) == connect.CodeDeadlineExceeded
	}
	return false
}

func parseStopAfter(value string) (time.Duration, error) {
	stopAfter, err := time.ParseDuration(value)
	if err != nil || stopAfter <= 0 {
		return 0, fmt.Errorf("--for %q is not a positive duration such as 30s or 5m", value)
	}
	return stopAfter, nil
}

func (o logsOptions) resolve(dependencies Dependencies, cwd string) (logsQuery, outputMode, error) {
	mode := outputMode{raw: o.raw, tail: o.tail}
	if o.level != "" {
		level, ok := logview.ParseLevel(o.level)
		if !ok {
			return logsQuery{}, outputMode{}, fmt.Errorf("--level %q is not one of debug, info, warn or error", o.level)
		}
		mode.minLevel = level
	}
	var query logsQuery
	if o.stopAfter != "" {
		stopAfter, err := parseStopAfter(o.stopAfter)
		if err != nil {
			return logsQuery{}, outputMode{}, err
		}
		if !o.tail {
			return logsQuery{}, outputMode{}, errors.New("--for stops a --tail, so pass --tail with it")
		}
		query.stopAfter = stopAfter
	}
	if o.tail && o.until != "" {
		return logsQuery{}, outputMode{}, errors.New("--until ends a window, which a --tail never has, so drop one of them")
	}
	if o.lines < 1 || o.lines > maxLines {
		return logsQuery{}, outputMode{}, fmt.Errorf("--lines %d is outside 1 to %d", o.lines, maxLines)
	}
	now := dependencies.Now()
	since, err := parseMoment(now, "--since", o.since)
	if err != nil {
		return logsQuery{}, outputMode{}, err
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
			return logsQuery{}, outputMode{}, err
		}
		if until.Before(since) {
			return logsQuery{}, outputMode{}, fmt.Errorf("--until %s is before --since %s", until.Format(time.RFC3339), since.Format(time.RFC3339))
		}
		request.Until = timestamppb.New(until)
	}
	if request.Environment, err = o.resolveEnvironment(dependencies, cwd); err != nil {
		return logsQuery{}, outputMode{}, err
	}
	query.request = request
	return query, mode, nil
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
