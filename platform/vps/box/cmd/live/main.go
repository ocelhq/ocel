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

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/platform/vps/box/agent"
	"github.com/ocelhq/ocel/platform/vps/box/queues"
	"github.com/ocelhq/ocel/platform/vps/box/recordstore"
	"github.com/ocelhq/ocel/platform/vps/box/sealkey"
	"github.com/ocelhq/ocel/platform/vps/provider/boxapi"
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
	if len(argv) > 0 && argv[0] == boxapi.EnvSourceSyncCommand {
		return envSourceSync(argv[1:], errs)
	}
	if len(argv) > 0 && argv[0] == boxapi.DockerCommand {
		return dockerCommand(argv[1:], errs)
	}
	if len(argv) > 0 && argv[0] == boxapi.SealCommand {
		return sealCommand(argv[1:], os.Stdin, os.Stdout, errs)
	}
	if len(argv) > 0 && argv[0] == boxapi.PinCommand {
		return pinCommand(argv[1:], os.Stdout, errs)
	}
	if len(argv) > 0 && argv[0] == boxapi.SignCommand {
		return signCommand(argv[1:], os.Stdin, os.Stdout, errs)
	}
	if len(argv) > 0 && argv[0] == boxapi.RecordsCommand {
		return recordsCommand(argv[1:], os.Stdin, os.Stdout, errs)
	}
	flags := flag.NewFlagSet("ocel-live", flag.ContinueOnError)
	flags.SetOutput(errs)
	listen := flags.String("listen", boxapi.SocketPath, "the unix socket to answer on when systemd hands over none")
	tierRoot := flags.String("tier-root", boxapi.TierRoot, "where each tier keeps its seal key")
	stateRoot := flags.String("state-root", boxapi.StateRoot, "where each tier keeps its records")
	proc := flags.String("proc", agent.ProcRoot, "the procfs a caller's cgroup is read from")
	routingTable := flags.String("routing-table", boxapi.RoutingTablePath, "routing table to read this box's hostnames from")
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
	engines := &queues.Engines{
		Records:   recordstore.KeyValues{Root: *stateRoot},
		Tiers:     []environment.Tier{environment.TierProduction, environment.TierPreview},
		Cipher:    sealkey.Cipher{Root: *tierRoot},
		Addresses: inspect,
		Mounts:    inspect,
		Open:      queues.OpenPgmq,
	}
	go engines.Run(ctx)
	server := &agent.Server{
		Proc:    *proc,
		Inspect: inspect,
		Resolve: agent.Store{TierRoot: *tierRoot, StateRoot: *stateRoot, RoutingTable: *routingTable},
		Space:   inspect,
		Queues:  engines,
	}
	if err := server.Serve(ctx, ln); err != nil {
		fmt.Fprintln(errs, "ocel-live: "+err.Error())
		return 1
	}
	return 0
}

func isTier(name string) bool {
	if name == "" {
		return false
	}
	for _, c := range name {
		if (c < 'a' || c > 'z') && (c < '0' || c > '9') && c != '-' {
			return false
		}
	}
	return true
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
		return nil, fmt.Errorf("systemd handed over %d sockets, want 1", fds)
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
