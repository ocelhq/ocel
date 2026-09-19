package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"syscall"

	"github.com/ocelhq/ocel/platform/vps/provider/live"
	"github.com/ocelhq/ocel/platform/vps/runtime/agent"
)

const (
	listenPIDEnv = "LISTEN_PID"
	listenFDsEnv = "LISTEN_FDS"
	activatedFD  = 3
	socketMode   = 0o666
	socketDir    = 0o755
)

func main() {
	os.Exit(run(os.Args[1:], os.Stderr))
}

func run(argv []string, errs *os.File) int {
	flags := flag.NewFlagSet("ocel-live", flag.ContinueOnError)
	flags.SetOutput(errs)
	listen := flags.String("listen", live.SocketPath, "the unix socket to answer on when systemd hands over none")
	classRoot := flags.String("class-root", live.ClassRoot, "where each class keeps its seal key")
	stateRoot := flags.String("state-root", live.StateRoot, "where each class keeps its records")
	proc := flags.String("proc", agent.ProcRoot, "the procfs a caller's cgroup is read from")
	if err := flags.Parse(argv); err != nil {
		return 2
	}
	ln, err := listener(*listen)
	if err != nil {
		fmt.Fprintln(errs, "ocel-live: "+err.Error())
		return 1
	}
	defer ln.Close()
	inspect, err := agent.NewDocker()
	if err != nil {
		fmt.Fprintln(errs, "ocel-live: "+err.Error())
		return 1
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()
	server := &agent.Server{
		Proc:    *proc,
		Inspect: inspect,
		Resolve: agent.Store{ClassRoot: *classRoot, StateRoot: *stateRoot},
		Space:   inspect,
	}
	if err := server.Serve(ctx, ln); err != nil {
		fmt.Fprintln(errs, "ocel-live: "+err.Error())
		return 1
	}
	return 0
}

func listener(path string) (net.Listener, error) {
	if handed, err := activated(); handed != nil || err != nil {
		return handed, err
	}
	if err := os.MkdirAll(filepath.Dir(path), socketDir); err != nil {
		return nil, err
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	ln, err := net.Listen("unix", path)
	if err != nil {
		return nil, err
	}
	if err := os.Chmod(path, socketMode); err != nil {
		ln.Close()
		return nil, err
	}
	return ln, nil
}

func activated() (net.Listener, error) {
	pid, err := strconv.Atoi(os.Getenv(listenPIDEnv))
	if err != nil || pid != os.Getpid() {
		return nil, nil
	}
	fds, err := strconv.Atoi(os.Getenv(listenFDsEnv))
	if err != nil || fds < 1 {
		return nil, nil
	}
	if fds != 1 {
		return nil, fmt.Errorf("systemd handed over %d sockets, and this agent answers on one", fds)
	}
	syscall.CloseOnExec(activatedFD)
	file := os.NewFile(uintptr(activatedFD), "ocel-live.socket")
	defer file.Close()
	ln, err := net.FileListener(file)
	if err != nil {
		return nil, fmt.Errorf("take the socket systemd handed over: %w", err)
	}
	return ln, nil
}
