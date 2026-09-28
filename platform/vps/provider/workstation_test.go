package vps_test

import (
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
)

const (
	workstationSlug        = "ocel-vps-e2e"
	workstationHostname    = "ocel-vps-e2e.localhost"
	workstationPreviewBase = "preview.ocel-vps-e2e.localhost"
	workstationApp         = "web"
	unreleasedVersion      = "dev"
	patience               = 8 * time.Minute
)

type workstation struct {
	vm        machine
	bin       string
	project   string
	settings  string
	cache     string
	providers string
	shims     string
	store     string
}

func liveWorkstation(t *testing.T) workstation {
	t.Helper()
	vm := liveMachine(t)
	vm.purges(t)
	t.Cleanup(func() { vm.purges(t) })

	dir := t.TempDir()
	ws := workstation{
		vm:        vm,
		project:   filepath.Join(dir, "project"),
		settings:  filepath.Join(dir, "config"),
		providers: filepath.Join(dir, "providers"),
	}
	if err := os.MkdirAll(ws.project, 0o700); err != nil {
		t.Fatal(err)
	}
	ws.isolatesSSH(t)

	cli, deploy := binaries(t)
	ws.bin = cli
	linked(t, deploy, ws.installed())
	write(t, filepath.Join(ws.project, "ocel.config.ts"), ws.declaration(t))
	return ws
}

func (ws *workstation) isolatesSSH(t *testing.T) {
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

	ws.store = filepath.Join(dir, "known_hosts")
	ws.cache = filepath.Join(dir, "cache")
	ws.shims = filepath.Join(dir, "bin")
	config := filepath.Join(dir, "ssh_config")

	write(t, config, fmt.Sprintf("Host *\n  UserKnownHostsFile %s\n  GlobalKnownHostsFile /dev/null\n", ws.store))
	shim := filepath.Join(ws.shims, "ssh")
	write(t, shim, fmt.Sprintf("#!/bin/sh\nexec %s -F %s \"$@\"\n", real, config))
	if err := os.Chmod(shim, 0o700); err != nil {
		t.Fatal(err)
	}
}

func (ws workstation) installed() string {
	return filepath.Join(ws.providers, "provider", "vps", unreleasedVersion, runtime.GOOS+"-"+runtime.GOARCH, "provider-vps")
}

func (ws workstation) declaration(t *testing.T) string {
	t.Helper()
	options, err := json.Marshal(map[string]any{
		"vps": map[string]any{"ssh": map[string]any{
			"host":         ws.vm.addr,
			"user":         ws.vm.user,
			"identityFile": ws.vm.key,
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	apps, err := json.Marshal([]map[string]any{{
		"name":    workstationApp,
		"path":    "app",
		"compute": "container",
		"health":  map[string]any{"path": "/"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	return fmt.Sprintf("export default {\n  slug: %q,\n  provider: %s,\n  apps: %s,\n  domains: { production: %q, preview: %q },\n};\n",
		workstationSlug, options, apps, workstationHostname, "*."+workstationPreviewBase)
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
		root, err := filepath.Abs(filepath.Join("..", "..", ".."))
		if err != nil {
			buildErr = err
			return
		}
		builtCLI, builtShip = filepath.Join(dir, "ocel"), filepath.Join(dir, "deploy")
		if buildErr = built(filepath.Join(root, "cli"), builtCLI, "./ocel"); buildErr == nil {
			buildErr = built(".", builtShip, "./cmd/deploy")
		}
	})
	if buildErr != nil {
		t.Fatalf("%v\nthe CLI embeds a node bundle: `pnpm install --frozen-lockfile && pnpm --filter ocel build && go generate ./...` in cli/ builds it", buildErr)
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

func (ws workstation) forgets(t *testing.T) {
	t.Helper()
	if err := os.Remove(ws.store); err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
}

func (ws workstation) env() []string {
	var kept []string
	for _, entry := range os.Environ() {
		switch name, _, _ := strings.Cut(entry, "="); name {
		case "PATH", "XDG_CONFIG_HOME", "XDG_CACHE_HOME", "SSH_AUTH_SOCK", "OCEL_CONFIG", "OCEL_ACCESS_TOKEN":
			continue
		}
		kept = append(kept, entry)
	}
	return append(kept,
		"PATH="+ws.shims+string(os.PathListSeparator)+os.Getenv("PATH"),
		"XDG_CONFIG_HOME="+ws.settings,
		"XDG_CACHE_HOME="+ws.cache,
		"OCEL_PROVIDERS_DIR="+ws.providers,
		"OCEL_NO_BROWSER=1",
	)
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

func (ws workstation) onATerminal(t *testing.T, args []string, awaiting, answer string) (string, error) {
	t.Helper()
	cmd := exec.Command(ws.bin, args...)
	cmd.Dir = ws.project
	cmd.Env = ws.env()
	terminal, err := pty.StartWithSize(cmd, &pty.Winsize{Rows: 60, Cols: 200})
	if err != nil {
		t.Fatalf("no pty for %s, and the CLI only asks where a human is: %v", ws.bin, err)
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
