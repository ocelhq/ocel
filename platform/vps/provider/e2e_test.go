package vps_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/creack/pty"

	"github.com/ocelhq/ocel/pkg/providerkit"
	"github.com/ocelhq/ocel/platform/vps/provider/host"
)

const e2eSlug = "ocel-vps-e2e"

const e2eHostname = "ocel-vps-e2e.invalid"

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
}

func e2e(t *testing.T) journey {
	t.Helper()
	vm := live(t)

	dir := t.TempDir()
	run := journey{
		vm:       vm,
		bin:      filepath.Join(dir, "ocel"),
		project:  filepath.Join(dir, "project"),
		settings: filepath.Join(dir, "config"),
	}
	if err := os.MkdirAll(run.project, 0o700); err != nil {
		t.Fatal(err)
	}
	run.apart(t)

	root := repoRoot(t)
	build(t, filepath.Join(root, "cli"), run.bin, "./ocel")
	build(t, ".", filepath.Join(run.installed(), "bin", "deploy"), "./cmd/deploy")
	write(t, filepath.Join(run.installed(), "package.json"), run.manifest(t))
	write(t, filepath.Join(run.project, "ocel.config.ts"), run.declaration(t))

	return run
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

func (j journey) declaration(t *testing.T) string {
	t.Helper()
	options, err := json.Marshal(map[string]any{
		"package": "@ocel/provider-vps",
		"options": map[string]any{"ssh": map[string]any{
			"host":         j.vm.addr,
			"user":         j.vm.user,
			"identityFile": j.vm.key,
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	return fmt.Sprintf("export default {\n  slug: %q,\n  provider: %s,\n  domains: { production: %q },\n};\n", e2eSlug, options, e2eHostname)
}

func repoRoot(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", "..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	return root
}

func build(t *testing.T, module, out, pkg string) {
	t.Helper()
	made := exec.Command("go", "build", "-C", module, "-o", out, pkg)
	if rendered, err := made.CombinedOutput(); err != nil {
		t.Fatalf("go build -C %s -o %s %s: %v\n%s\nthe CLI carries an embedded node bundle: `pnpm install --frozen-lockfile && pnpm --filter ocel build && go generate ./...` in cli/ builds it",
			module, out, pkg, err, rendered)
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
	return append(kept,
		"PATH="+j.shims+string(os.PathListSeparator)+os.Getenv("PATH"),
		"XDG_CONFIG_HOME="+j.settings,
		"XDG_CACHE_HOME="+j.cache,
	)
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

func TestE2ETheWholeJourneyRunsOnTheRealBinaryAndGivesTheMachineBack(t *testing.T) {
	run := e2e(t)
	run.trusts(t)
	run.vm.ssh(t, "sudo rm -rf /etc/ocel /var/lib/ocel /usr/local/lib/ocel")

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
		e2eHostname + " does not resolve",
		"something listens on port " + host.RenewalPort,
		"nothing listens on tcp " + host.AdminPort + " inside " + host.ProxyContainer,
	} {
		if !strings.Contains(standing, verdict) {
			t.Errorf("`ocel doctor` never said %q, and this is the only command that runs thirty days after a deploy:\n%s", verdict, standing)
		}
	}
	if strings.Contains(standing, "✗") {
		t.Errorf("`ocel doctor` refused something on a bootstrapped box whose one owed thing is a dns record a human has not written:\n%s", standing)
	}
	if strings.Contains(standing, "\nCertificates\n") {
		t.Errorf("`ocel doctor` printed a certificates section over %s, which is a reserved name nothing resolves and no acme issuer will ever answer for: a renewal reported for it is one read off something other than a certificate this box holds:\n%s",
			e2eHostname, standing)
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

	run.vm.runs(t, workload)
	defer run.vm.ssh(t, "sudo docker rm -f "+workload+" >/dev/null 2>&1 || true")
	decoy := run.vm.holds(t, decoyImage)

	trusted, err := os.ReadFile(run.store)
	if err != nil {
		t.Fatal(err)
	}
	destroyed := run.must(t, "bootstrap", "destroy", "production", "--yes")
	for _, bearing := range []string{host.StateDir(class), host.SealKeyPath(class)} {
		if !strings.Contains(destroyed, bearing) {
			t.Errorf("`ocel bootstrap destroy production` never named %s, and a user types the confirmation without knowing what is unrecoverable:\n%s", bearing, destroyed)
		}
	}

	gone := run.must(t, "doctor")
	if !owedABootstrap.MatchString(gone) {
		t.Errorf("`ocel doctor` after a destroy still claims a bootstrap:\n%s", gone)
	}
	for _, taken := range []string{host.ClassDir(class), host.StateDir(class), filepath.Dir(host.SealHelper)} {
		if run.vm.stands(t, taken) {
			t.Errorf("%s stands after a destroy, so the machine was not given back", taken)
		}
	}
	if strings.TrimSpace(run.vm.ssh(t, "id -u "+host.DeployUser()+" >/dev/null 2>&1 && echo standing || echo gone")) != "gone" {
		t.Errorf("%s still logs in after the last class on this machine went", host.DeployUser())
	}
	if strings.TrimSpace(run.vm.ssh(t, "command -v docker >/dev/null && echo standing || echo gone")) != "standing" {
		t.Error("the docker engine went with the destroy, and removing ocel from a host must never remove what runs on it")
	}
	if !run.vm.running(t, workload) {
		t.Errorf("%s is gone after `ocel bootstrap destroy production`, and a container ocel never ran is not ocel's to take", workload)
	}
	if after := run.vm.inspects(t, "image", decoyImage, "{{.Id}}"); after != decoy {
		t.Errorf("%s reads %q after the destroy where it read %q, and the images on a machine belong to whoever bought it", decoyImage, after, decoy)
	}
	if after, err := os.ReadFile(run.store); err != nil || !bytes.Equal(after, trusted) {
		t.Errorf("destroy edited %s; ocel never edits the user's trust store", run.store)
	}
	if !strings.Contains(destroyed, "ssh-keygen -R") {
		t.Errorf("destroy left the known_hosts entry standing without saying the line that takes it:\n%s", destroyed)
	}
}
