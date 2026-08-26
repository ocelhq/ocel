package vps_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
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

type journey struct {
	vm       machine
	bin      string
	project  string
	settings string
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
	run.store = borrowed(t, vm.addr)

	root := repoRoot(t)
	build(t, filepath.Join(root, "cli"), run.bin, "./ocel")
	build(t, ".", filepath.Join(run.installed(), "bin", "deploy"), "./cmd/deploy")
	write(t, filepath.Join(run.installed(), "package.json"), fmt.Sprintf("{\"name\":%q,\"version\":\"0.0.0\",\"bin\":{\"ocel-provider-vps-deploy\":\"bin/deploy\"}}\n", run.platformPackage()))
	write(t, filepath.Join(run.project, "ocel.config.ts"), run.declaration(t))

	return run
}

func borrowed(t *testing.T, address string) string {
	t.Helper()
	path := trustStore(t, address)
	held, err := os.ReadFile(path)
	switch {
	case err == nil:
		t.Cleanup(func() {
			if err := os.WriteFile(path, held, 0o600); err != nil {
				t.Errorf("%s was not given back as it was found: %v", path, err)
			}
		})
	case os.IsNotExist(err):
		t.Cleanup(func() { _ = os.Remove(path) })
	default:
		t.Fatal(err)
	}
	return path
}

func trustStore(t *testing.T, address string) string {
	t.Helper()
	rendered, err := exec.Command("ssh", "-G", address).Output()
	if err != nil {
		t.Fatalf("ssh -G %s: %v", address, err)
	}
	for line := range strings.Lines(string(rendered)) {
		name, value, split := strings.Cut(strings.TrimSpace(line), " ")
		if !split || name != "userknownhostsfile" {
			continue
		}
		if files := strings.Fields(value); len(files) > 0 {
			return files[0]
		}
	}
	t.Fatalf("ssh -G %s named no known_hosts file, so there is no trust store to borrow", address)
	return ""
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
	return fmt.Sprintf("export default {\n  slug: %q,\n  provider: %s,\n};\n", e2eSlug, options)
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
		case "XDG_CONFIG_HOME", "SSH_AUTH_SOCK", "OCEL_CONFIG", "OCEL_ACCESS_TOKEN":
			continue
		}
		kept = append(kept, entry)
	}
	return append(kept, "XDG_CONFIG_HOME="+j.settings)
}

func (j journey) run(t *testing.T, args ...string) (string, error) {
	t.Helper()
	cmd := exec.Command(j.bin, args...)
	cmd.Dir = j.project
	cmd.Env = j.env()
	var rendered bytes.Buffer
	cmd.Stdout = &rendered
	cmd.Stderr = &rendered
	err := cmd.Run()
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
		if !appears(&seen, awaiting) {
			_ = cmd.Process.Kill()
			<-read
			t.Fatalf("ocel %s never asked %q on a terminal:\n%s", strings.Join(args, " "), awaiting, plain(seen.String()))
		}
		if _, err := io.WriteString(terminal, answer); err != nil {
			t.Fatal(err)
		}
	}

	err = cmd.Wait()
	<-read
	return plain(seen.String()), err
}

func appears(seen *transcript, fragment string) bool {
	for deadline := time.Now().Add(3 * time.Minute); time.Now().Before(deadline); time.Sleep(200 * time.Millisecond) {
		if strings.Contains(plain(seen.String()), fragment) {
			return true
		}
	}
	return false
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
	fresh := run.must(t, "bootstrap", "status")
	if !strings.Contains(fresh, "production: not bootstrapped") {
		t.Fatalf("`ocel bootstrap status` on a machine nothing has written to said:\n%s", fresh)
	}

	applied := run.must(t, "bootstrap", "production", "--yes")
	if !strings.Contains(applied, "Bootstrapped") {
		t.Errorf("`ocel bootstrap production --yes` finished without saying it bootstrapped:\n%s", applied)
	}
	if stamp := stampOn(t, run.vm); stamp.State != host.StateComplete {
		t.Errorf("the stamp the CLI left reads state %q, want %q", stamp.State, host.StateComplete)
	}

	standing := run.must(t, "bootstrap", "status")
	if strings.Contains(standing, "production: not bootstrapped") {
		t.Fatalf("`ocel bootstrap status` after an apply still calls production unbootstrapped:\n%s", standing)
	}

	replanned := run.must(t, "bootstrap", "production", "--dry")
	if !strings.Contains(replanned, "No infrastructure changes") {
		t.Errorf("a re-plan over a bootstrapped machine found work to do:\n%s", replanned)
	}
	run.must(t, "bootstrap", "production", "--yes")

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

	gone := run.must(t, "bootstrap", "status")
	if !strings.Contains(gone, "production: not bootstrapped") {
		t.Errorf("`ocel bootstrap status` after a destroy still claims a bootstrap:\n%s", gone)
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
	if after, err := os.ReadFile(run.store); err != nil || !bytes.Equal(after, trusted) {
		t.Errorf("destroy edited %s; ocel never edits the user's trust store", run.store)
	}
	if !strings.Contains(destroyed, "ssh-keygen -R") {
		t.Errorf("destroy left the known_hosts entry standing without saying the line that takes it:\n%s", destroyed)
	}
}
