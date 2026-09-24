package main

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
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
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"time"

	edge "github.com/ocelhq/ocel/platform/edge/contract"
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
	exitNotServingYet  = 3
	exitSilent         = 4
	exitUnattributable = 5
	exitUnservable     = 6
)

const adminTimeout = 30 * time.Second

const caddyBinary = "caddy"

var becomingCaddy = syscall.Exec

const (
	gateInterval  = 250 * time.Millisecond
	gateAttempt   = 2 * time.Second
	drainInterval = 250 * time.Millisecond
)

const (
	loadPath      = "/load"
	upstreamsPath = "/reverse_proxy/upstreams"
	configPath    = "/config/"
	authorityPath = "/pki/ca/local"
)

const (
	servingAt      = "127.0.0.1:443"
	servingTimeout = 10 * time.Second
)

func main() { os.Exit(run(proxyData, procRoot, liveRoot, os.Args[1:], os.Stdout, os.Stderr)) }

func run(data, proc, live string, argv []string, out, errs io.Writer) int {
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
		return flipping(socket, live, rest, out, errs)
	case "gate":
		return gate(rest, out, errs)
	case "serve":
		if len(rest) != 1 {
			return usage(errs)
		}
		return serve(live, rest[0], errs)
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
	case "probe":
		if len(rest) != 1 {
			return usage(errs)
		}
		return probe(socket, servingAt, rest[0], out, errs)
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
	default:
		return usage(errs)
	}
}

func usage(errs io.Writer) int {
	fmt.Fprintln(errs, "usage: ocel-proxyctl serve <config> | upstreams | config <path> | leaf <hostname> |")
	fmt.Fprintln(errs, "       probe <hostname> |")
	fmt.Fprintln(errs, "       listeners |")
	fmt.Fprintln(errs, "       forget <hostname>... |")
	fmt.Fprintln(errs, "       gate --deploy-timeout <seconds> <host:port/path>... |")
	fmt.Fprintln(errs, "       flip [--drain-timeout <seconds> --retire <host:port>...] <config>")
	return exitRefused
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
			fmt.Fprintf(errs, "ocel-proxyctl: %v\n", err)
			return exitRefused
		}
		found, err := listeners.Parse(read)
		_ = read.Close()
		if err != nil {
			fmt.Fprintf(errs, "ocel-proxyctl: %s: %v\n", name, err)
			return exitUnattributable
		}
		tables++
		held = append(held, found...)
	}
	if tables == 0 {
		fmt.Fprintf(errs, "ocel-proxyctl: neither %s nor %s exists\n",
			listeners.TCPPath, listeners.TCP6Path)
		return exitUnattributable
	}
	for _, line := range listeners.Lines(held) {
		fmt.Fprintln(out, line)
	}
	return 0
}

func forget(root string, hostnames []string, out, errs io.Writer) int {
	for _, hostname := range hostnames {
		if !subject(hostname) {
			fmt.Fprintf(errs, "ocel-proxyctl: %q is not a hostname\n", hostname)
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
			if !letter && (r < '0' || r > '9') && r != '-' && r != '_' {
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
			fmt.Fprintf(errs, "ocel-proxyctl: tls handshake for %s failed: %v\n", hostname, err)
			return exitUnservable
		}
		fmt.Fprintf(errs, "ocel-proxyctl: the proxy served no certificate for %s: %v\n", hostname, err)
		return exitNotServingYet
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

func probe(socket, at, hostname string, out, errs io.Writer) int {
	status, said, err := exchange(socket, http.MethodGet, authorityPath, nil)
	switch {
	case err != nil:
		fmt.Fprintf(errs, "ocel-proxyctl: %v\n", err)
		return exitRefused
	case status == http.StatusNotFound:
		fmt.Fprintf(errs, "ocel-proxyctl: the proxy holds no local authority yet, so nothing it serves for %s can be verified: %s\n", hostname, strings.TrimSpace(string(said)))
		return exitNotServingYet
	case status/100 != 2:
		fmt.Fprintf(errs, "ocel-proxyctl: the proxy answered %s with %d %s: %s\n", authorityPath, status, http.StatusText(status), strings.TrimSpace(string(said)))
		return exitRefused
	}
	var authority struct {
		Root string `json:"root_certificate"`
	}
	if err := json.Unmarshal(said, &authority); err != nil {
		fmt.Fprintf(errs, "ocel-proxyctl: the proxy described its local authority as something other than json: %v\n", err)
		return exitRefused
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM([]byte(authority.Root)) {
		fmt.Fprintln(errs, "ocel-proxyctl: the proxy's local authority carries no root certificate")
		return exitRefused
	}
	client := &http.Client{
		Timeout: servingTimeout,
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{ServerName: hostname, RootCAs: roots},
			DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
				return (&net.Dialer{}).DialContext(ctx, network, at)
			},
		},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	answer, err := client.Get("https://" + hostname + edge.LivenessProbePath)
	if err != nil {
		fmt.Fprintf(errs, "ocel-proxyctl: %s answered nothing from inside the proxy: %v\n", hostname, err)
		return exitNotServingYet
	}
	defer answer.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(answer.Body, 1<<12))
	fmt.Fprintln(out, strings.TrimSpace(answer.Header.Get(edge.HeaderEdge)))
	return 0
}

func declined(err error) bool {
	var refused *net.OpError
	return errors.As(err, &refused) && refused.Op == "remote error"
}

type addresses []string

func (a *addresses) String() string { return strings.Join(*a, " ") }

func (a *addresses) Set(address string) error {
	*a = append(*a, address)
	return nil
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

func flipping(socket, live string, argv []string, out, errs io.Writer) int {
	flags := flag.NewFlagSet("flip", flag.ContinueOnError)
	flags.SetOutput(errs)
	var retiring addresses
	flags.Var(&retiring, "retire", "")
	drainTimeout := flags.Int("drain-timeout", 0, "")
	if err := flags.Parse(argv); err != nil {
		return usage(errs)
	}
	if flags.NArg() != 1 || (len(retiring) > 0 && *drainTimeout <= 0) {
		return usage(errs)
	}
	retired := make([]string, 0, len(retiring))
	for _, address := range retiring {
		keyed, err := dialled(address)
		if err != nil {
			fmt.Fprintf(errs, "ocel-proxyctl: --retire %v\n", err)
			return exitRefused
		}
		retired = append(retired, keyed)
	}
	if code := flip(socket, live, flags.Arg(0), out, errs); code != 0 || len(retired) == 0 {
		return code
	}
	return draining(socket, retired, time.Duration(*drainTimeout)*time.Second, out, errs)
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
	fmt.Fprintf(errs, "%s answered %s with status %d, no 2xx within %s\n", target, path, status, window)
	return exitNotServingYet
}

type upstream struct {
	Address     string `json:"address"`
	NumRequests int    `json:"num_requests"`
}

func draining(socket string, retiring []string, window time.Duration, out, errs io.Writer) int {
	deadline := time.Now().Add(window)
	inFlight := map[string]int{}
	pending := slices.Clone(retiring)
	for {
		var read strings.Builder
		if code := ask(socket, upstreamsPath, &read, errs); code != 0 {
			return code
		}
		var pool []upstream
		if err := json.Unmarshal([]byte(read.String()), &pool); err != nil {
			fmt.Fprintf(errs, "ocel-proxyctl: cannot parse the proxy's upstreams %q: %v\n",
				strings.TrimSpace(read.String()), err)
			return exitUnattributable
		}
		held := map[string]int{}
		for _, up := range pool {
			held[up.Address] += up.NumRequests
		}
		pending = slices.DeleteFunc(pending, func(address string) bool {
			if held[address] > 0 {
				inFlight[address] = held[address]
				return false
			}
			fmt.Fprintf(out, "%s %s\n", caddyadmin.Drained, address)
			return true
		})
		if len(pending) == 0 {
			return 0
		}
		if time.Now().Add(drainInterval).After(deadline) {
			break
		}
		time.Sleep(drainInterval)
	}
	for _, address := range pending {
		fmt.Fprintf(out, "%s %s %d\n", caddyadmin.DrainExpired, address, inFlight[address])
	}
	return 0
}

func flip(socket, live, path string, out, errs io.Writer) int {
	probed, code := flipped(socket, live, path, out, errs)
	if code == 0 {
		time.Sleep(probed)
	}
	return code
}

func flipped(socket, live, path string, out, errs io.Writer) (time.Duration, int) {
	release, err := holding(live)
	if err != nil {
		fmt.Fprintf(errs, "ocel-proxyctl: %v\n", err)
		return 0, exitRefused
	}
	defer release()
	shape, read := shapedAt(live, path, errs)
	if !read {
		return 0, exitRefused
	}
	if err := caddyadmin.Keeps(shape.config, socket); err != nil {
		fmt.Fprintf(errs, "ocel-proxyctl: %s %v\n", path, err)
		return 0, exitRefused
	}
	if err := shape.introduced(); err != nil {
		fmt.Fprintf(errs, "ocel-proxyctl: %v\n", err)
		return 0, exitRefused
	}
	if code := speak(socket, http.MethodPost, loadPath, shape.config, out, errs); code != 0 {
		return 0, code
	}
	probed, err := shape.moved()
	if err := errors.Join(err, shape.pruned()); err != nil {
		fmt.Fprintf(errs, "ocel-proxyctl: %v\n", err)
		return 0, exitRefused
	}
	return probed, 0
}

func shapedAt(live, path string, errs io.Writer) (shaped, bool) {
	document, err := os.ReadFile(path)
	if err != nil {
		fmt.Fprintf(errs, "ocel-proxyctl: read %s: %v\n", path, err)
		return shaped{}, false
	}
	shape, err := shaping(live, document)
	if err != nil {
		fmt.Fprintf(errs, "ocel-proxyctl: %s %v\n", path, err)
		return shaped{}, false
	}
	return shape, true
}

func serve(live, path string, errs io.Writer) int {
	shape, read := shapedAt(live, path, errs)
	if !read {
		return exitRefused
	}
	started := filepath.Join(live, liveConfig)
	_, moved := shape.moved()
	if err := errors.Join(moved, shape.pruned(), os.WriteFile(started, shape.config, 0o600)); err != nil {
		fmt.Fprintf(errs, "ocel-proxyctl: %v\n", err)
		return exitRefused
	}
	caddy, err := exec.LookPath(caddyBinary)
	if err != nil {
		fmt.Fprintf(errs, "ocel-proxyctl: %v\n", err)
		return exitRefused
	}
	if err := becomingCaddy(caddy, []string{caddyBinary, "run", "--config", started}, os.Environ()); err != nil {
		fmt.Fprintf(errs, "ocel-proxyctl: exec %s: %v\n", caddy, err)
		return exitRefused
	}
	return 0
}

func ask(socket, path string, out, errs io.Writer) int {
	return speak(socket, http.MethodGet, path, nil, out, errs)
}

func speak(socket, method, path string, body []byte, out, errs io.Writer) int {
	status, said, err := exchange(socket, method, path, body)
	if err != nil {
		fmt.Fprintf(errs, "ocel-proxyctl: %v\n", err)
		return exitRefused
	}
	if status/100 != 2 {
		fmt.Fprintf(errs, "ocel-proxyctl: the proxy answered %s with %d %s: %s\n", path, status, http.StatusText(status), strings.TrimSpace(string(said)))
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

func exchange(socket, method, path string, body []byte) (int, []byte, error) {
	request, err := http.NewRequest(method, "http://localhost"+path, bytes.NewReader(body))
	if err != nil {
		return 0, nil, err
	}
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	client := &http.Client{Timeout: adminTimeout, Transport: &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "unix", socket)
		},
	}}
	answer, err := client.Do(request)
	if err != nil {
		return 0, nil, fmt.Errorf("the proxy answered nothing over %s: %w", socket, err)
	}
	defer answer.Body.Close()
	said, err := io.ReadAll(answer.Body)
	if err != nil {
		return 0, nil, fmt.Errorf("the proxy cut its answer to %s short: %w", path, err)
	}
	return answer.StatusCode, said, nil
}
