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
	"github.com/ocelhq/ocel/cli/internal/projectconfig"
	"github.com/ocelhq/ocel/cli/internal/variablescope"
)

type Options struct {
	Config          *projectconfig.Config
	Command         []string
	OpenDocker      docker.OpenFunc
	Stdin           io.Reader
	Stdout          io.Writer
	Stderr          io.Writer
	StdinIsTerminal bool
}

func Run(ctx context.Context, opts Options, reset bool) error {
	for range 3 {
		running, found, err := leader.Find(opts.Config.Dir)
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
	running, found, err := leader.Find(opts.Config.Dir)
	if err != nil {
		return fmt.Errorf("look for a running dev server: %w", err)
	}
	if found {
		stream, env, err := subscribe(ctx, running)
		if err != nil {
			return err
		}
		defer stream.Close()
		return runChild(ctx, opts, env)
	}
	return runStandalone(ctx, opts, cwd)
}

func lead(ctx context.Context, opts Options, reset bool) error {
	cfg := opts.Config
	source, err := readValueSource(ctx, cfg)
	if err != nil {
		return err
	}
	values, err := source.read(cfg.Dir)
	if err != nil {
		return err
	}
	reportValues(opts.Stdout, cfg.Dir, values, true)

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
			_ = leader.Release(cfg.Dir)
		}
	}()
	srv := host.srv
	run := invocation{name: "dev", source: source}

	background, stopBackground := context.WithCancel(ctx)
	defer stopBackground()

	if err := leader.Claim(cfg.Dir, leader.Leader{Address: host.address, Token: srv.AppToken()}); err != nil {
		return err
	}
	claimed = true

	resolved, err := resolveOnce(ctx, srv, cfg, run, opts.Stdout, opts.Stderr)
	if err != nil {
		return err
	}
	updates := make(chan map[string]string, 1)
	watching, err := startWatching(background, srv, cfg, run, opts.Stdout, opts.Stderr, func(env map[string]string) {
		select {
		case <-updates:
		default:
		}
		updates <- env
	})
	if err != nil {
		return fmt.Errorf("watch discovery paths: %w", err)
	}
	defer func() {
		stopBackground()
		<-watching.Done()
	}()
	srv.PushEnv(resolved)

	child, err := startChild(ctx, opts, resolved)
	if err != nil {
		return err
	}
	for {
		select {
		case err := <-child.Exited():
			return exitError(ctx, err)
		case env := <-updates:
			if maps.Equal(env, resolved) {
				continue
			}
			resolved = env
			child.Stop()
			child, err = startChild(ctx, opts, resolved)
			if err != nil {
				return err
			}
		}
	}
}

func follow(ctx context.Context, opts Options, running leader.Leader) error {
	stream, first, err := subscribe(ctx, running)
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
			fmt.Fprintln(opts.Stderr, "Leader disconnected. Restart `ocel dev` in the leader's terminal, then re-run this command.")
			return &exitcode.ExitError{Code: 1}
		}
	}
}

func subscribe(ctx context.Context, running leader.Leader) (*leader.EnvStream, map[string]string, error) {
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
	cfg := opts.Config
	source, err := readValueSource(ctx, cfg)
	if err != nil {
		return err
	}
	values, err := source.read(cfg.Dir)
	if err != nil {
		return err
	}
	reportUnreadableLines(opts.Stdout, values)
	reportValues(opts.Stdout, cfg.Dir, values, false)

	host, err := startHost(ctx, opts, source)
	if err != nil {
		return err
	}
	defer host.close()
	host.srv.UseValues(values.merged(), variablescope.ForDev(cfg))

	resolved, err := discoverAndSync(ctx, host.srv, cfg, values, targetScope(cfg, cwd), invocation{name: "run", source: source}, opts.Stdout, opts.Stderr)
	if err != nil {
		return err
	}

	return runChild(ctx, opts, resolved)
}
