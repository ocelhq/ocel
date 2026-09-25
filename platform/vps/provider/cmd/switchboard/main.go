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
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/ocelhq/ocel/platform/vps/provider/switchboard"
)

const controlEnv = "OCEL_SWITCHBOARD_CONTROL"

const (
	exitRefused        = 2
	exitNotServingYet  = 3
	exitSilent         = 4
	exitUnattributable = 5
)

const (
	controlTimeout = 30 * time.Second
	socketMask     = 0o177
	controlDirMode = 0o700
	lockMode       = 0o600
	lockSuffix     = ".lock"
)

const (
	gateInterval = 250 * time.Millisecond
	gateAttempt  = 2 * time.Second
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	code := run(ctx, os.Args[1:], os.Stdout, os.Stderr)
	stop()
	os.Exit(code)
}

func run(ctx context.Context, argv []string, out, errs io.Writer) int {
	control := os.Getenv(controlEnv)
	if control == "" {
		control = switchboard.ControlSocket
	}
	if len(argv) == 0 {
		return usage(errs)
	}
	rest := argv[1:]
	speaking := controlClient{socket: control, out: out, errs: errs}
	switch argv[0] {
	case "serve":
		return serve(ctx, control, rest, errs)
	case "load":
		if len(rest) != 1 {
			return usage(errs)
		}
		return speaking.load(ctx, rest[0])
	case "flip":
		return speaking.flip(ctx, rest)
	case "gate":
		return gate(rest, out, errs)
	case "idle":
		if len(rest) == 0 {
			return usage(errs)
		}
		return speaking.speak(ctx, http.MethodPost, switchboard.IdlePath, url.Values{switchboard.TargetField: rest})
	case "upstreams":
		if len(rest) != 0 {
			return usage(errs)
		}
		return speaking.speak(ctx, http.MethodGet, switchboard.UpstreamsPath, nil)
	case "leaf":
		return leaf(rest, out, errs)
	case "probe":
		return probe(rest, out, errs)
	case "inodes":
		return inodes(rest, out, errs)
	case "holds":
		return holds(rest, errs)
	default:
		return usage(errs)
	}
}

func usage(errs io.Writer) int {
	fmt.Fprintln(errs, "usage: "+switchboard.Name+" serve --listen <host:port> --front <socket> --table <path> [--relay <addr|cidr|"+switchboard.OwnNetwork+">]... |")
	fmt.Fprintln(errs, "       load <table> |")
	fmt.Fprintln(errs, "       gate --deploy-timeout <seconds> <host:port/path>... |")
	fmt.Fprintln(errs, "       flip [--drain-timeout <seconds> --retire <host:port>...] <table> |")
	fmt.Fprintln(errs, "       idle <host:port>... |")
	fmt.Fprintln(errs, "       upstreams |")
	fmt.Fprintln(errs, "       leaf [--at <host:port>] <hostname> |")
	fmt.Fprintln(errs, "       probe [--at <host:port>] <hostname> |")
	fmt.Fprintln(errs, "       inodes <path>... |")
	fmt.Fprintln(errs, "       holds <socket> <path>")
	return exitRefused
}

func refuse(errs io.Writer, err error) int {
	fmt.Fprintf(errs, "%s: %v\n", switchboard.Name, err)
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
	fronting := flags.String("front", "", "")
	path := flags.String("table", "", "")
	var relaying repeated
	flags.Var(&relaying, "relay", "")
	if err := flags.Parse(argv); err != nil || *listen == "" || *fronting == "" || *path == "" || flags.NArg() != 0 {
		return usage(errs)
	}
	relayed, err := relayOf(relaying)
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
	board := switchboard.New(table, relayed...)
	controlling, lock, err := controlListener(control)
	if err != nil {
		return refuse(errs, err)
	}
	defer lock.Close()
	front, err := socketListener(*fronting)
	if err != nil {
		_ = controlling.Close()
		return refuse(errs, err)
	}
	data, err := net.Listen("tcp", *listen)
	if err != nil {
		_ = controlling.Close()
		_ = front.Close()
		return refuse(errs, err)
	}
	controller := &http.Server{Handler: board.Control(), ReadHeaderTimeout: switchboard.ReadHeaderTimeout}
	failed := make(chan error, 3)
	go func() {
		if err := board.Serve(data); err != nil {
			failed <- err
		}
	}()
	go func() {
		if err := board.ServeFront(front); err != nil {
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
	listener, err := socketListener(path)
	if err != nil {
		_ = lock.Close()
		return nil, nil, err
	}
	return listener, lock, nil
}

func socketListener(path string) (net.Listener, error) {
	if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}
	mask := syscall.Umask(socketMask)
	listener, err := net.Listen("unix", path)
	syscall.Umask(mask)
	return listener, err
}

type controlClient struct {
	socket    string
	out, errs io.Writer
}

func (c controlClient) load(ctx context.Context, path string) int {
	table, err := filepath.Abs(path)
	if err != nil {
		return refuse(c.errs, err)
	}
	return c.speak(ctx, http.MethodPost, switchboard.LoadPath, url.Values{switchboard.TableField: {table}})
}

func (c controlClient) flip(ctx context.Context, argv []string) int {
	flags := flag.NewFlagSet("flip", flag.ContinueOnError)
	flags.SetOutput(c.errs)
	var retiring repeated
	flags.Var(&retiring, "retire", "")
	drainTimeout := flags.Int("drain-timeout", 0, "")
	if err := flags.Parse(argv); err != nil || flags.NArg() != 1 || (len(retiring) > 0 && *drainTimeout <= 0) {
		return usage(c.errs)
	}
	table, err := filepath.Abs(flags.Arg(0))
	if err != nil {
		return refuse(c.errs, err)
	}
	form := url.Values{switchboard.TableField: {table}, switchboard.RetireField: retiring}
	if len(retiring) > 0 {
		form.Set(switchboard.WindowField, (time.Duration(*drainTimeout) * time.Second).String())
	}
	return c.speak(ctx, http.MethodPost, switchboard.FlipPath, form)
}

func (c controlClient) speak(ctx context.Context, method, path string, form url.Values) int {
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
		return refuse(c.errs, err)
	}
	if form != nil {
		request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	client := &http.Client{Transport: &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "unix", c.socket)
		},
	}}
	answer, err := client.Do(request)
	if err != nil {
		return refuse(c.errs, fmt.Errorf("the switchboard answered nothing over %s: %w", c.socket, err))
	}
	defer answer.Body.Close()
	if answer.StatusCode/100 != 2 {
		said, _ := io.ReadAll(answer.Body)
		return refuse(c.errs, fmt.Errorf("the switchboard refused %s: %s", path, strings.TrimSpace(string(said))))
	}
	if _, err := io.Copy(c.out, answer.Body); err != nil {
		return refuse(c.errs, fmt.Errorf("the switchboard cut its answer to %s short: %w", path, err))
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
			fmt.Fprintf(out, "%s %s\n", switchboard.Ungated, target)
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
