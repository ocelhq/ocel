package dev

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"net"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ocelhq/ocel/cli/internal/childprocess"
	"github.com/ocelhq/ocel/cli/internal/devresources/queue"
	"github.com/ocelhq/ocel/cli/internal/discovery"
	"github.com/ocelhq/ocel/cli/internal/workdir"
	"github.com/ocelhq/ocel/pkg/processenv"
	"github.com/ocelhq/ocel/pkg/tracing"
)

const (
	workerHost          = "127.0.0.1"
	workerListensWithin = 5 * time.Minute
	workerDialInterval  = 100 * time.Millisecond
)

type workerQueue interface {
	Workers() []queue.Worker
	DeliverTo(ctx context.Context, urls map[string]string) error
}

type workerProcess struct {
	name    string
	served  string
	roots   []string
	env     map[string]string
	child   *childprocess.Child
	exited  chan struct{}
	stopped atomic.Bool

	stopWaiting context.CancelFunc
	waited      chan struct{}
}

func (p *workerProcess) hasExited() bool {
	select {
	case <-p.exited:
		return true
	default:
		return false
	}
}

func (p *workerProcess) runs(served discovery.Root, roots []string, env map[string]string, changed []string) bool {
	if p.hasExited() || p.served != served.Dir || !slices.Equal(p.roots, roots) || !maps.Equal(p.env, env) {
		return false
	}
	return !slices.ContainsFunc(changed, func(path string) bool {
		return slices.ContainsFunc(roots, func(root string) bool { return isWithin(root, path) })
	})
}

func isWithin(dir, path string) bool {
	rel, err := filepath.Rel(dir, path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

type workerProcesses struct {
	opts    Options
	run     *workdir.Run
	queue   workerQueue
	command func(ctx context.Context, configDir string, roots []discovery.Root, served discovery.Root, run *workdir.Run) (*tracing.Cmd, error)
	ports   map[string]int
	running map[string]*workerProcess

	mu     sync.Mutex
	served map[string]string
}

func newWorkerProcesses(opts Options, backend workerQueue) *workerProcesses {
	return &workerProcesses{
		opts:    opts,
		run:     workdir.NewRun(),
		queue:   backend,
		command: discovery.WorkerCommand,
		ports:   map[string]int{},
		running: map[string]*workerProcess{},
		served:  map[string]string{},
	}
}

func (w *workerProcesses) restart(ctx context.Context, env map[string]string, changed []string) {
	workers := w.queue.Workers()
	if len(workers) == 0 {
		w.stop(ctx, slices.Collect(maps.Keys(w.running)))
		return
	}
	cfg := w.opts.Project
	roots, err := discovery.RootsOf(cfg)
	if err != nil {
		w.stop(ctx, slices.Collect(maps.Keys(w.running)))
		w.warnAll(err)
		return
	}

	type plan struct {
		served discovery.Root
		roots  []string
	}
	plans := map[string]plan{}
	for _, worker := range workers {
		served, err := discovery.WorkerRoot(cfg.Dir, roots, worker.Name, worker.Sources)
		if err != nil {
			w.warn(worker.Name, err)
			continue
		}
		program, err := discovery.WorkerRoots(cfg.Dir, roots, served)
		if err != nil {
			w.warn(worker.Name, err)
			continue
		}
		dirs := []string{served.Dir}
		for _, root := range program {
			if !slices.Contains(dirs, root.Dir) {
				dirs = append(dirs, root.Dir)
			}
		}
		plans[worker.Name] = plan{served: served, roots: dirs}
	}

	var stopping []string
	for name, process := range w.running {
		if p, wanted := plans[name]; !wanted || !process.runs(p.served, p.roots, env, changed) {
			stopping = append(stopping, name)
		}
	}
	w.stop(ctx, stopping)

	for _, worker := range workers {
		p, planned := plans[worker.Name]
		if _, running := w.running[worker.Name]; !planned || running {
			continue
		}
		cmd, err := w.command(ctx, cfg.Dir, roots, p.served, w.run)
		if err != nil {
			w.warn(worker.Name, err)
			continue
		}
		port, err := w.portOf(worker.Name)
		if err != nil {
			w.warn(worker.Name, err)
			continue
		}
		process, err := w.start(ctx, cmd, worker.Name, port, env)
		if err != nil {
			w.warn(worker.Name, err)
			continue
		}
		process.served, process.roots = p.served.Dir, p.roots
		w.running[worker.Name] = process
		waiting, stopWaiting := context.WithCancel(ctx)
		process.stopWaiting, process.waited = stopWaiting, make(chan struct{})
		go func() {
			defer close(process.waited)
			w.serveOnceListening(waiting, process, port)
		}()
	}
}

func (w *workerProcesses) start(ctx context.Context, cmd *tracing.Cmd, name string, port int, env map[string]string) (*workerProcess, error) {
	workerEnv := maps.Clone(env)
	workerEnv[hostEnv] = workerHost
	workerEnv[portEnv] = strconv.Itoa(port)
	workerEnv[processenv.WorkerEnvVar] = name
	cmd.Env = applyEnv(cmd.Env, workerEnv)
	cmd.Stdout, cmd.Stderr = w.opts.Stdout, w.opts.Stderr
	child, err := childprocess.Start(ctx, cmd, childprocess.Options{})
	if err != nil {
		return nil, err
	}
	process := &workerProcess{name: name, env: env, child: child, exited: make(chan struct{})}
	go func() {
		err := child.Wait()
		if !process.stopped.Load() && ctx.Err() == nil {
			w.opts.session().Warn(fmt.Sprintf("Worker %q exited (%v); it starts again on the next change", name, err))
		}
		close(process.exited)
	}()
	return process, nil
}

func (w *workerProcesses) serveOnceListening(ctx context.Context, process *workerProcess, port int) {
	address := net.JoinHostPort(workerHost, strconv.Itoa(port))
	deadline := time.Now().Add(workerListensWithin)
	for {
		conn, err := net.DialTimeout("tcp", address, workerDialInterval)
		if err == nil {
			_ = conn.Close()
			break
		}
		if time.Now().After(deadline) {
			w.warn(process.name, fmt.Errorf("it never listened on %s", address))
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-process.exited:
			return
		case <-time.After(workerDialInterval):
		}
	}

	w.mu.Lock()
	defer w.mu.Unlock()
	if ctx.Err() != nil {
		return
	}
	w.served[process.name] = "http://" + address
	if err := w.queue.DeliverTo(ctx, maps.Clone(w.served)); err != nil && ctx.Err() == nil {
		w.warn(process.name, err)
	}
}

func (w *workerProcesses) portOf(name string) (int, error) {
	if port, found := w.ports[name]; found {
		return port, nil
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, fmt.Errorf("find a port to run on: %w", err)
	}
	defer listener.Close()
	port := listener.Addr().(*net.TCPAddr).Port
	w.ports[name] = port
	return port, nil
}

func (w *workerProcesses) stop(ctx context.Context, names []string) {
	if len(names) == 0 {
		return
	}
	stopping := make([]*workerProcess, 0, len(names))
	for _, name := range names {
		process := w.running[name]
		delete(w.running, name)
		if process.stopWaiting != nil {
			process.stopWaiting()
			<-process.waited
		}
		stopping = append(stopping, process)
	}
	w.mu.Lock()
	for _, process := range stopping {
		delete(w.served, process.name)
	}
	served := maps.Clone(w.served)
	w.mu.Unlock()
	if err := w.queue.DeliverTo(context.WithoutCancel(ctx), served); err != nil {
		w.warnAll(err)
	}
	for _, process := range stopping {
		process.stopped.Store(true)
		process.child.Stop()
	}
}

func (w *workerProcesses) close(ctx context.Context) {
	w.stop(ctx, slices.Collect(maps.Keys(w.running)))
	if err := w.run.Remove(); err != nil {
		w.warnAll(err)
	}
}

func (w *workerProcesses) warn(worker string, err error) {
	if !errors.Is(err, context.Canceled) {
		w.opts.session().Warn(fmt.Sprintf("Running worker %q failed: %v", worker, err))
	}
}

func (w *workerProcesses) warnAll(err error) {
	if !errors.Is(err, context.Canceled) {
		w.opts.session().Warn(fmt.Sprintf("Running the workers failed: %v", err))
	}
}
