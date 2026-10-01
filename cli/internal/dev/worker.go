package dev

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"net"
	"os/exec"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ocelhq/ocel/cli/internal/childprocess"
	"github.com/ocelhq/ocel/cli/internal/devresources/queue"
	"github.com/ocelhq/ocel/cli/internal/discovery"
	"github.com/ocelhq/ocel/pkg/processenv"
)

const (
	workerHost          = "127.0.0.1"
	workerListensWithin = 5 * time.Minute
	workerDialInterval  = 100 * time.Millisecond
)

type workerProcess struct {
	name    string
	child   *childprocess.Child
	exited  chan struct{}
	stopped atomic.Bool
}

type workerProcesses struct {
	opts  Options
	queue *queue.Backend
	ports map[string]int

	running     []*workerProcess
	stopWaiting context.CancelFunc
	waiting     sync.WaitGroup

	mu     sync.Mutex
	served map[string]string
}

func newWorkerProcesses(opts Options, backend *queue.Backend) *workerProcesses {
	return &workerProcesses{opts: opts, queue: backend, ports: map[string]int{}}
}

func (w *workerProcesses) restart(ctx context.Context, env map[string]string) {
	w.stop(ctx)
	workers := w.queue.Workers()
	if len(workers) == 0 {
		return
	}
	cfg := w.opts.Project
	roots, err := discovery.RootsOf(cfg)
	if err != nil {
		w.warnAll(err)
		return
	}

	waiting, stopWaiting := context.WithCancel(ctx)
	w.stopWaiting = stopWaiting
	for _, worker := range workers {
		root, err := discovery.WorkerRoot(cfg.Dir, roots, worker.Name, worker.Sources)
		if err != nil {
			w.warn(worker.Name, err)
			continue
		}
		cmd, err := discovery.WorkerCommand(ctx, cfg.Dir, roots, root)
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
		w.running = append(w.running, process)
		w.waiting.Go(func() { w.serveOnceListening(waiting, process, port) })
	}
}

func (w *workerProcesses) start(ctx context.Context, cmd *exec.Cmd, name string, port int, env map[string]string) (*workerProcess, error) {
	workerEnv := maps.Clone(env)
	workerEnv[hostEnv] = workerHost
	workerEnv[portEnv] = strconv.Itoa(port)
	workerEnv[processenv.WorkerEnvVar] = name
	cmd.Env = applyEnv(cmd.Env, workerEnv)
	cmd.Stdout, cmd.Stderr = w.opts.Stdout, w.opts.Stderr
	child, err := childprocess.Start(ctx, cmd, nil, false)
	if err != nil {
		return nil, err
	}
	process := &workerProcess{name: name, child: child, exited: make(chan struct{})}
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

func (w *workerProcesses) stop(ctx context.Context) {
	if w.stopWaiting != nil {
		w.stopWaiting()
		w.stopWaiting = nil
	}
	w.waiting.Wait()
	w.mu.Lock()
	w.served = map[string]string{}
	w.mu.Unlock()
	if len(w.running) > 0 {
		if err := w.queue.DeliverTo(context.WithoutCancel(ctx), nil); err != nil {
			w.warnAll(err)
		}
	}
	for _, process := range w.running {
		process.stopped.Store(true)
		process.child.Stop()
	}
	w.running = nil
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
