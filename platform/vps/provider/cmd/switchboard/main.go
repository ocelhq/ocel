package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/ocelhq/ocel/platform/vps/provider/caddyadmin"
	"github.com/ocelhq/ocel/platform/vps/provider/listeners"
	"github.com/ocelhq/ocel/platform/vps/provider/switchboard"
)

const (
	controlEnv     = "OCEL_SWITCHBOARD_CONTROL"
	defaultControl = "/run/ocel-switchboard/control.sock"
	procRoot       = "/proc"
	helperName     = "ocel-switchboard"
)

const (
	exitRefused        = 2
	exitNotServingYet  = 3
	exitSilent         = 4
	exitUnattributable = 5
)

const (
	controlTimeout    = 30 * time.Second
	readHeaderTimeout = 10 * time.Second
	socketMask        = 0o177
	controlDirMode    = 0o700
	lockMode          = 0o600
	lockSuffix        = ".lock"
)

const (
	gateInterval = 250 * time.Millisecond
	gateAttempt  = 2 * time.Second
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	code := run(ctx, procRoot, os.Args[1:], os.Stdout, os.Stderr)
	stop()
	os.Exit(code)
}

func run(ctx context.Context, proc string, argv []string, out, errs io.Writer) int {
	control := os.Getenv(controlEnv)
	if control == "" {
		control = defaultControl
	}
	if len(argv) == 0 {
		return usage(errs)
	}
	rest := argv[1:]
	switch argv[0] {
	case "serve":
		return serve(ctx, control, rest, errs)
	case "load":
		if len(rest) != 1 {
			return usage(errs)
		}
		return load(ctx, control, rest[0], out, errs)
	case "flip":
		return flip(ctx, control, rest, out, errs)
	case "gate":
		return gate(rest, out, errs)
	case "idle":
		if len(rest) == 0 {
			return usage(errs)
		}
		return speak(ctx, control, http.MethodPost, switchboard.IdlePath,
			url.Values{switchboard.TargetField: rest}, out, errs)
	case "upstreams":
		if len(rest) != 0 {
			return usage(errs)
		}
		return speak(ctx, control, http.MethodGet, switchboard.UpstreamsPath, nil, out, errs)
	case "listeners":
		if len(rest) != 0 {
			return usage(errs)
		}
		return netnsListeners(proc, out, errs)
	default:
		return usage(errs)
	}
}

func usage(errs io.Writer) int {
	fmt.Fprintln(errs, "usage: "+helperName+" serve --listen <host:port> --table <path> [--trust <addr|cidr>]... |")
	fmt.Fprintln(errs, "       load <table> |")
	fmt.Fprintln(errs, "       gate --deploy-timeout <seconds> <host:port/path>... |")
	fmt.Fprintln(errs, "       flip [--drain-timeout <seconds> --retire <host:port>...] <table> |")
	fmt.Fprintln(errs, "       idle <host:port>... |")
	fmt.Fprintln(errs, "       upstreams |")
	fmt.Fprintln(errs, "       listeners")
	return exitRefused
}

func refuse(errs io.Writer, err error) int {
	fmt.Fprintf(errs, "%s: %v\n", helperName, err)
	return exitRefused
}

type repeated []string

func (r *repeated) String() string { return strings.Join(*r, " ") }

func (r *repeated) Set(value string) error {
	*r = append(*r, value)
	return nil
}

func serve(ctx context.Context, control string, argv []string, errs io.Writer) int {
	flags := flag.NewFlagSet("serve", flag.ContinueOnError)
	flags.SetOutput(errs)
	listen := flags.String("listen", "", "")
	path := flags.String("table", "", "")
	var trusting repeated
	flags.Var(&trusting, "trust", "")
	if err := flags.Parse(argv); err != nil || *listen == "" || *path == "" || flags.NArg() != 0 {
		return usage(errs)
	}
	trusted, err := prefixes(trusting)
	if err != nil {
		return refuse(errs, err)
	}
	document, err := os.ReadFile(*path)
	if err != nil {
		return refuse(errs, err)
	}
	table, err := switchboard.Read(document)
	if err != nil {
		return refuse(errs, fmt.Errorf("%s: %w", *path, err))
	}
	board := switchboard.New(table, trusted)
	controlling, lock, err := controlListener(control)
	if err != nil {
		return refuse(errs, err)
	}
	defer lock.Close()
	data, err := net.Listen("tcp", *listen)
	if err != nil {
		_ = controlling.Close()
		return refuse(errs, err)
	}
	controller := &http.Server{Handler: board.Control(), ReadHeaderTimeout: readHeaderTimeout}
	failed := make(chan error, 2)
	go func() {
		if err := board.Serve(data); err != nil {
			failed <- err
		}
	}()
	go func() {
		if err := controller.Serve(controlling); !errors.Is(err, http.ErrServerClosed) {
			failed <- err
		}
	}()
	code := 0
	select {
	case <-ctx.Done():
	case err := <-failed:
		code = refuse(errs, err)
	}
	_ = controller.Close()
	grace, stop := context.WithTimeout(context.WithoutCancel(ctx), board.Grace())
	defer stop()
	if err := board.Shutdown(grace); err != nil {
		_ = board.Close()
	}
	return code
}

func prefixes(trusting []string) ([]netip.Prefix, error) {
	trusted := make([]netip.Prefix, 0, len(trusting))
	for _, spelled := range trusting {
		if addr, err := netip.ParseAddr(spelled); err == nil {
			trusted = append(trusted, netip.PrefixFrom(addr.Unmap(), addr.Unmap().BitLen()))
			continue
		}
		prefix, err := netip.ParsePrefix(spelled)
		if err != nil {
			return nil, fmt.Errorf("--trust %q is neither an address nor a prefix", spelled)
		}
		trusted = append(trusted, prefix.Masked())
	}
	return trusted, nil
}

func controlListener(path string) (net.Listener, io.Closer, error) {
	if err := os.MkdirAll(filepath.Dir(path), controlDirMode); err != nil {
		return nil, nil, err
	}
	lock, err := os.OpenFile(path+lockSuffix, os.O_RDWR|os.O_CREATE, lockMode)
	if err != nil {
		return nil, nil, err
	}
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = lock.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, nil, fmt.Errorf("another switchboard holds %s", path)
		}
		return nil, nil, err
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		_ = lock.Close()
		return nil, nil, err
	}
	mask := syscall.Umask(socketMask)
	listener, err := net.Listen("unix", path)
	syscall.Umask(mask)
	if err != nil {
		_ = lock.Close()
		return nil, nil, err
	}
	return listener, lock, nil
}

func load(ctx context.Context, control, path string, out, errs io.Writer) int {
	table, err := filepath.Abs(path)
	if err != nil {
		return refuse(errs, err)
	}
	return speak(ctx, control, http.MethodPost, switchboard.LoadPath,
		url.Values{switchboard.TableField: {table}}, out, errs)
}

func flip(ctx context.Context, control string, argv []string, out, errs io.Writer) int {
	flags := flag.NewFlagSet("flip", flag.ContinueOnError)
	flags.SetOutput(errs)
	var retiring repeated
	flags.Var(&retiring, "retire", "")
	drainTimeout := flags.Int("drain-timeout", 0, "")
	if err := flags.Parse(argv); err != nil || flags.NArg() != 1 || (len(retiring) > 0 && *drainTimeout <= 0) {
		return usage(errs)
	}
	table, err := filepath.Abs(flags.Arg(0))
	if err != nil {
		return refuse(errs, err)
	}
	form := url.Values{switchboard.TableField: {table}, switchboard.RetireField: retiring}
	if len(retiring) > 0 {
		form.Set(switchboard.WindowField, (time.Duration(*drainTimeout) * time.Second).String())
	}
	return speak(ctx, control, http.MethodPost, switchboard.FlipPath, form, out, errs)
}

func speak(ctx context.Context, control, method, path string, form url.Values, out, errs io.Writer) int {
	if path != switchboard.FlipPath {
		var stop context.CancelFunc
		ctx, stop = context.WithTimeout(ctx, controlTimeout)
		defer stop()
	}
	var body io.Reader
	if form != nil {
		body = strings.NewReader(form.Encode())
	}
	request, err := http.NewRequestWithContext(ctx, method, "http://switchboard"+path, body)
	if err != nil {
		return refuse(errs, err)
	}
	if form != nil {
		request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	client := &http.Client{Transport: &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "unix", control)
		},
	}}
	answer, err := client.Do(request)
	if err != nil {
		return refuse(errs, fmt.Errorf("the switchboard answered nothing over %s: %w", control, err))
	}
	defer answer.Body.Close()
	if answer.StatusCode/100 != 2 {
		said, _ := io.ReadAll(answer.Body)
		return refuse(errs, fmt.Errorf("the switchboard refused %s: %s", path, strings.TrimSpace(string(said))))
	}
	if _, err := io.Copy(out, answer.Body); err != nil {
		return refuse(errs, fmt.Errorf("the switchboard cut its answer to %s short: %w", path, err))
	}
	return 0
}

func gate(argv []string, out, errs io.Writer) int {
	flags := flag.NewFlagSet("gate", flag.ContinueOnError)
	flags.SetOutput(errs)
	deployTimeout := flags.Int("deploy-timeout", 0, "")
	if err := flags.Parse(argv); err != nil {
		return usage(errs)
	}
	targets := flags.Args()
	if *deployTimeout <= 0 || len(targets) == 0 {
		return usage(errs)
	}
	for _, target := range targets {
		if !strings.Contains(target, "/") {
			return usage(errs)
		}
	}
	deadline := time.Now().Add(time.Duration(*deployTimeout) * time.Second)
	for _, target := range targets {
		address, path, _ := strings.Cut(target, "/")
		if code := gating(address, "/"+path, max(time.Until(deadline), gateInterval), out, errs); code != 0 {
			fmt.Fprintf(out, "%s %s\n", caddyadmin.Ungated, target)
			return code
		}
	}
	return 0
}

func gating(target, path string, window time.Duration, out, errs io.Writer) int {
	client := &http.Client{Timeout: min(window, gateAttempt)}
	deadline := time.Now().Add(window)
	status := 0
	for {
		answer, err := client.Get("http://" + target + path)
		if err == nil {
			status = answer.StatusCode
			_, _ = io.Copy(io.Discard, answer.Body)
			_ = answer.Body.Close()
			if status/100 == 2 {
				fmt.Fprintln(out, status)
				return 0
			}
		}
		if time.Now().Add(gateInterval).After(deadline) {
			break
		}
		time.Sleep(gateInterval)
	}
	if status == 0 {
		fmt.Fprintf(errs, "%s never answered %s within %s\n", target, path, window)
		return exitSilent
	}
	fmt.Fprintln(out, status)
	fmt.Fprintf(errs, "%s answered %s with status %d, no 2xx within %s\n", target, path, status, window)
	return exitNotServingYet
}

func netnsListeners(proc string, out, errs io.Writer) int {
	var held []listeners.Listener
	tables := 0
	for _, name := range []string{listeners.TCPPath, listeners.TCP6Path} {
		read, err := os.Open(filepath.Join(proc, strings.TrimPrefix(name, "/proc/")))
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return refuse(errs, err)
		}
		found, err := listeners.Parse(read)
		_ = read.Close()
		if err != nil {
			fmt.Fprintf(errs, "%s: %s: %v\n", helperName, name, err)
			return exitUnattributable
		}
		tables++
		held = append(held, found...)
	}
	if tables == 0 {
		fmt.Fprintf(errs, "%s: neither %s nor %s exists\n", helperName, listeners.TCPPath, listeners.TCP6Path)
		return exitUnattributable
	}
	for _, line := range listeners.Lines(held) {
		fmt.Fprintln(out, line)
	}
	return 0
}
