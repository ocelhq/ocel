package livemachine

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
	"github.com/ocelhq/ocel/pkg/providerkit"
)

type Machine struct {
	addr  string
	user  string
	key   string
	known string
}

func Require(t *testing.T) Machine {
	t.Helper()
	vm := Machine{
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

func (vm Machine) opts() []string {
	return []string{
		"-i", vm.key,
		"-o", "IdentitiesOnly=yes",
		"-o", "BatchMode=yes",
		"-o", "UserKnownHostsFile=" + vm.known,
		"-o", "GlobalKnownHostsFile=/dev/null",
		"-o", "LogLevel=ERROR",
	}
}

func (vm Machine) SSH(t *testing.T, command string) string {
	t.Helper()
	said, err := vm.Attempt(command)
	if err != nil {
		t.Fatalf("ssh %q: %v\n%s", command, err, said)
	}
	return said
}

func (vm Machine) Attempt(command string) (string, error) {
	cmd := exec.Command("ssh", append(vm.opts(), vm.user+"@"+vm.addr, command)...)
	said, err := cmd.CombinedOutput()
	return strings.TrimSpace(string(said)), err
}

const engineSetup = `set -e
exec 9>/tmp/ocel-engine.lock
flock 9
want='{"features":{"containerd-snapshotter":true}}'
if ! sudo docker info >/dev/null 2>&1; then
script=$(mktemp)
trap 'rm -f "$script"' EXIT
if curl -fsSL --connect-timeout 10 --retry 5 --retry-delay 2 --retry-all-errors https://get.docker.com -o "$script"; then
sudo sh "$script"
else
echo 'https://get.docker.com is unreachable from this machine, so the distro package stands in' >&2
sudo apt-get update
sudo apt-get install -y docker.io
fi
fi
docker --version
getent group docker >/dev/null || sudo groupadd docker
id -nG %[1]s | grep -qw docker || sudo usermod -aG docker %[1]s
if [ "$(cat /etc/docker/daemon.json 2>/dev/null)" != "$want" ]; then
sudo mkdir -p /etc/docker
printf '%%s\n' "$want" | sudo tee /etc/docker/daemon.json >/dev/null
sudo systemctl restart docker
fi
sudo docker version >/dev/null`

func (vm Machine) Engine(t *testing.T) {
	t.Helper()
	vm.SSH(t, fmt.Sprintf(engineSetup, vm.user))
}

func (vm Machine) Forward(t *testing.T) {
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

	t.Setenv(providerkit.DockerHostEnv, "unix://"+socket)
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

type Progress struct{ T *testing.T }

func (p Progress) Write(b []byte) (int, error) {
	p.T.Log(strings.TrimRight(string(b), "\n"))
	return len(b), nil
}
