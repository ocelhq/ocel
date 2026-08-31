package main

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"encoding/pem"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/ocelhq/ocel/platform/vps/provider/caddyadmin"
	"github.com/ocelhq/ocel/platform/vps/provider/listeners"
)

const (
	socketEnv     = "OCEL_PROXY_ADMIN"
	defaultSocket = "/run/caddy-admin.sock"
)

const (
	proxyData = "/data"
	procRoot  = "/proc"
)

const (
	exitRefused        = 2
	exitUnhealthy      = 3
	exitSilent         = 4
	exitUnattributable = 5
	exitUnservable     = 6
)

const (
	gateInterval  = 250 * time.Millisecond
	gateAttempt   = 2 * time.Second
	drainInterval = 250 * time.Millisecond
)

const (
	loadPath      = "/load"
	upstreamsPath = "/reverse_proxy/upstreams"
	configPath    = "/config/"
)

const (
	servingAt      = "127.0.0.1:443"
	servingTimeout = 10 * time.Second
)

func main() { os.Exit(run(proxyData, procRoot, os.Args[1:], os.Stdout, os.Stderr)) }

func run(data, proc string, argv []string, out, errs io.Writer) int {
	socket := os.Getenv(socketEnv)
	if socket == "" {
		socket = defaultSocket
	}
	if len(argv) == 0 {
		return usage(errs)
	}
	rest := argv[1:]
	switch argv[0] {
	case "flip":
		if len(rest) != 1 {
			return usage(errs)
		}
		return flip(socket, rest[0], out, errs)
	case "upstreams":
		if len(rest) != 0 {
			return usage(errs)
		}
		return ask(socket, upstreamsPath, out, errs)
	case "config":
		if len(rest) != 1 {
			return usage(errs)
		}
		return ask(socket, configPath+strings.TrimPrefix(rest[0], "/"), out, errs)
	case "leaf":
		if len(rest) != 1 {
			return usage(errs)
		}
		return serving(servingAt, rest[0], out, errs)
	case "listeners":
		if len(rest) != 0 {
			return usage(errs)
		}
		return netnsListeners(proc, out, errs)
	case "forget":
		if len(rest) == 0 {
			return usage(errs)
		}
		return forget(data, rest, out, errs)
	case "deploy":
		return deploy(socket, rest, out, errs)
	default:
		return usage(errs)
	}
}

func usage(errs io.Writer) int {
	fmt.Fprintln(errs, "usage: ocel-proxyctl flip <config> | upstreams | config <path> | leaf <hostname> |")
	fmt.Fprintln(errs, "       listeners |")
	fmt.Fprintln(errs, "       forget <hostname>... |")
	fmt.Fprintln(errs, "       deploy --target <host:port> --health-check-path <path> --deploy-timeout <seconds>")
	fmt.Fprintln(errs, "              --config <path> --drain-timeout <seconds> [--retire <host:port>]")
	return exitRefused
}

func netnsListeners(proc string, out, errs io.Writer) int {
	var held []listeners.Listener
	for _, name := range []string{listeners.TCPPath, listeners.TCP6Path} {
		read, err := os.Open(filepath.Join(proc, strings.TrimPrefix(name, "/proc/")))
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			fmt.Fprintf(errs, "ocel-proxyctl: %v\n", err)
			return exitRefused
		}
		found, err := listeners.Parse(read)
		_ = read.Close()
		if err != nil {
			fmt.Fprintf(errs, "ocel-proxyctl: %s: %v\n", name, err)
			return exitUnattributable
		}
		held = append(held, found...)
	}
	for _, line := range listeners.Lines(held) {
		fmt.Fprintln(out, line)
	}
	return 0
}

func forget(root string, hostnames []string, out, errs io.Writer) int {
	for _, hostname := range hostnames {
		if !subject(hostname) {
			fmt.Fprintf(errs, "ocel-proxyctl: %q is no hostname: forget spends its argument as a directory name under the proxy's certificate store, beside the acme account key that issues for every name on this box\n", hostname)
			return exitRefused
		}
	}
	certificates := filepath.Join(root, "caddy", "certificates")
	issuers, err := os.ReadDir(certificates)
	if errors.Is(err, fs.ErrNotExist) {
		return 0
	}
	if err != nil {
		fmt.Fprintf(errs, "ocel-proxyctl: %v\n", err)
		return exitRefused
	}
	for _, issuer := range issuers {
		if !issuer.IsDir() {
			continue
		}
		for _, hostname := range hostnames {
			held := filepath.Join(certificates, issuer.Name(), hostname)
			if _, err := os.Stat(held); errors.Is(err, fs.ErrNotExist) {
				continue
			}
			if err := os.RemoveAll(held); err != nil {
				fmt.Fprintf(errs, "ocel-proxyctl: %v\n", err)
				return exitRefused
			}
			fmt.Fprintln(out, held)
		}
	}
	return 0
}

const wildcardDirectory = "wildcard_"

func subject(hostname string) bool {
	if hostname == "" || len(hostname) > 253 {
		return false
	}
	for _, label := range strings.Split(hostname, ".") {
		if label == "" || strings.HasPrefix(label, wildcardDirectory) {
			return false
		}
		for _, r := range label {
			letter := r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z'
			if !letter && !(r >= '0' && r <= '9') && r != '-' && r != '_' {
				return false
			}
		}
	}
	return true
}

func serving(at, hostname string, out, errs io.Writer) int {
	held, err := net.DialTimeout("tcp", at, servingTimeout)
	if err != nil {
		fmt.Fprintf(errs, "ocel-proxyctl: nothing answered %s from inside the proxy: %v\n", at, err)
		return exitRefused
	}
	spoken := tls.Client(held, &tls.Config{ServerName: hostname, InsecureSkipVerify: true})
	defer spoken.Close()
	if err := spoken.SetDeadline(time.Now().Add(servingTimeout)); err != nil {
		fmt.Fprintf(errs, "ocel-proxyctl: %v\n", err)
		return exitRefused
	}
	if err := spoken.Handshake(); err != nil {
		if !declined(err) {
			fmt.Fprintf(errs, "ocel-proxyctl: the handshake for %s never finished: %v\n", hostname, err)
			return exitUnservable
		}
		fmt.Fprintf(errs, "ocel-proxyctl: the proxy served no certificate for %s: %v\n", hostname, err)
		return exitUnhealthy
	}
	chain := spoken.ConnectionState().PeerCertificates
	if len(chain) == 0 {
		fmt.Fprintf(errs, "ocel-proxyctl: the proxy completed a handshake for %s and presented no certificate\n", hostname)
		return exitUnservable
	}
	if err := pem.Encode(out, &pem.Block{Type: "CERTIFICATE", Bytes: chain[0].Raw}); err != nil {
		fmt.Fprintf(errs, "ocel-proxyctl: %v\n", err)
		return exitRefused
	}
	return 0
}

func declined(err error) bool {
	var refused *net.OpError
	return errors.As(err, &refused) && refused.Op == "remote error"
}

func deploy(socket string, argv []string, out, errs io.Writer) int {
	flags := flag.NewFlagSet("deploy", flag.ContinueOnError)
	flags.SetOutput(errs)
	target := flags.String("target", "", "")
	path := flags.String("health-check-path", "", "")
	config := flags.String("config", "", "")
	retire := flags.String("retire", "", "")
	deployTimeout := flags.Int("deploy-timeout", 0, "")
	drainTimeout := flags.Int("drain-timeout", 0, "")
	if err := flags.Parse(argv); err != nil {
		return usage(errs)
	}
	if *target == "" || *path == "" || *config == "" || *deployTimeout <= 0 || *drainTimeout <= 0 {
		return usage(errs)
	}

	if code := gating(*target, *path, time.Duration(*deployTimeout)*time.Second, out, errs); code != 0 {
		return code
	}
	if code := flip(socket, *config, out, errs); code != 0 {
		return code
	}
	if *retire == "" {
		return 0
	}
	return draining(socket, *retire, time.Duration(*drainTimeout)*time.Second, out, errs)
}

func gating(target, path string, window time.Duration, out, errs io.Writer) int {
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	attempt := min(window, gateAttempt)
	client := &http.Client{Timeout: attempt}
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
	fmt.Fprintf(errs, "%s answered %s with status %d and never with a 2xx within %s\n", target, path, status, window)
	return exitUnhealthy
}

type upstream struct {
	Address     string `json:"address"`
	NumRequests int    `json:"num_requests"`
}

func draining(socket, address string, window time.Duration, out, errs io.Writer) int {
	deadline := time.Now().Add(window)
	inFlight := 0
	for {
		var read strings.Builder
		if code := ask(socket, upstreamsPath, &read, errs); code != 0 {
			return code
		}
		var pool []upstream
		if err := json.Unmarshal([]byte(read.String()), &pool); err != nil {
			fmt.Fprintf(errs, "ocel-proxyctl: the proxy's upstreams read as %q rather than the pool a drain is counted from: %v\n",
				strings.TrimSpace(read.String()), err)
			return exitUnattributable
		}
		held := -1
		for _, up := range pool {
			if up.Address == address {
				held = up.NumRequests
			}
		}
		if held < 0 {
			fmt.Fprintf(errs, "ocel-proxyctl: %s carries no upstream %s, so nothing here can say whether it is still serving; "+
				"the retired upstream is declared on the drain server precisely so its count stays readable, and a proxy that drops it from the pool before it is free has changed when an upstream leaves\n",
				upstreamsPath, address)
			return exitUnattributable
		}
		if held == 0 {
			return 0
		}
		inFlight = held
		if time.Now().Add(drainInterval).After(deadline) {
			break
		}
		time.Sleep(drainInterval)
	}
	fmt.Fprintf(out, "%s %s %d\n", caddyadmin.DrainExpired, address, inFlight)
	return 0
}

func flip(socket, path string, out, errs io.Writer) int {
	document, err := os.ReadFile(path)
	if err != nil {
		fmt.Fprintf(errs, "ocel-proxyctl: %s is no config this host can read: %v\n", path, err)
		return exitRefused
	}
	if err := caddyadmin.Keeps(document, socket); err != nil {
		fmt.Fprintf(errs, "ocel-proxyctl: %s %v\n", path, err)
		return exitRefused
	}
	return speak(socket, http.MethodPost, loadPath, document, out, errs)
}

func ask(socket, path string, out, errs io.Writer) int {
	return speak(socket, http.MethodGet, path, nil, out, errs)
}

func speak(socket, method, path string, body []byte, out, errs io.Writer) int {
	request, err := http.NewRequest(method, "http://localhost"+path, bytes.NewReader(body))
	if err != nil {
		fmt.Fprintf(errs, "ocel-proxyctl: %v\n", err)
		return exitRefused
	}
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	client := &http.Client{Transport: &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "unix", socket)
		},
	}}
	answer, err := client.Do(request)
	if err != nil {
		fmt.Fprintf(errs, "ocel-proxyctl: the proxy answered nothing over %s: %v\n", socket, err)
		return exitRefused
	}
	defer answer.Body.Close()
	said, err := io.ReadAll(answer.Body)
	if err != nil {
		fmt.Fprintf(errs, "ocel-proxyctl: the proxy cut its answer to %s short: %v\n", path, err)
		return exitRefused
	}
	if answer.StatusCode/100 != 2 {
		fmt.Fprintf(errs, "ocel-proxyctl: the proxy answered %s with %s: %s\n", path, answer.Status, strings.TrimSpace(string(said)))
		return exitRefused
	}
	if len(said) > 0 {
		_, _ = out.Write(said)
		if said[len(said)-1] != '\n' {
			fmt.Fprintln(out)
		}
	}
	return 0
}
