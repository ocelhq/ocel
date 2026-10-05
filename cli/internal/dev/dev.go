package dev

import (
	"context"
	"errors"
	"fmt"
	"io"
	"maps"

	"github.com/ocelhq/ocel/cli/internal/dev/leader"
	"github.com/ocelhq/ocel/cli/internal/devresources"
	"github.com/ocelhq/ocel/cli/internal/devresources/docker"
	"github.com/ocelhq/ocel/cli/internal/exitcode"
	"github.com/ocelhq/ocel/cli/internal/project"
	"github.com/ocelhq/ocel/cli/internal/run"
	"github.com/ocelhq/ocel/cli/internal/telemetry"
	"github.com/ocelhq/ocel/cli/internal/variablescope"
	"github.com/ocelhq/ocel/pkg/progress"
	progressv1 "github.com/ocelhq/ocel/pkg/proto/common/progress/v1"
)

var (
	environmentTitle = progress.Title{Started: "Resolving the app's environment", Ended: "Resolved the app's environment"}
	changeTitle      = progress.Title{Started: "Re-resolving the app's environment after a change", Ended: "Re-resolved the app's environment after a change"}
	leaderTitle      = progress.Title{Started: "Connecting to the running `ocel dev`", Ended: "Connected to the running `ocel dev`"}
)

type Options struct {
	Project         *project.Project
	Command         []string
	OpenDocker      docker.OpenFunc
	Stdin           io.Reader
	Stdout          io.Writer
	Stderr          io.Writer
	StdinIsTerminal bool
	Run             *run.Run
	RecordSession   func(telemetry.DevSession)
}

func (o Options) session() *run.Span {
	return o.Run.Phase(progressv1.Phase_PHASE_UNSPECIFIED)
}

func Run(ctx context.Context, opts Options, reset bool) error {
	for range 3 {
		running, found, err := leader.Find(opts.Project.Dir)
		if err != nil {
			return fmt.Errorf("look for a running ocel dev: %w", err)
		}

		if found {
			if reset {
				return errors.New("`ocel dev` is already running for this project and owns its dev resources: stop it, then run `ocel dev --reset`")
			}
			return follow(ctx, opts, running)
		}

		if err := lead(ctx, opts, reset); !errors.Is(err, leader.ErrAlreadyRunning) {
			return err
		}
	}
	return errors.New("start ocel dev: another ocel dev kept claiming this project first; try again")
}

func RunOnce(ctx context.Context, opts Options, cwd string) error {
	running, found, err := leader.Find(opts.Project.Dir)
	if err != nil {
		return fmt.Errorf("look for a running dev server: %w", err)
	}
	if found {
		stream, env, err := subscribe(ctx, opts, running)
		if err != nil {
			return err
		}
		defer stream.Close()
		return runChild(ctx, opts, env)
	}
	return runStandalone(ctx, opts, cwd)
}

func lead(ctx context.Context, opts Options, reset bool) (err error) {
	cfg := opts.Project
	tally := newSession(opts.RecordSession)
	defer func() {
		if !errors.Is(err, leader.ErrAlreadyRunning) {
			tally.end(err)
		}
	}()
	startup := opts.session().Child("", environmentTitle)
	defer func() { startup.End(err) }()
	source, err := readValueSource(ctx, cfg)
	if err != nil {
		return err
	}
	values, err := source.read(cfg.Dir)
	if err != nil {
		return err
	}
	reportValues(startup, cfg.Dir, values, true)

	if reset {
		if err := devresources.Reset(ctx, opts.OpenDocker, stateDir(cfg), devresources.ProjectName(cfg.Dir)); err != nil {
			return err
		}
	}

	host, err := startHost(ctx, opts, source)
	if err != nil {
		return err
	}
	claimed := false
	defer func() {
		host.close()
		if claimed {
			_ = leader.Release(cfg.Dir, host.server.AppToken())
		}
	}()
	server := host.server
	invoked := invocation{name: "dev", source: source}

	background, stopBackground := context.WithCancel(ctx)
	defer stopBackground()

	if err := leader.Claim(cfg.Dir, leader.Leader{Address: host.address, Token: server.AppToken()}); err != nil {
		return err
	}
	claimed = true

	resolved, err := resolveOnce(ctx, server, cfg, invoked, startup)
	if err != nil {
		return err
	}
	tally.noteKinds(server.ResourceKinds())
	updates := make(chan map[string]string, 1)
	watching, err := startWatching(background, server, cfg, invoked, opts.session(), func(env map[string]string) {
		tally.noteKinds(server.ResourceKinds())
		select {
		case <-updates:
		default:
		}
		updates <- env
	}, tally.noteError)
	if err != nil {
		return fmt.Errorf("watch discovery paths: %w", err)
	}
	defer func() {
		stopBackground()
		<-watching.Done()
	}()
	server.PushEnv(resolved)
	startup.End(nil)

	child, err := startChild(ctx, opts, resolved)
	if err != nil {
		return err
	}
	workers := newWorkerProcesses(opts, host.resources.Queue())
	defer workers.stop(ctx)
	workers.restart(ctx, resolved)
	for {
		select {
		case err := <-child.Exited():
			return exitError(ctx, err)
		case env := <-updates:
			workers.restart(ctx, env)
			if maps.Equal(env, resolved) {
				continue
			}
			resolved = env
			tally.noteReload()
			child.Stop()
			child, err = startChild(ctx, opts, resolved)
			if err != nil {
				return err
			}
		}
	}
}

func follow(ctx context.Context, opts Options, running leader.Leader) (err error) {
	tally := newSession(opts.RecordSession)
	defer func() { tally.end(err) }()
	stream, first, err := subscribe(ctx, opts, running)
	if err != nil {
		return err
	}
	defer stream.Close()

	child, err := startChild(ctx, opts, first)
	if err != nil {
		return err
	}

	updates := make(chan map[string]string)
	streamDone := make(chan struct{}, 1)
	go func() {
		for {
			env, err := stream.Next()
			if err != nil {
				streamDone <- struct{}{}
				return
			}
			select {
			case updates <- env:
			case <-ctx.Done():
				return
			}
		}
	}()

	for {
		select {
		case err := <-child.Exited():
			return exitError(ctx, err)
		case env := <-updates:
			tally.noteReload()
			child.Stop()
			child, err = startChild(ctx, opts, env)
			if err != nil {
				return err
			}
		case <-streamDone:
			child.Stop()
			if ctx.Err() != nil {
				return &exitcode.ExitError{Code: exitcode.Interrupt}
			}
			return errors.New("the leader disconnected: restart `ocel dev` in the leader's terminal, then re-run this command")
		}
	}
}

func subscribe(ctx context.Context, opts Options, running leader.Leader) (_ *leader.EnvStream, _ map[string]string, err error) {
	connecting := opts.session().Child("", leaderTitle)
	defer func() { connecting.End(err) }()
	stream, err := leader.Subscribe(ctx, running)
	if err != nil {
		return nil, nil, fmt.Errorf("connect to leader: %w", err)
	}
	first, err := stream.Next()
	if err != nil {
		stream.Close()
		if errors.Is(err, io.EOF) {
			return nil, nil, errors.New("connect to leader: stream closed before first env push")
		}
		return nil, nil, fmt.Errorf("connect to leader: %w", err)
	}
	return stream, first, nil
}

func runStandalone(ctx context.Context, opts Options, cwd string) error {
	cfg := opts.Project
	startup := opts.session().Child("", environmentTitle)
	source, err := readValueSource(ctx, cfg)
	if err != nil {
		return err
	}
	values, err := source.read(cfg.Dir)
	if err != nil {
		return err
	}
	reportUnreadableLines(startup, values)
	reportValues(startup, cfg.Dir, values, false)

	host, err := startHost(ctx, opts, source)
	if err != nil {
		return err
	}
	defer host.close()
	host.server.UseValues(values.merged(), variablescope.ForDev(cfg))

	resolved, err := discoverAndSync(ctx, host.server, cfg, values, targetScope(cfg, cwd), invocation{name: "run", source: source}, startup)
	if err != nil {
		return err
	}
	startup.End(nil)

	return runChild(ctx, opts, resolved)
}
