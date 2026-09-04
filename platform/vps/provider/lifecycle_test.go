package vps_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/creack/pty"

	"github.com/ocelhq/ocel/pkg/providerkit"
	kitledger "github.com/ocelhq/ocel/pkg/providerkit/ledger"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
	vps "github.com/ocelhq/ocel/platform/vps/provider"
	boxedge "github.com/ocelhq/ocel/platform/vps/provider/box"
	"github.com/ocelhq/ocel/platform/vps/provider/host"
)

const lifecycleSlug = "ocel-vps-e2e"

const (
	lifecycleHostname    = "ocel-vps-e2e.localhost"
	lifecyclePreviewBase = "preview.ocel-vps-e2e.localhost"
	lifecycleApp         = "web"
	lifecyclePreview     = "staging"
	lifecycleSensitive   = "E2E_LEDGER_URL"
	lifecycleDeployFile  = "ocel.deploy.config.ts"
)

const patience = 8 * time.Minute

type journey struct {
	vm       machine
	bin      string
	project  string
	settings string
	cache    string
	shims    string
	config   string
	store    string
	trust    string
	value    string
}

func lifecycle(t *testing.T) journey {
	t.Helper()
	vm := live(t)

	dir := t.TempDir()
	run := journey{
		vm:       vm,
		project:  filepath.Join(dir, "project"),
		settings: filepath.Join(dir, "config"),
		value:    "e2e-" + strconv.FormatInt(time.Now().UnixNano(), 36),
	}
	if err := os.MkdirAll(run.project, 0o700); err != nil {
		t.Fatal(err)
	}
	run.apart(t)

	cli, deploy := binaries(t)
	run.bin = cli
	linked(t, deploy, filepath.Join(run.installed(), "bin", "deploy"))
	write(t, filepath.Join(run.installed(), "package.json"), run.manifest(t))
	write(t, filepath.Join(run.project, "ocel.config.ts"), run.declaration(t, vm.user))
	write(t, filepath.Join(run.project, lifecycleDeployFile), run.declaration(t, host.DeployUser()))

	return run
}

const lifecycleManifest = `{
  "name": "e2e-app",
  "private": true,
  "type": "module",
  "scripts": { "start": "node server.js" }
}
`

func (j journey) fixture(t *testing.T, release string) {
	t.Helper()
	write(t, filepath.Join(j.project, "app", "release.txt"), release+"\n")
}

const lifecycleServer = `import { createServer } from "node:http";
import { readFileSync } from "node:fs";

const version = readFileSync(
  new URL("./release.txt", import.meta.url),
  "utf8",
).trim();

createServer((request, response) => {
  const asked = new URL(request.url, "http://box");
  response.setHeader("content-type", "text/plain");
  if (asked.pathname === "/env") {
    response.end(process.env[asked.searchParams.get("name")] ?? "");
    return;
  }
  if (asked.pathname === "/hold") {
    setTimeout(
      () => response.end(version),
      Number(asked.searchParams.get("s") ?? 1) * 1000,
    );
    return;
  }
  response.end(version);
}).listen(Number(process.env.PORT) || 3000);
`

func (j journey) declares(t *testing.T, release string) {
	t.Helper()
	write(t, filepath.Join(j.project, "app", "package.json"), lifecycleManifest)
	j.fixture(t, release)
	write(t, filepath.Join(j.project, "app", "server.js"), lifecycleServer)
	write(t, filepath.Join(j.project, "ocel", "vars.ts"), fmt.Sprintf(
		"import { defineEnv } from \"ocel/env\";\n\nexport const env = defineEnv({\n  %s: { class: \"sensitive\" },\n});\n", lifecycleSensitive))
	if err := os.Symlink(filepath.Join(repoRoot(t), "packages", "ocel"), filepath.Join(j.project, "node_modules", "ocel")); err != nil && !os.IsExist(err) {
		t.Fatal(err)
	}
}

func (j *journey) apart(t *testing.T) {
	t.Helper()
	real, err := exec.LookPath("ssh")
	if err != nil {
		t.Fatalf("no ssh on PATH, and every machine the CLI reaches it reaches through ssh: %v", err)
	}
	dir, err := os.MkdirTemp("", "ocel-e2e-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })

	j.store = filepath.Join(dir, "known_hosts")
	j.cache = filepath.Join(dir, "cache")
	j.config = filepath.Join(dir, "ssh_config")
	j.shims = filepath.Join(dir, "bin")

	write(t, j.config, fmt.Sprintf("Host *\n  UserKnownHostsFile %s\n  GlobalKnownHostsFile /dev/null\n", j.store))
	shim := filepath.Join(j.shims, "ssh")
	write(t, shim, fmt.Sprintf("#!/bin/sh\nexec %s -F %s \"$@\"\n", real, j.config))
	if err := os.Chmod(shim, 0o700); err != nil {
		t.Fatal(err)
	}
}

func (j journey) platformPackage() string {
	arch := runtime.GOARCH
	if arch == "amd64" {
		arch = "x64"
	}
	return "@ocel/provider-vps-" + runtime.GOOS + "-" + arch
}

func (j journey) installed() string {
	return filepath.Join(j.project, "node_modules", filepath.FromSlash(j.platformPackage()))
}

func (j journey) manifest(t *testing.T) string {
	t.Helper()
	written, err := json.Marshal(map[string]any{
		"name":    j.platformPackage(),
		"version": "0.0.0",
		"bin":     map[string]any{"ocel-provider-vps-deploy": "bin/deploy"},
	})
	if err != nil {
		t.Fatal(err)
	}
	return string(written) + "\n"
}

func (j journey) declaration(t *testing.T, login string) string {
	t.Helper()
	options, err := json.Marshal(map[string]any{
		"package": "@ocel/provider-vps",
		"options": map[string]any{"ssh": map[string]any{
			"host":         j.vm.addr,
			"user":         login,
			"identityFile": j.vm.key,
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	apps, err := json.Marshal([]map[string]any{{
		"name":    lifecycleApp,
		"path":    "app",
		"compute": "container",
		"health":  map[string]any{"path": "/"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	return fmt.Sprintf("export default {\n  slug: %q,\n  provider: %s,\n  apps: %s,\n  domains: { production: %q, preview: %q },\n};\n",
		lifecycleSlug, options, apps, lifecycleHostname, "*."+lifecyclePreviewBase)
}

func repoRoot(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", "..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	return root
}

var (
	buildOnce sync.Once
	builtCLI  string
	builtShip string
	buildErr  error
)

func binaries(t *testing.T) (string, string) {
	t.Helper()
	buildOnce.Do(func() {
		dir, err := os.MkdirTemp("", "ocel-e2e-bin-")
		if err != nil {
			buildErr = err
			return
		}
		builtCLI, builtShip = filepath.Join(dir, "ocel"), filepath.Join(dir, "deploy")
		buildErr = errors.Join(
			built(filepath.Join(repoRoot(t), "cli"), builtCLI, "./ocel"),
			built(".", builtShip, "./cmd/deploy"))
	})
	if buildErr != nil {
		t.Fatalf("%v\nthe CLI carries an embedded node bundle: `pnpm install --frozen-lockfile && pnpm --filter ocel build && go generate ./...` in cli/ builds it", buildErr)
	}
	return builtCLI, builtShip
}

func built(module, out, pkg string) error {
	made := exec.Command("go", "build", "-C", module, "-o", out, pkg)
	rendered, err := made.CombinedOutput()
	if err != nil {
		return fmt.Errorf("go build -C %s -o %s %s: %w\n%s", module, out, pkg, err, rendered)
	}
	return nil
}

func linked(t *testing.T, from, to string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(to), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(from, to); err != nil && !os.IsExist(err) {
		if err := os.Symlink(from, to); err != nil && !os.IsExist(err) {
			t.Fatal(err)
		}
	}
}

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func (j journey) trusts(t *testing.T) {
	t.Helper()
	scanned, err := os.ReadFile(j.vm.known)
	if err != nil {
		t.Fatal(err)
	}
	write(t, j.store, string(scanned))
}

func (j journey) forgets(t *testing.T) {
	t.Helper()
	if err := os.Remove(j.store); err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
}

func (j journey) env() []string {
	var kept []string
	for _, entry := range os.Environ() {
		switch name, _, _ := strings.Cut(entry, "="); name {
		case "PATH", "XDG_CONFIG_HOME", "XDG_CACHE_HOME", "SSH_AUTH_SOCK", "OCEL_CONFIG", "OCEL_ACCESS_TOKEN":
			continue
		}
		kept = append(kept, entry)
	}
	kept = append(kept,
		"PATH="+j.shims+string(os.PathListSeparator)+os.Getenv("PATH"),
		"XDG_CONFIG_HOME="+j.settings,
		"XDG_CACHE_HOME="+j.cache,
		"OCEL_NO_BROWSER=1",
	)
	if j.trust != "" {
		kept = append(kept, "SSL_CERT_FILE="+j.trust)
	}
	return kept
}

func (j journey) deploying(t *testing.T, args ...string) string {
	t.Helper()
	return j.must(t, append([]string{"-c", lifecycleDeployFile}, args...)...)
}

func (j journey) refused(t *testing.T, args ...string) string {
	t.Helper()
	rendered, err := j.run(t, append([]string{"-c", lifecycleDeployFile}, args...)...)
	if err == nil {
		t.Fatalf("ocel %s succeeded, and the whole point of this step is that it is refused:\n%s", strings.Join(args, " "), rendered)
	}
	return rendered
}

type reply struct {
	status  int
	headers string
	body    string
}

func (j journey) over(t *testing.T, hostname, path string) reply {
	t.Helper()
	request, err := http.NewRequest(http.MethodGet, "http://"+j.vm.addr+path, nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Host = hostname
	client := &http.Client{
		Timeout:       30 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	said, err := client.Do(request)
	if err != nil {
		t.Fatalf("GET http://%s%s with Host: %s: %v", j.vm.addr, path, hostname, err)
	}
	defer func() { _ = said.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(said.Body, 1<<16))
	if err != nil {
		t.Fatal(err)
	}
	var headers strings.Builder
	for name, values := range said.Header {
		for _, value := range values {
			headers.WriteString(strings.ToLower(name) + ": " + strings.ToLower(value) + "\n")
		}
	}
	return reply{status: said.StatusCode, headers: headers.String(), body: string(body)}
}

func (a reply) fromTheBox() bool {
	return strings.Contains(a.headers, strings.ToLower(edge.HeaderEdge)+": "+host.EdgeName)
}

type flight struct {
	status int
	body   string
	err    error
}

func (j journey) held(hostname, path string, patience time.Duration) flight {
	request, err := http.NewRequest(http.MethodGet, "http://"+j.vm.addr+path, nil)
	if err != nil {
		return flight{err: err}
	}
	request.Host = hostname
	client := &http.Client{
		Timeout:       patience,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	said, err := client.Do(request)
	if err != nil {
		return flight{err: err}
	}
	defer func() { _ = said.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(said.Body, 1<<16))
	return flight{status: said.StatusCode, body: strings.TrimSpace(string(body)), err: err}
}

type upstream struct {
	Address     string `json:"address"`
	NumRequests int    `json:"num_requests"`
}

func upstreams(read string) []upstream {
	at := strings.Index(read, "[")
	if at < 0 {
		return nil
	}
	var held []upstream
	if err := json.Unmarshal([]byte(read[at:]), &held); err != nil {
		return nil
	}
	return held
}

type crossing struct {
	answers  []flight
	samples  []string
	together bool
	inflight bool
	drained  int
	stopped  int
}

func (j journey) underLoad(t *testing.T, hostname, retiring string, flipping func()) crossing {
	t.Helper()

	const beat = 500 * time.Millisecond
	address := retiring + ":" + host.AppPort
	stop := make(chan struct{})
	var mu sync.Mutex
	watched := crossing{drained: -1, stopped: -1}
	var group sync.WaitGroup

	const holders = 4
	group.Add(holders + 1)
	for range holders {
		go func() {
			defer group.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				answered := j.held(hostname, "/hold?s=6", 90*time.Second)
				mu.Lock()
				watched.answers = append(watched.answers, answered)
				mu.Unlock()
			}
		}()
	}
	go func() {
		defer group.Done()
		for at := 0; ; at++ {
			select {
			case <-stop:
				return
			case <-time.After(beat):
			}
			read, _ := j.vm.attempt(j.vm.user, "sudo docker exec "+host.ProxyContainer+" "+host.ProxyHelperMount+
				" upstreams 2>&1; echo '@@'; sudo docker inspect -f '{{.State.Status}}' "+quote(retiring)+" 2>/dev/null || true")
			pool, state, _ := strings.Cut(read, "@@")
			mu.Lock()
			watched.samples = append(watched.samples, strings.TrimSpace(read))
			held := upstreams(pool)
			for _, one := range held {
				if one.Address != address {
					continue
				}
				watched.together = watched.together || len(held) > 1
				watched.inflight = watched.inflight || one.NumRequests > 0
				if one.NumRequests == 0 && watched.drained < 0 {
					watched.drained = at
				}
			}
			if watched.stopped < 0 && strings.TrimSpace(state) == "exited" {
				watched.stopped = at
			}
			mu.Unlock()
		}
	}()

	flipping()
	settled(&mu, &watched, 20*time.Second)
	close(stop)
	group.Wait()

	mu.Lock()
	defer mu.Unlock()
	return watched
}

func settled(mu *sync.Mutex, watched *crossing, within time.Duration) {
	deadline := time.Now().Add(within)
	for {
		mu.Lock()
		read := watched.drained >= 0 && watched.stopped >= 0
		mu.Unlock()
		if read || time.Now().After(deadline) {
			return
		}
		time.Sleep(200 * time.Millisecond)
	}
}

func (j journey) serving(t *testing.T, hostname, want string) {
	t.Helper()
	deadline := time.Now().Add(90 * time.Second)
	var last reply
	for {
		if last = j.over(t, hostname, "/"); last.status == http.StatusOK && strings.TrimSpace(last.body) == want {
			if !last.fromTheBox() {
				t.Errorf("%s answered %q with no %s: %s header:\n%s", hostname, want, edge.HeaderEdge, host.EdgeName, last.headers)
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s answered %d %q, want 200 %q:\n%s", hostname, last.status, last.body, want, last.headers)
		}
		time.Sleep(time.Second)
	}
}

func (j journey) resolving(t *testing.T, hostnames ...string) {
	t.Helper()
	const hosts = "/etc/hosts"
	read, err := os.ReadFile(hosts)
	if err != nil {
		t.Fatal(err)
	}
	named := map[string]bool{}
	for _, line := range strings.Split(string(read), "\n") {
		if fields := strings.Fields(line); len(fields) == 2 && fields[0] == j.vm.addr {
			named[fields[1]] = true
		}
	}
	var owed []string
	for _, hostname := range hostnames {
		if !named[hostname] {
			owed = append(owed, j.vm.addr+" "+hostname)
		}
	}
	if len(owed) > 0 {
		if err := writing(hosts, "\n"+strings.Join(owed, "\n")+"\n", os.O_APPEND, "cat >> "+hosts); err != nil {
			t.Fatalf("%s must name %s at %s for this journey's hostnames to reach the box, and this run cannot write it: %v\nAdd the lines and run again:\n%s",
				hosts, j.vm.addr, strings.Join(hostnames, ", "), err, strings.Join(owed, "\n"))
		}
		t.Cleanup(func() { j.forgetHosts(hostnames) })
	}
	for _, hostname := range hostnames {
		found, err := net.LookupHost(hostname)
		if err != nil || !slices.Contains(found, j.vm.addr) {
			t.Fatalf("%s resolves to %v (%v), want %s: every hostname this journey binds is reached through %s", hostname, found, err, j.vm.addr, hosts)
		}
	}
}

func writing(path, written string, mode int, elevated string) error {
	file, direct := os.OpenFile(path, mode|os.O_WRONLY, 0o644)
	if direct == nil {
		if _, err := io.WriteString(file, written); err != nil {
			_ = file.Close()
			return err
		}
		return file.Close()
	}
	through := exec.Command("sudo", "-n", "sh", "-c", elevated)
	through.Stdin = strings.NewReader(written)
	if said, err := through.CombinedOutput(); err != nil {
		return fmt.Errorf("%w, and sudo could not either: %w: %s", direct, err, said)
	}
	return nil
}

func (j journey) forgetHosts(hostnames []string) {
	read, err := os.ReadFile("/etc/hosts")
	if err != nil {
		return
	}
	var kept []string
	for _, line := range strings.Split(string(read), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 2 && fields[0] == j.vm.addr && slices.Contains(hostnames, fields[1]) {
			continue
		}
		kept = append(kept, line)
	}
	_ = writing("/etc/hosts", strings.Join(kept, "\n"), os.O_TRUNC, "cat > /etc/hosts")
}

func (j *journey) trusting(t *testing.T) {
	t.Helper()
	root := j.vm.ssh(t, "sudo cat "+quote(host.ProxyData+"/caddy/pki/authorities/local/root.crt"))
	if !strings.Contains(root, "BEGIN CERTIFICATE") {
		t.Fatalf("the proxy holds no local issuing root at %s, so no hostname on this box can be settled without asking a CA this machine cannot reach:\n%s", host.ProxyData, root)
	}
	j.trust = filepath.Join(filepath.Dir(j.store), "proxy-root.pem")
	write(t, j.trust, root)
}

func (j journey) run(t *testing.T, args ...string) (string, error) {
	t.Helper()
	ctx, done := context.WithTimeout(context.Background(), patience)
	defer done()

	cmd := exec.CommandContext(ctx, j.bin, args...)
	cmd.Dir = j.project
	cmd.Env = j.env()
	var rendered bytes.Buffer
	cmd.Stdout = &rendered
	cmd.Stderr = &rendered
	err := cmd.Run()
	if ctx.Err() != nil {
		err = fmt.Errorf("ocel %s was still running after %s: %w", strings.Join(args, " "), patience, ctx.Err())
	}
	return plain(rendered.String()), err
}

func (j journey) must(t *testing.T, args ...string) string {
	t.Helper()
	rendered, err := j.run(t, args...)
	if err != nil {
		t.Fatalf("ocel %s = %v\n%s", strings.Join(args, " "), err, rendered)
	}
	return rendered
}

type transcript struct {
	mu   sync.Mutex
	seen strings.Builder
}

func (s *transcript) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.seen.Write(p)
}

func (s *transcript) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.seen.String()
}

func (j journey) onATerminal(t *testing.T, args []string, awaiting, answer string) (string, error) {
	t.Helper()
	cmd := exec.Command(j.bin, args...)
	cmd.Dir = j.project
	cmd.Env = j.env()
	terminal, err := pty.StartWithSize(cmd, &pty.Winsize{Rows: 60, Cols: 200})
	if err != nil {
		t.Fatalf("no pty for %s, and the CLI only asks where a human is: %v", j.bin, err)
	}
	defer terminal.Close()

	var seen transcript
	read := make(chan struct{})
	go func() {
		_, _ = io.Copy(&seen, terminal)
		close(read)
	}()

	if awaiting != "" {
		if !appears(&seen, read, awaiting) {
			_ = cmd.Process.Kill()
			<-read
			t.Fatalf("ocel %s never asked %q on a terminal:\n%s", strings.Join(args, " "), awaiting, plain(seen.String()))
		}
		if _, err := io.WriteString(terminal, answer); err != nil {
			_ = cmd.Process.Kill()
			<-read
			t.Fatalf("answering %q to ocel %s on a terminal: %v\n%s", answer, strings.Join(args, " "), err, plain(seen.String()))
		}
	}

	waited := make(chan error, 1)
	go func() { waited <- cmd.Wait() }()
	giveUp := time.NewTimer(patience)
	defer giveUp.Stop()
	select {
	case err = <-waited:
	case <-giveUp.C:
		_ = cmd.Process.Kill()
		<-waited
		<-read
		t.Fatalf("ocel %s was still running on a terminal after %s:\n%s", strings.Join(args, " "), patience, plain(seen.String()))
	}
	<-read
	return plain(seen.String()), err
}

func appears(seen *transcript, read <-chan struct{}, fragment string) bool {
	beat := time.NewTicker(200 * time.Millisecond)
	defer beat.Stop()
	giveUp := time.NewTimer(3 * time.Minute)
	defer giveUp.Stop()
	for {
		if strings.Contains(plain(seen.String()), fragment) {
			return true
		}
		select {
		case <-read:
			return strings.Contains(plain(seen.String()), fragment)
		case <-giveUp.C:
			return false
		case <-beat.C:
		}
	}
}

var escapes = regexp.MustCompile("\x1b\\[[\x30-\x3f]*[\x20-\x2f]*[\x40-\x7e]|\x1b\\][^\x07\x1b]*(\x07|\x1b\\\\)|\x1b[()][0-9A-B]|\x1b[=>]")

func plain(rendered string) string {
	return strings.ReplaceAll(escapes.ReplaceAllString(rendered, ""), "\r\n", "\n")
}

const (
	lifecycleDecoyContainer = "not-ocels-workload"
	lifecycleDecoyLabel     = "not.ocels.workload"
	lifecycleDecoyImage     = "public.ecr.aws/docker/library/alpine:3.20"
	lifecycleDecoyProxy     = "not-ocels-caddy"
	lifecycleDecoyProxyData = "/var/lib/not-ocels-caddy"
)

type decoy struct {
	workload string
	image    string
	proxy    string
	data     string
}

func (j journey) plants(t *testing.T) decoy {
	t.Helper()
	j.vm.ssh(t, "sudo docker rm -f "+lifecycleDecoyContainer+" >/dev/null 2>&1 || true")
	j.vm.ssh(t, "sudo docker run -d --name "+lifecycleDecoyContainer+" --label "+lifecycleDecoyLabel+"=kept "+
		decoyImage+" sleep 5400")
	t.Cleanup(func() { j.vm.ssh(t, "sudo docker rm -f "+lifecycleDecoyContainer+" >/dev/null 2>&1 || true") })

	j.vm.ssh(t, "sudo docker pull "+quote(lifecycleDecoyImage))
	t.Cleanup(func() { j.vm.ssh(t, "sudo docker rmi "+lifecycleDecoyImage+" >/dev/null 2>&1 || true") })

	j.vm.ssh(t, "sudo install -d -m 0755 "+quote(lifecycleDecoyProxyData))
	j.vm.ssh(t, "printf 'a caddy this machine already ran\n' | sudo install -m 0644 /dev/stdin "+quote(lifecycleDecoyProxyData+"/caddy.json"))
	j.vm.ssh(t, "sudo docker rm -f "+lifecycleDecoyProxy+" >/dev/null 2>&1 || true")
	j.vm.ssh(t, "sudo docker run -d --name "+lifecycleDecoyProxy+" --entrypoint sleep -v "+
		quote(lifecycleDecoyProxyData)+":/data "+quote(host.ProxyImage)+" 3000")
	t.Cleanup(func() {
		j.vm.ssh(t, "sudo docker rm -f "+lifecycleDecoyProxy+" >/dev/null 2>&1 || true")
		j.vm.ssh(t, "sudo rm -rf "+quote(lifecycleDecoyProxyData))
	})

	held := decoy{
		workload: j.vm.inspects(t, "container", lifecycleDecoyContainer, "{{.Id}}"),
		image:    j.vm.holds(t, lifecycleDecoyImage),
		proxy:    j.vm.inspects(t, "container", lifecycleDecoyProxy, "{{.Id}}"),
		data:     j.vm.ssh(t, "sudo sha256sum "+quote(lifecycleDecoyProxyData+"/caddy.json")),
	}
	for what, id := range map[string]string{
		lifecycleDecoyContainer: held.workload,
		lifecycleDecoyImage:     held.image,
		lifecycleDecoyProxy:     held.proxy,
	} {
		if id == "" {
			t.Fatalf("%s is not on this machine before ocel deploys anything, so what ocel leaves of a user's own containers and images cannot be proven here", what)
		}
	}
	for _, standing := range []string{lifecycleDecoyContainer, lifecycleDecoyProxy} {
		if !j.vm.running(t, standing) {
			t.Fatalf("%s is not running before ocel deploys anything, and a container the machine's owner ran themselves is what proves ocel's destroy takes only what ocel put here", standing)
		}
	}
	if ran := j.vm.inspects(t, "container", lifecycleDecoyContainer, "{{.Image}}"); ran == held.image {
		t.Fatalf("%s is the image %s runs, so a container references it and it cannot fail the way an image the user pulled and never ran would", lifecycleDecoyImage, lifecycleDecoyContainer)
	}
	return held
}

func (j journey) gaveBack(t *testing.T, repository string) {
	t.Helper()
	if kept := j.labelled(t, lifecycleDecoyLabel); len(kept) == 0 {
		t.Fatalf("this box lists no container at all under label %s, so the empty listing under %s is the emptiness of the command rather than of the label",
			lifecycleDecoyLabel, host.LabelApp)
	}
	if standing := j.appContainers(t); len(standing) > 0 {
		t.Errorf("containers %v still carry %s", standing, host.LabelApp)
	}
	if kept := j.appImages(t, decoyRepository); len(kept) == 0 {
		t.Fatalf("this box lists no image at all under %s, so the empty listing under %s is the emptiness of the command rather than of the repository", decoyRepository, repository)
	}
	if swept := j.appImages(t, repository); len(swept) > 0 {
		t.Errorf("%v still stand under %s, and a destroy empties the difference its own reference filter names", swept, repository)
	}
}

func localAuthority(logged, hostname string) bool {
	for _, line := range strings.Split(logged, "\n") {
		if strings.Contains(line, "\"identifier\":\""+hostname+"\"") && strings.Contains(line, "\"issuer\":\"local\"") {
			return true
		}
	}
	return false
}

func (j journey) survived(t *testing.T, planted decoy) {
	t.Helper()
	for _, standing := range []string{lifecycleDecoyContainer, lifecycleDecoyProxy} {
		if !j.vm.running(t, standing) {
			t.Errorf("%s stopped somewhere in this journey, and a container the machine's owner started under its own name is not ocel's to stop", standing)
		}
	}
	for what, want := range map[string]string{
		lifecycleDecoyContainer: planted.workload,
		lifecycleDecoyProxy:     planted.proxy,
	} {
		if after := j.vm.inspects(t, "container", what, "{{.Id}}"); after != want {
			t.Errorf("%s reads %q after both bootstrap destroys where it read %q, and a container ocel never ran is not ocel's to take", what, after, want)
		}
	}
	if after := j.vm.inspects(t, "image", lifecycleDecoyImage, "{{.Id}}"); after != planted.image {
		t.Errorf("%s reads %q after both bootstrap destroys where it read %q: ocel prunes no images wholesale, and removes only what the difference under its own reference filter names",
			lifecycleDecoyImage, after, planted.image)
	}
	if after := j.vm.ssh(t, "sudo sha256sum "+quote(lifecycleDecoyProxyData+"/caddy.json")); after != planted.data {
		t.Errorf("%s reads\n%s\nafter both bootstrap destroys where it read\n%s\nand the data directory of a proxy ocel did not install is not ocel's to edit",
			lifecycleDecoyProxyData, after, planted.data)
	}
}

func (j journey) appContainers(t *testing.T) []string {
	t.Helper()
	listed := j.vm.ssh(t, "sudo docker ps -a --filter label="+host.LabelApp+
		" --format '{{.Names}} {{.Label \""+host.LabelRef+"\"}}' | sort")
	return lines(listed)
}

func (j journey) labelled(t *testing.T, label string) []string {
	t.Helper()
	return lines(j.vm.ssh(t, "sudo docker ps -a --filter label="+label+" --format '{{.Names}}' | sort"))
}

func (j journey) appImages(t *testing.T, repository string) []string {
	t.Helper()
	return lines(j.vm.ssh(t, "sudo docker images --filter reference="+quote(repository+":*")+
		" --format '{{.Repository}}:{{.Tag}}' | sort"))
}

func lines(rendered string) []string {
	trimmed := strings.TrimSpace(rendered)
	if trimmed == "" {
		return nil
	}
	return strings.Split(trimmed, "\n")
}

func (j journey) window(t *testing.T, class providerkit.Class) []string {
	t.Helper()
	return lines(j.vm.sshAs(t, host.DeployUser(), "cat "+quote(host.ReleasesDir()+"/"+lifecycleApp+"/"+string(class))))
}

func (j journey) container(t *testing.T) string {
	t.Helper()
	running := lines(j.vm.ssh(t, "sudo docker ps --filter label="+host.LabelApp+"="+lifecycleApp+" --format '{{.Names}}'"))
	if len(running) != 1 {
		t.Fatalf("%d containers carry %s=%s, and this journey turns on there being exactly one: %v", len(running), host.LabelApp, lifecycleApp, running)
	}
	return running[0]
}

func redacted(rendered string, at, width int) string {
	from := strings.LastIndex(rendered[:at], "\n") + 1
	to := at + width
	if end := strings.Index(rendered[to:], "\n"); end >= 0 {
		to += end
	} else {
		to = len(rendered)
	}
	return rendered[from:at] + strings.Repeat("*", width) + rendered[at+width:to]
}

func digestIn(reference string) string {
	at := strings.LastIndex(reference, "sha256")
	if at < 0 || len(reference) < at+7+64 {
		return ""
	}
	return reference[at+7:]
}

func firstLineOf(rendered, fragment string) string {
	at := strings.Index(rendered, fragment)
	if at < 0 {
		return ""
	}
	line := rendered[at:]
	if end := strings.Index(line, "\n"); end >= 0 {
		line = line[:end]
	}
	return line
}

func (j journey) promotionOf(t *testing.T, ref string) string {
	t.Helper()
	held, err := kitledger.New(j.box(t).Records(), providerkit.ClassProduction, lifecycleSlug).
		History(context.Background(), edge.DefaultPointer)
	if err != nil {
		t.Fatalf("read the promotions this box holds for %s: %v", lifecycleSlug, err)
	}
	digest := digestIn(ref)
	if digest == "" {
		t.Fatalf("%s carries no digest, and a promotion is looked up by the one thing a container label and a ledger identity spell the same way", ref)
	}
	for _, entry := range held {
		if digestIn(entry.Builds[lifecycleApp]) == digest {
			return entry.PromotionID
		}
	}
	t.Fatalf("no promotion among %+v carries %s for %s, so there is no name a rollback to it could be asserted against", held, ref, lifecycleApp)
	return ""
}

func (j journey) box(t *testing.T) *vps.Provider {
	t.Helper()
	p := vps.NewProvider(vps.Options{SSH: vps.Target{
		Host: j.vm.addr, User: deployLogin, IdentityFile: j.vm.key, Config: j.vm.config,
	}})
	t.Cleanup(func() { _ = p.Close() })
	return p
}

func (j journey) claims(t *testing.T, hostname string) string {
	t.Helper()
	if err := j.box(t).Host().ClaimHosts(context.Background(), []host.HostClaim{{
		Hostname: hostname, Owner: boxedge.Surface(lifecycleSlug, edge.ClassPreview), Pointer: edge.DefaultPointer,
	}}); err != nil {
		t.Fatalf("claim %s on this box, so a release of the catch-all has a route beside it that it never touches: %v", hostname, err)
	}
	return hostname
}

func (j journey) rival(t *testing.T) journey {
	t.Helper()
	rival := j
	rival.project = filepath.Join(filepath.Dir(j.project), "rival")
	if err := os.MkdirAll(rival.project, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(j.project, "node_modules"), filepath.Join(rival.project, "node_modules")); err != nil && !os.IsExist(err) {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(j.project, "app"), filepath.Join(rival.project, "app")); err != nil && !os.IsExist(err) {
		t.Fatal(err)
	}
	written := strings.Replace(rival.declaration(t, host.DeployUser()), "slug: "+strconv.Quote(lifecycleSlug), "slug: "+strconv.Quote(lifecycleSlug+"-rival"), 1)
	write(t, filepath.Join(rival.project, lifecycleDeployFile), written)
	return rival
}

func TestLifecycleTheWholeJourneyRunsOnTheRealBinaryAndGivesTheMachineBack(t *testing.T) {
	run := lifecycle(t)
	run.trusts(t)
	run.declares(t, "one")
	run.resolving(t, lifecycleHostname, edge.ProbeHostname("*."+lifecyclePreviewBase),
		lifecyclePreview+"."+lifecyclePreviewBase, "unclaimed."+lifecyclePreviewBase)
	run.vm.purges(t)

	class := providerkit.ClassProduction
	fresh := run.must(t, "doctor")
	if !owedABootstrap.MatchString(fresh) {
		t.Fatalf("`ocel doctor` on a machine nothing has written to said:\n%s", fresh)
	}

	applied := run.must(t, "bootstrap", "production", "--yes")
	if !strings.Contains(applied, "Bootstrapped") {
		t.Errorf("`ocel bootstrap production --yes` finished without saying it bootstrapped:\n%s", applied)
	}
	stamped := stampOn(t, run.vm)
	if stamped.State != host.StateComplete {
		t.Errorf("the stamp the CLI left reads state %q, want %q", stamped.State, host.StateComplete)
	}

	standing := run.must(t, "doctor")
	if !strings.Contains(standing, "bootstrapped — schema") {
		t.Fatalf("`ocel doctor` after an apply still calls production unbootstrapped:\n%s", standing)
	}
	if !strings.Contains(standing, "\nStanding\n") {
		t.Fatalf("`ocel doctor` on a bootstrapped box printed no standing section, so there is no output an absence can be read over:\n%s", standing)
	}
	for _, verdict := range []string{
		lifecycleHostname + " resolves to " + run.vm.addr + ", which is this box",
		"something listens on port " + host.RenewalPort,
		"nothing listens on tcp " + host.AdminPort + " inside " + host.ProxyContainer,
	} {
		if !strings.Contains(standing, verdict) {
			t.Errorf("`ocel doctor` never said %q, and this is the only command that runs thirty days after a deploy:\n%s", verdict, standing)
		}
	}
	if strings.Contains(standing, "✗") {
		t.Errorf("`ocel doctor` refused something on a box whose one owed thing is a dns record a human has not written:\n%s", standing)
	}
	if strings.Contains(standing, "\nCertificates\n") {
		t.Errorf("`ocel doctor` printed a certificates section over a box holding no hostname at all, so it read a renewal off something other than a certificate this box serves:\n%s", standing)
	}

	replanned := run.must(t, "bootstrap", "production", "--dry")
	if !strings.Contains(replanned, "No infrastructure changes") {
		t.Errorf("a re-plan over a bootstrapped machine found work to do:\n%s", replanned)
	}
	reapplied := run.must(t, "bootstrap", "production", "--yes")
	if !strings.Contains(reapplied, "No infrastructure changes") {
		t.Errorf("a re-apply over a bootstrapped machine found work the re-plan said was not there:\n%s", reapplied)
	}
	again := stampOn(t, run.vm)
	if again.Seal != stamped.Seal {
		t.Errorf("the re-apply minted seal %+v over %+v, and a key nothing asked to rotate takes every value sealed under the old one", again.Seal, stamped.Seal)
	}
	if !maps.Equal(again.Digests, stamped.Digests) {
		t.Errorf("the re-apply rewrote the host: digests %v, want %v", again.Digests, stamped.Digests)
	}

	bootstrapping := run.must(t, "permissions", "bootstrap")
	if !strings.Contains(bootstrapping, "NOPASSWD: ALL") {
		t.Errorf("`ocel permissions bootstrap` printed no grant to hand an ops team:\n%s", bootstrapping)
	}
	deploying := run.must(t, "permissions", "deploy")
	for _, want := range []string{host.DeployUser(), "root on this machine under another name"} {
		if !strings.Contains(deploying, want) {
			t.Errorf("`ocel permissions deploy` never says %q, and what a user trusts goes unsaid:\n%s", want, deploying)
		}
	}

	planted := run.plants(t)

	run.vm.ssh(t, "sudo usermod -aG docker "+run.vm.user)
	t.Cleanup(func() { run.vm.ssh(t, "sudo gpasswd -d "+run.vm.user+" docker >/dev/null 2>&1 || true") })

	set := run.deploying(t, "env", "set", lifecycleSensitive, run.value)
	if !strings.Contains(set, "Set "+lifecycleSensitive) {
		t.Fatalf("`ocel env set %s` said nothing about setting it:\n%s", lifecycleSensitive, set)
	}

	drawn := run.must(t, "deploy", "--dry")
	if !strings.Contains(drawn, "Proposed changes to production") {
		t.Fatalf("`ocel deploy --dry` drew no plan, so there is no plan an image row can be read out of:\n%s", drawn)
	}
	for _, row := range []string{lifecycleApp + "  image", "promotion " + edge.DefaultPointer, lifecycleApp + "  deployment"} {
		if !strings.Contains(drawn, row) {
			t.Errorf("`ocel deploy --dry` drew no %q row, and every mutation a deploy performs on this box is a plan row sourced from an engine event:\n%s", row, drawn)
		}
	}
	if !strings.Contains(drawn, "+ "+lifecycleApp+"  image") {
		t.Errorf("`ocel deploy --dry` drew %s's image row without the create the engine's own answer decides, so the row is drawn off the plan rather than off what this box holds:\n%s", lifecycleApp, drawn)
	}

	deployed := run.deploying(t, "deploy", "--yes")
	if !strings.Contains(deployed, "Deployed") {
		t.Fatalf("`ocel deploy --yes` finished without deploying:\n%s", deployed)
	}
	if want := "Sending " + lifecycleApp + "'s image to " + run.vm.addr; !strings.Contains(deployed, want) {
		t.Errorf("the deploy never reported %q, and the transfer of an image onto this box is the one mutation nothing else would name:\n%s", want, deployed)
	}
	container := run.container(t)
	if held := run.vm.inspects(t, "container", container, host.LabelSelector(host.LabelApp)); held != lifecycleApp {
		t.Errorf("%s carries %s=%q, want %q: the label is the join key retention reconciles over", container, host.LabelApp, held, lifecycleApp)
	}
	ref := run.vm.inspects(t, "container", container, host.LabelSelector(host.LabelRef))
	repository, named := host.Repository(ref)
	if !named {
		t.Fatalf("%s carries %s=%q, which names no repository the box could ever sweep under", container, host.LabelRef, ref)
	}
	if read := run.vm.reads(t, container, lifecycleSensitive); read != run.value {
		t.Fatalf("%s does not read %s as the value this deploy resolved, and nothing below turns on a value the container never got", container, lifecycleSensitive)
	}

	envFile := host.EnvFile(class, container)
	run.vm.proves(t, envFile)
	if run.vm.stands(t, envFile) {
		t.Errorf("%s stands after the deploy that wrote it, and it holds in plaintext every value this deploy resolved", envFile)
	}
	held := run.vm.ssh(t, "sudo ls -A "+quote(host.StateDir(class)))
	if !strings.Contains(held, "records") {
		t.Fatalf("%s lists\n%s\nand names not even the records tier a bootstrap writes, so an env file missing from that listing means nothing", host.StateDir(class), held)
	}
	for _, left := range lines(held) {
		if strings.HasSuffix(strings.TrimSpace(left), ".env") {
			t.Errorf("%s still holds %s after the deploy, and a file no deploy after this one takes back holds every value in plaintext", host.StateDir(class), left)
		}
	}
	if at := strings.Index(deployed, run.value); at >= 0 {
		t.Errorf("the deploy printed the value it handed %s at byte %d of its transcript, and this transcript is what a ci log keeps:\n%s", lifecycleApp, at, redacted(deployed, at, len(run.value)))
	}
	if !strings.Contains(deployed, lifecycleApp) {
		t.Fatalf("the deploy transcript never even names %s, so it is no window a leaked value could have appeared in:\n%s", lifecycleApp, deployed)
	}
	if !strings.Contains(deployed, "`ocel domain add`") {
		t.Errorf("the deploy that creates the edge surface never said what binds %s:\n%s", lifecycleHostname, deployed)
	}

	written := run.vm.proxyLogBytes(t)
	owed := run.refused(t, "domain", "add")
	for _, want := range []string{
		"A     " + lifecycleHostname + "  " + run.vm.addr,
		"has no DNS writer configured",
		lifecycleHostname + " does not answer as the box edge yet",
	} {
		if !strings.Contains(owed, want) {
			t.Errorf("`ocel domain add` never said %q, and a box's whole dns story is the record it owes and the honesty of its probe:\n%s", want, owed)
		}
	}
	run.trusting(t)
	bound := run.deploying(t, "domain", "add")
	if !strings.Contains(bound, "Serving "+lifecycleHostname) {
		t.Fatalf("`ocel domain add` never settled %s once this machine trusted the root the box issues from:\n%s", lifecycleHostname, bound)
	}
	run.serving(t, lifecycleHostname, "one")

	reported := run.deploying(t, "domain", "status")
	if !strings.Contains(reported, lifecycleHostname) {
		t.Fatalf("`ocel domain status` names no hostname at all, so it is no window a certificate claim could be read out of:\n%s", reported)
	}
	for _, want := range []string{"proxy:" + lifecycleHostname, "no expiry reported", "ocel issues and renews nothing here", lifecycleHostname + " A " + run.vm.addr} {
		if !strings.Contains(reported, want) {
			t.Errorf("`ocel domain status` never said %q: what serves a hostname on a box, and who renews it, is the whole of what this command owes:\n%s", want, reported)
		}
	}
	loaded, err := json.Marshal(run.vm.loadedProxyConfig(t))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(loaded), lifecycleHostname) {
		t.Fatalf("the loaded configuration names no hostname this journey bound, so it is no window a tls policy could be read out of:\n%s", loaded)
	}
	if strings.Contains(string(loaded), "on_demand") {
		t.Errorf("the loaded configuration declares an on-demand tls policy:\n%s\nCaddy only warns when one carries no permission module and serves anyway, so a catch-all beside it is an unauthenticated acme trigger a stranger drives with a junk subdomain until this box is locked out of its own tls", loaded)
	}

	retired := run.container(t)
	retiredRef := run.vm.inspects(t, "container", retired, host.LabelSelector(host.LabelRef))
	run.fixture(t, "two")
	var redeployed string
	watched := run.underLoad(t, lifecycleHostname, retired, func() {
		redeployed = run.deploying(t, "deploy", "--yes")
	})
	if !strings.Contains(redeployed, "Deployed") {
		t.Fatalf("the second `ocel deploy --yes` finished without deploying:\n%s", redeployed)
	}
	run.serving(t, lifecycleHostname, "two")

	crossed := map[string]int{}
	for _, answered := range watched.answers {
		if answered.err != nil {
			t.Errorf("a request held open across the redeploy failed outright (%v), and a flip drops nothing", answered.err)
			continue
		}
		if answered.status != http.StatusOK {
			t.Errorf("a request held open across the redeploy answered %d %q, want the release it started against, with its original status", answered.status, answered.body)
			continue
		}
		crossed[answered.body]++
	}
	if crossed["one"] == 0 || crossed["two"] == 0 {
		t.Errorf("%d requests were held open across the redeploy and they answered %v, want some held against the release the flip retired and some against the one it promoted: a load that never spans the flip proves nothing about what the flip does to a request in flight",
			len(watched.answers), crossed)
	}
	if !watched.together || !watched.inflight {
		t.Errorf("%s stood beside the release that replaced it in the proxy's pool: %v, and was serving a request there: %v — want both, or a request that answered across the flip answered before it and proves nothing:\n%v",
			retired, watched.together, watched.inflight, watched.samples)
	}
	if watched.drained < 0 {
		t.Errorf("%s's in-flight count was never read as zero while it was retired, and the drain that waits for it is the whole of why the old container is stopped second:\n%v",
			retired, watched.samples)
	}
	if watched.stopped < 0 {
		t.Errorf("%s was never read as stopped while the flip ran, so nothing here says the drain finished before it went:\n%v", retired, watched.samples)
	}
	if watched.drained >= 0 && watched.stopped >= 0 && watched.drained > watched.stopped {
		t.Errorf("%s was stopped at sample %d and its in-flight count only reached zero at %d, so a request it was still serving died with it:\n%v",
			retired, watched.stopped, watched.drained, watched.samples)
	}
	if state := run.vm.state(t, retired); state != "exited" {
		t.Errorf("%s reads %q after the deploy that replaced it, want it stopped and still standing: a rollback the ledger still offers has nothing to restart once it is removed", retired, state)
	}
	window := run.window(t, class)
	if len(window) != 2 || len(window) > host.KeepWindow {
		t.Fatalf("%s names %v, want the two refs two deploys left, most-recent-first, inside a window of %d", host.ReleasesDir()+"/"+lifecycleApp+"/"+string(class), window, host.KeepWindow)
	}
	if serving := run.vm.inspects(t, "container", run.container(t), host.LabelSelector(host.LabelRef)); window[0] != serving {
		t.Errorf("the keep window reads %v and %s serves %s, want the most recent ref first", window, lifecycleApp, serving)
	}
	if window[1] != retiredRef {
		t.Errorf("the keep window reads %v and the release this deploy retired is %s, want the window ordered most-recent-first past its own first entry", window, retiredRef)
	}

	standingBefore := run.appContainers(t)
	imagesBefore := run.appImages(t, repository)
	if len(standingBefore) != 2 || len(imagesBefore) != 2 {
		t.Fatalf("this box holds containers %v and images %v after two deploys, and a rollback that provisions nothing can only be read against both releases standing", standingBefore, imagesBefore)
	}
	previous := run.promotionOf(t, retiredRef)
	rolled := run.deploying(t, "rollback")
	if !strings.Contains(rolled, "Rolled back to promotion "+previous) {
		t.Errorf("`ocel rollback` said %q and never named %s, the promotion that carries %s, so a flip to any other release reads the same:\n%s",
			firstLineOf(rolled, "Rolled back to promotion"), previous, retiredRef, rolled)
	}
	run.serving(t, lifecycleHostname, "one")
	if after := run.appContainers(t); !slices.Equal(after, standingBefore) {
		t.Errorf("the containers on this box read\n%v\nafter a rollback that read\n%v\nbefore it: a rollback ensures the promotion's containers are running and then flips, and provisions nothing",
			after, standingBefore)
	}
	if state := run.vm.state(t, retired); state != "running" {
		t.Errorf("%s reads %q after the rollback that re-pointed at it, want the container its own deploy stood up serving again rather than a fresh one carrying none of its values", retired, state)
	}
	if after := run.appImages(t, repository); !slices.Equal(after, imagesBefore) {
		t.Errorf("the images under %s read %v after a rollback that read %v before it, and a rollback re-points at a release this box already holds", repository, after, imagesBefore)
	}

	rival := run.rival(t)
	taken := rival.refused(t, "deploy", "--yes")
	for _, want := range []string{lifecycleHostname + " is held by " + boxedge.Surface(lifecycleSlug, edge.ClassProduction), "another project already serves a hostname this project declares"} {
		if !strings.Contains(taken, want) {
			t.Errorf("a second project declaring %s was refused without saying %q, and a hostname belongs to one project:\n%s", lifecycleHostname, want, taken)
		}
	}
	if after := run.appContainers(t); !slices.Equal(after, standingBefore) {
		t.Errorf("the containers on this box read\n%v\nafter the refused deploy where they read\n%v\nbefore it: a claimed hostname is refused at preflight, before a byte crosses to the box and before a route moves", after, standingBefore)
	}
	if after := run.appImages(t, repository); !slices.Equal(after, imagesBefore) {
		t.Errorf("the images under %s read\n%v\nafter the refused deploy where they read\n%v\nbefore it: a hostname another project holds is refused at preflight, before an image is streamed onto the box",
			repository, after, imagesBefore)
	}

	if previewed := run.must(t, "bootstrap", "preview", "--yes"); !strings.Contains(previewed, "Bootstrapped") {
		t.Fatalf("`ocel bootstrap preview --yes` finished without bootstrapping:\n%s", previewed)
	}
	wildcard := edge.PreviewWildcard(lifecyclePreviewBase)
	if used := run.deploying(t, "domain", "use", wildcard, "--preview"); !strings.Contains(used, "Previews are served on "+wildcard) {
		t.Fatalf("`ocel domain use %s --preview` never installed the entry every project's previews fall to:\n%s", wildcard, used)
	}
	routed := run.vm.routedHosts(t)
	if !slices.Contains(routed, wildcard) {
		t.Fatalf("the loaded configuration routes %v and names no catch-all, so nothing below about what falls to it means anything", routed)
	}
	skipped, listed := nestedIn(t, run.vm.loadedProxyConfig(t), "apps", "http", "servers", "ocel", "automatic_https", "skip_certificates").([]any)
	if !listed || len(skipped) != 1 || skipped[0] != wildcard {
		t.Errorf("automatic https skips %v, want exactly %s: it is the one route on this box that must never be acme-eligible, and stock Caddy would order a wildcard it cannot obtain forever", skipped, wildcard)
	}
	missed := run.over(t, "unclaimed."+lifecyclePreviewBase, "/")
	if missed.status != http.StatusNotFound || !missed.fromTheBox() || missed.body != "" {
		t.Errorf("an unclaimed hostname under %s was answered %d %q\n%s\nwant a bare 404 carrying %s: %s, naming no project and no other preview",
			lifecyclePreviewBase, missed.status, missed.body, missed.headers, edge.HeaderEdge, host.EdgeName)
	}
	var wildcards []string
	for _, served := range routed {
		if strings.HasPrefix(served, "*.") {
			wildcards = append(wildcards, served)
		}
	}
	if !slices.Equal(wildcards, []string{wildcard}) {
		t.Errorf("the loaded configuration carries the suffix rules %v, want exactly %s: every other route this box holds names a hostname in full, and a second wildcard is a second open door",
			wildcards, wildcard)
	}
	if probed := run.over(t, edge.ProbeHostname(wildcard), "/"); !probed.fromTheBox() {
		t.Errorf("%s was answered without %s: %s, and it is the hostname `ocel domain use --preview` reads the edge off:\n%s",
			edge.ProbeHostname(wildcard), edge.HeaderEdge, host.EdgeName, probed.headers)
	}

	if held := run.deploying(t, "env", "set", lifecycleSensitive, run.value, "--preview"); !strings.Contains(held, "Set "+lifecycleSensitive) {
		t.Fatalf("`ocel env set %s --preview` said nothing about setting it:\n%s", lifecycleSensitive, held)
	}
	if up := run.deploying(t, "preview", "up", "--name", lifecyclePreview, "--yes"); !strings.Contains(up, "Preview "+lifecyclePreview+" is up") {
		t.Fatalf("`ocel preview up --name %s` finished without a preview:\n%s", lifecyclePreview, up)
	}
	previewHost := lifecyclePreview + "." + lifecyclePreviewBase
	run.serving(t, previewHost, "two")
	if listed := run.deploying(t, "preview", "ls"); !strings.Contains(listed, lifecyclePreview) {
		t.Errorf("`ocel preview ls` never listed %s:\n%s", lifecyclePreview, listed)
	}
	if !slices.Contains(run.vm.routedHosts(t), previewHost) {
		t.Fatalf("the loaded configuration routes %v and names no %s, so nothing below about it falling back means anything", run.vm.routedHosts(t), previewHost)
	}
	if removed := run.deploying(t, "preview", "rm", "--name", lifecyclePreview, "--yes"); !strings.Contains(removed, lifecyclePreview) {
		t.Errorf("`ocel preview rm --name %s` said nothing about what it tore down:\n%s", lifecyclePreview, removed)
	}
	fell := run.over(t, previewHost, "/")
	if fell.status != http.StatusNotFound || !fell.fromTheBox() || fell.body != "" {
		t.Errorf("%s was answered %d %q\n%s\nafter its preview came down, want the catch-all's bare 404 carrying %s: %s",
			previewHost, fell.status, fell.body, fell.headers, edge.HeaderEdge, host.EdgeName)
	}
	if left := run.vm.routedHosts(t); slices.Contains(left, previewHost) {
		t.Errorf("the loaded configuration still routes %s after `ocel preview rm`: %v", previewHost, left)
	} else if !slices.Contains(left, wildcard) {
		t.Errorf("the catch-all %s came down with one project's preview, and it answers for every project this box serves: %v", wildcard, left)
	}

	after := run.vm.proxyLogBytes(t)
	if after <= written {
		t.Fatalf("the proxy had logged %d bytes before this journey bound anything and %d after every hostname it binds, so its log is no window an acme order could have appeared in", written, after)
	}
	since := run.vm.proxyLogSince(t, written)
	for _, bound := range []string{lifecycleHostname, previewHost} {
		if !strings.Contains(since, "\"identifier\":\""+bound+"\"") {
			t.Fatalf("the proxy logged no certificate work for %s across every bind this journey made, so those logs are no window an acme order could have appeared in:\n%s", bound, since)
		}
		if !localAuthority(since, bound) {
			t.Errorf("the proxy issued %s from something other than the box's own local authority:\n%s", bound, since)
		}
	}
	for _, fired := range []string{"acme", "letsencrypt", "zerossl"} {
		if strings.Contains(strings.ToLower(since), fired) {
			t.Errorf("the proxy log names %q across the binds this journey made, and ocel asks no certificate authority on a box:\n%s", fired, since)
		}
	}

	if reached := run.vm.peers(t, "curl -sS -m 5 -o /dev/null -w '%{http_code}' http://"+host.ProxyContainer+"/"); !strings.Contains(reached, "404") {
		t.Fatalf("a container on the shared network could not reach the proxy on port %s at all (%q), so nothing it fails to reach on %s means anything",
			host.RenewalPort, strings.TrimSpace(reached), host.AdminPort)
	}
	if reached := run.vm.peers(t, "curl -sS -m 5 -o /dev/null -w '%{http_code}' http://"+host.ProxyContainer+":"+host.AdminPort+"/config/"); strings.Contains(reached, "200") {
		t.Errorf("a container on the shared network reached the admin endpoint on %s and got %q: every app this box runs would hold arbitrary config replacement of its own edge",
			host.AdminPort, strings.TrimSpace(reached))
	}

	for _, tier := range []string{"production", "preview"} {
		if destroyed := run.deploying(t, "destroy", tier, "--yes"); !strings.Contains(destroyed, "Destroyed") {
			t.Fatalf("`ocel destroy %s --yes` finished without destroying:\n%s", tier, destroyed)
		}
	}
	run.gaveBack(t, repository)
	served := run.vm.routedHosts(t)
	if !slices.Contains(served, wildcard) {
		t.Fatalf("the loaded configuration routes %v and names not even the bootstrap-owned catch-all, so a hostname missing from it means nothing", served)
	}
	if slices.Contains(served, lifecycleHostname) {
		t.Errorf("%s is still routed after `ocel destroy production`: %v", lifecycleHostname, served)
	}

	trusted, err := os.ReadFile(run.store)
	if err != nil {
		t.Fatal(err)
	}
	untouched := run.claims(t, "kept.ocel-vps-e2e.invalid")
	before := run.vm.routedHosts(t)
	if !slices.Contains(before, untouched) || !slices.Contains(before, wildcard) {
		t.Fatalf("the loaded configuration routes %v and names not both %s and %s, so nothing read off it after the release means anything", before, wildcard, untouched)
	}
	if released := run.deploying(t, "domain", "release", "--preview", "--yes"); !strings.Contains(released, "Released "+wildcard) {
		t.Fatalf("`ocel domain release --preview` never took %s down:\n%s", wildcard, released)
	}
	left := run.vm.routedHosts(t)
	if !slices.Contains(left, untouched) {
		t.Fatalf("the loaded configuration routes %v after `ocel domain release --preview` and names not even %s, which the release never touches, so the absence of the catch-all from it is the absence of every route", left, untouched)
	}
	if slices.Contains(left, wildcard) {
		t.Errorf("the loaded configuration still routes %s after `ocel domain release --preview`: %v", wildcard, left)
	}
	if shed := run.must(t, "bootstrap", "destroy", "preview", "--yes"); !strings.Contains(shed, "Removed the preview bootstrap") {
		t.Fatalf("`ocel bootstrap destroy preview --yes` finished without removing it:\n%s", shed)
	}
	const typing = "Type the environment name"
	destroyed, err := run.onATerminal(t, []string{"bootstrap", "destroy", "production"}, typing, "production\r")
	if err != nil {
		t.Fatalf("`ocel bootstrap destroy production` on a terminal = %v\n%s", err, destroyed)
	}
	asked := strings.Index(destroyed, typing)
	if asked < 0 {
		t.Fatalf("`ocel bootstrap destroy production` never asked for the environment name, so there is no confirmation for anything to be named before:\n%s", destroyed)
	}
	for _, bearing := range []string{host.StateDir(class), host.SealKeyPath(class), host.ProxyData} {
		switch at := strings.Index(destroyed, bearing); {
		case at < 0:
			t.Errorf("`ocel bootstrap destroy production` never named %s, and a user types the confirmation without knowing what is unrecoverable — %s holds every certificate this box was issued and the account key that issued them:\n%s",
				bearing, host.ProxyData, destroyed)
		case at > asked:
			t.Errorf("`ocel bootstrap destroy production` named %s only after it asked for the environment name, and a user who has already typed it has already consented to something they were not shown:\n%s",
				bearing, destroyed)
		}
	}

	gone := run.must(t, "doctor")
	if !owedABootstrap.MatchString(gone) {
		t.Errorf("`ocel doctor` after a destroy still claims a bootstrap:\n%s", gone)
	}
	run.gaveBack(t, repository)
	for _, taken := range []string{
		filepath.Dir(host.ClassDir(class)), host.ClassDir(class), host.StateDir(class),
		filepath.Dir(host.SealHelper), host.ProxyData, host.ProxyConfig, host.ProxyHelper,
	} {
		if run.vm.stands(t, taken) {
			t.Errorf("%s stands after a destroy, so the machine was not given back", taken)
		}
	}
	if run.vm.running(t, host.ProxyContainer) {
		t.Errorf("%s is still running after both bootstrap destroys, and ocel takes back the proxy it installed", host.ProxyContainer)
	}
	if left := run.vm.inspects(t, "container", host.ProxyContainer, "{{.Id}}"); left != "" {
		t.Errorf("%s reads %q after both bootstrap destroys, and nothing ocel placed on this machine survives them", host.ProxyContainer, left)
	}
	if strings.TrimSpace(run.vm.ssh(t, "id -u "+host.DeployUser()+" >/dev/null 2>&1 && echo standing || echo gone")) != "gone" {
		t.Errorf("%s still logs in after the last class on this machine went", host.DeployUser())
	}
	if strings.TrimSpace(run.vm.ssh(t, "command -v docker >/dev/null && echo standing || echo gone")) != "standing" {
		t.Error("the docker engine went with the destroy, and removing ocel from a host must never remove what runs on it")
	}
	run.survived(t, planted)
	if after, err := os.ReadFile(run.store); err != nil || !bytes.Equal(after, trusted) {
		t.Errorf("destroy edited %s; ocel never edits the user's trust store", run.store)
	}
	if !strings.Contains(destroyed, "ssh-keygen -R") {
		t.Errorf("destroy left the known_hosts entry standing without saying the line that takes it:\n%s", destroyed)
	}
}
