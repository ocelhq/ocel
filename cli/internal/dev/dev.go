package dev

import (
	"context"
	"errors"
	"fmt"
	"io"
	"maps"
	"slices"

	"github.com/ocelhq/ocel/cli/internal/dev/leader"
	"github.com/ocelhq/ocel/cli/internal/devresources"
	"github.com/ocelhq/ocel/cli/internal/devresources/docker"
	"github.com/ocelhq/ocel/cli/internal/exitcode"
	"github.com/ocelhq/ocel/cli/internal/project"
	"github.com/ocelhq/ocel/cli/internal/run"
	"github.com/ocelhq/ocel/cli/internal/telemetry"
	"github.com/ocelhq/ocel/cli/internal/variables"
	"github.com/ocelhq/ocel/pkg/progress"
	progressv1 "github.com/ocelhq/ocel/pkg/proto/common/progress/v1"
)

var (
	environmentTitle = progress.Title{Started: "Resolving the app's environment", Ended: "Resolved the app's environment"}
	changeTitle      = progress.Title{Started: "Re-resolving the app's environment after a change", Ended: "Re-resolved the app's environment after a change"}
	leaderTitle      = progress.Title{Started: "Connecting to the running `ocel dev`", Ended: "Connected to the running `ocel dev`"}

	stoppingResourcesTitle = progress.Title{Started: "Stopping the dev resources", Ended: "Stopped the dev resources"}
	closingForwardsTitle   = progress.Title{Started: "Closing the forwarded ports", Ended: "Closed the forwarded ports"}
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
	RecordEvent     func(telemetry.Payload) bool
}

func (o Options) session() *run.Span {
	return o.Run.Phase(progressv1.Phase_PHASE_UNSPECIFIED)
}

func leaderKey(cfg *project.Project) string {
	return cfg.Path
}

func Run(ctx context.Context, opts Options, reset bool) error {
	for range 3 {
		running, found, err := leader.Find(ctx, leaderKey(opts.Project))
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
	running, found, err := leader.Find(ctx, leaderKey(opts.Project))
	if err != nil {
		return fmt.Errorf("look for a running dev server: %w", err)
	}
	if found {
		stream, env, err := subscribe(ctx, opts, running)
		if err != nil {
			return err
		}
		defer stream.Close()
		return RunProjected(ctx, opts, env)
	}
	return runStandalone(ctx, opts, cwd)
}

func RunProjected(ctx context.Context, opts Options, env map[string]string) error {
	live, err := newLiveDir()
	if err != nil {
		return err
	}
	defer live.remove()
	appEnv, err := live.project(env)
	if err != nil {
		return err
	}
	return runChild(ctx, opts, appEnv)
}

func lead(ctx context.Context, opts Options, reset bool) (err error) {
	cfg := opts.Project
	tally := newTally(opts.RecordEvent, telemetry.NewStack(opts.Project))
	defer func() {
		if !errors.Is(err, leader.ErrAlreadyRunning) {
			tally.end(err)
		}
	}()
	startupCtx, startup := opts.session().Child(ctx, "", environmentTitle)
	defer func() { startup.End(err) }()
	source, err := readValueSource(startupCtx, cfg)
	if err != nil {
		return err
	}
	values, err := source.read(cfg.Dir)
	if err != nil {
		return err
	}
	reportValues(startupCtx, startup, cfg.Dir, values, true)

	if reset {
		if err := devresources.Reset(startupCtx, opts.OpenDocker, stateDir(cfg), resourcesName(cfg)); err != nil {
			return err
		}
	}

	host, err := startHost(startupCtx, opts, source)
	if err != nil {
		return err
	}
	claimed := false
	defer func() {
		host.close()
		if claimed {
			_ = leader.Release(leaderKey(cfg), host.server.AppToken())
		}
	}()
	server := host.server
	local := localCommand{name: "dev", source: source}

	background, stopBackground := context.WithCancel(ctx)
	defer stopBackground()

	if err := leader.Claim(startupCtx, leaderKey(cfg), leader.Leader{Address: host.address, Token: server.AppToken()}); err != nil {
		return err
	}
	claimed = true

	resolved, err := resolveOnce(ctx, server, cfg, local, startup)
	if err != nil {
		return err
	}
	tally.noteKinds(server.ResourceKinds())
	updates := make(chan reload, 1)
	watching, err := startWatching(background, server, cfg, local, opts.session(), func(env map[string]string, changed []string) {
		tally.noteKinds(server.ResourceKinds())
		next := reload{env: env, changed: changed}
		select {
		case pending := <-updates:
			next.changed = slices.Concat(pending.changed, changed)
		default:
		}
		updates <- next
	}, tally.noteError)
	if err != nil {
		return fmt.Errorf("watch discovery paths: %w", err)
	}
	defer func() {
		stopBackground()
		<-watching.Done()
	}()
	server.PushEnv(resolved)
	live, err := newLiveDir()
	if err != nil {
		return err
	}
	defer live.remove()
	appEnv, err := live.project(resolved)
	if err != nil {
		return err
	}
	startup.End(nil)

	child, err := startChild(ctx, opts, appEnv)
	if err != nil {
		return err
	}
	workers := newWorkerProcesses(opts, host.resources.Queue())
	defer workers.close(ctx)
	workers.restart(ctx, appEnv, nil)
	for {
		select {
		case err := <-child.Exited():
			return exitError(ctx, err)
		case update := <-updates:
			env := update.env
			appEnv, err := live.project(env)
			if err != nil {
				return err
			}
			workers.restart(ctx, appEnv, update.changed)
			if maps.Equal(env, resolved) {
				live.retire()
				continue
			}
			resolved = env
			tally.noteReload()
			child.Stop()
			child, err = startChild(ctx, opts, appEnv)
			if err != nil {
				return err
			}
			live.retire()
		}
	}
}

type reload struct {
	env     map[string]string
	changed []string
}

func follow(ctx context.Context, opts Options, running leader.Leader) (err error) {
	tally := newTally(opts.RecordEvent, telemetry.NewStack(opts.Project))
	defer func() { tally.end(err) }()
	stream, first, err := subscribe(ctx, opts, running)
	if err != nil {
		return err
	}
	defer stream.Close()

	live, err := newLiveDir()
	if err != nil {
		return err
	}
	defer live.remove()
	appEnv, err := live.project(first)
	if err != nil {
		return err
	}
	child, err := startChild(ctx, opts, appEnv)
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
			appEnv, err := live.project(env)
			if err != nil {
				return err
			}
			child.Stop()
			child, err = startChild(ctx, opts, appEnv)
			if err != nil {
				return err
			}
			live.retire()
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
	connectingCtx, connecting := opts.session().Child(ctx, "", leaderTitle)
	defer func() { connecting.End(err) }()
	stream, err := leader.Subscribe(connectingCtx, running)
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
	startupCtx, startup := opts.session().Child(ctx, "", environmentTitle)
	source, err := readValueSource(startupCtx, cfg)
	if err != nil {
		return err
	}
	values, err := source.read(cfg.Dir)
	if err != nil {
		return err
	}
	reportUnreadableLines(startup, values)
	reportValues(startupCtx, startup, cfg.Dir, values, false)

	host, err := startHost(startupCtx, opts, source)
	if err != nil {
		return err
	}
	defer host.close()
	host.server.UseValues(values.merged(), variables.DevScope(cfg))

	resolved, err := discoverAndSync(startupCtx, host.server, cfg, values, targetScope(cfg, cwd), localCommand{name: "run", source: source}, startup)
	if err != nil {
		return err
	}
	startup.End(nil)

	return RunProjected(ctx, opts, resolved)
}
