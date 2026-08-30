package imagebuild_test

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ocelhq/ocel/cli/internal/imagebuild"
)

type machine struct {
	addr  string
	user  string
	key   string
	known string
}

func live(t *testing.T) machine {
	t.Helper()
	vm := machine{
		addr: os.Getenv("OCEL_INCUS_ADDR"),
		user: os.Getenv("OCEL_INCUS_USER"),
		key:  os.Getenv("OCEL_INCUS_KEY"),
	}
	if vm.addr == "" || vm.user == "" || vm.key == "" {
		t.Skip("no incus VM in the environment; run under `scripts/incus.sh run <name> -- go test ./...`")
	}

	vm.known = filepath.Join(t.TempDir(), "known_hosts")
	scanned, err := exec.Command("ssh-keyscan", "-T", "10", vm.addr).Output()
	if err != nil {
		t.Fatalf("ssh-keyscan %s: %v", vm.addr, err)
	}
	if len(strings.TrimSpace(string(scanned))) == 0 {
		t.Fatalf("ssh-keyscan %s offered no host key", vm.addr)
	}
	if err := os.WriteFile(vm.known, scanned, 0o600); err != nil {
		t.Fatal(err)
	}
	return vm
}

func (vm machine) opts() []string {
	return []string{
		"-i", vm.key,
		"-o", "IdentitiesOnly=yes",
		"-o", "BatchMode=yes",
		"-o", "UserKnownHostsFile=" + vm.known,
		"-o", "GlobalKnownHostsFile=/dev/null",
		"-o", "LogLevel=ERROR",
	}
}

func (vm machine) ssh(t *testing.T, command string) string {
	t.Helper()
	said, err := vm.attempt(command)
	if err != nil {
		t.Fatalf("ssh %q: %v\n%s", command, err, said)
	}
	return said
}

func (vm machine) attempt(command string) (string, error) {
	cmd := exec.Command("ssh", append(vm.opts(), vm.user+"@"+vm.addr, command)...)
	said, err := cmd.CombinedOutput()
	return strings.TrimSpace(string(said)), err
}

func (vm machine) engine(t *testing.T) {
	t.Helper()
	vm.ssh(t, "command -v docker >/dev/null || (curl -fsSL https://get.docker.com | sudo sh) >/dev/null")
	vm.ssh(t, `sudo mkdir -p /etc/docker && printf '{"features":{"containerd-snapshotter":true}}\n' | sudo tee /etc/docker/daemon.json >/dev/null`)
	vm.ssh(t, "sudo usermod -aG docker "+vm.user)
	vm.ssh(t, "sudo systemctl restart docker")
	vm.ssh(t, "docker version >/dev/null")
}

func (vm machine) forward(t *testing.T) {
	t.Helper()
	dir, err := os.MkdirTemp("", "ocel-live-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	socket := filepath.Join(dir, "docker.sock")

	tunnel := exec.Command("ssh", append(vm.opts(),
		"-N", "-L", socket+":/var/run/docker.sock", vm.user+"@"+vm.addr)...)
	if err := tunnel.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = tunnel.Process.Kill()
		_ = tunnel.Wait()
	})

	t.Setenv(imagebuild.DockerHostEnv, "unix://"+socket)
	deadline := time.Now().Add(30 * time.Second)
	for {
		err := imagebuild.Reachable(context.Background())
		if err == nil {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("the forwarded daemon socket never answered: %v", err)
		}
		time.Sleep(time.Second)
	}
}

type progress struct{ t *testing.T }

func (p progress) Write(b []byte) (int, error) {
	p.t.Log(strings.TrimRight(string(b), "\n"))
	return len(b), nil
}

func TestLiveARailpackBuildLandsAWorkingImageInTheDaemon(t *testing.T) {
	vm := live(t)
	vm.engine(t)
	vm.forward(t)
	t.Setenv("OCEL_LIVE_LEAK", "a value the build must never see")

	image, err := imagebuild.Builder{Progress: progress{t}}.Build(context.Background(), "Web API", "testdata/plainserver")
	if err != nil {
		t.Fatalf("Build() against a real daemon = %v", err)
	}

	if image.Repository != "ocel/web-api" {
		t.Errorf("the image's repository is %q, want one derived from the app's name", image.Repository)
	}
	if want := image.Repository + "@" + image.Digest; image.Ref != want {
		t.Errorf("the image's ref is %q, want %q", image.Ref, want)
	}
	for _, coordinate := range []string{image.Ref, image.Repository + ":" + image.Tag} {
		if _, err := vm.attempt("docker image inspect " + coordinate); err != nil {
			t.Errorf("the daemon holds no image at %s, so the coordinate ocel hands a provider names nothing: %v", coordinate, err)
		}
	}

	if pulled := vm.ssh(t, "docker image ls --format '{{.Repository}}'"); strings.Contains(pulled, "railpack-frontend") {
		t.Errorf("the daemon pulled a railpack frontend image, so the build was not in-process:\n%s", pulled)
	}
	if env := vm.ssh(t, "docker image inspect --format '{{.Config.Env}}' "+image.Ref); strings.Contains(env, "OCEL_LIVE_LEAK") {
		t.Errorf("the image carries %q, so a variable from ocel's own environment was baked into it", env)
	}

	name := "ocel-live-build"
	vm.ssh(t, "docker rm -f "+name+" >/dev/null 2>&1 || true")
	t.Cleanup(func() { _, _ = vm.attempt("docker rm -f " + name) })
	vm.ssh(t, fmt.Sprintf("docker run -d --name %s -e PORT=8080 -p 127.0.0.1:18080:8080 %s", name, image.Ref))

	var said string
	deadline := time.Now().Add(60 * time.Second)
	for {
		out, err := vm.attempt("curl -sf -m 2 http://127.0.0.1:18080/")
		if err == nil {
			said = out
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the image railpack built never served on the injected PORT: %v\n%s", err, vm.ssh(t, "docker logs "+name+" 2>&1 | tail -30"))
		}
		time.Sleep(2 * time.Second)
	}
	if said != "plainserver" {
		t.Errorf("the running image answered %q, want the app's own response", said)
	}
}
