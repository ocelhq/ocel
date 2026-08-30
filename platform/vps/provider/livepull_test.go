package vps_test

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net"
	"net/http"
	"os/exec"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/bcrypt"

	"github.com/ocelhq/ocel/pkg/providerkit"
)

const (
	liveRegistryName  = "ocel-live-registry"
	liveRegistryImage = "registry:2.8.3"
	liveRegistryDir   = "/tmp/ocel-live-registry"
	liveRegistryLogin = "ocel-live"
	pullRepository    = "live-pull"
	pullNamespace     = "live"
)

func freePort(t *testing.T) int {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	return port
}

func secretOf(t *testing.T) string {
	t.Helper()
	raw := make([]byte, 16)
	if _, err := rand.Read(raw); err != nil {
		t.Fatal(err)
	}
	return hex.EncodeToString(raw)
}

func shellQuoted(arg string) string {
	return "'" + strings.ReplaceAll(arg, "'", `'\''`) + "'"
}

func (vm machine) registry(t *testing.T) providerkit.RegistryTarget {
	t.Helper()
	port := freePort(t)
	password := secretOf(t)
	hashed, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		t.Fatal(err)
	}

	vm.ssh(t, "sudo docker rm -f "+liveRegistryName+" >/dev/null 2>&1 || true")
	t.Cleanup(func() {
		vm.ssh(t, "sudo docker rm -f "+liveRegistryName+" >/dev/null 2>&1 || true; sudo rm -rf "+liveRegistryDir)
	})
	vm.ssh(t, strings.Join([]string{
		"set -e",
		"sudo rm -rf " + liveRegistryDir,
		"mkdir -p " + liveRegistryDir,
		"printf '%s\\n' " + shellQuoted(liveRegistryLogin+":"+string(hashed)) + " > " + liveRegistryDir + "/htpasswd",
		"chmod 644 " + liveRegistryDir + "/htpasswd",
		fmt.Sprintf("sudo docker run -d --name %s -p 127.0.0.1:%d:5000 -v %s:/auth "+
			"-e REGISTRY_AUTH=htpasswd -e REGISTRY_AUTH_HTPASSWD_REALM=ocel -e REGISTRY_AUTH_HTPASSWD_PATH=/auth/htpasswd %s >/dev/null",
			liveRegistryName, port, liveRegistryDir, liveRegistryImage),
	}, "\n"))

	vm.forwarding(t, port)
	server := fmt.Sprintf("127.0.0.1:%d", port)
	answering(t, server)
	return providerkit.RegistryTarget{
		Server:    server,
		Namespace: pullNamespace,
		Username:  liveRegistryLogin,
		Password:  password,
	}
}

func (vm machine) forwarding(t *testing.T, port int) {
	t.Helper()
	forward := fmt.Sprintf("127.0.0.1:%d:127.0.0.1:%d", port, port)
	tunnel := exec.Command("ssh",
		"-F", vm.config, "-i", vm.key,
		"-o", "IdentitiesOnly=yes", "-o", "BatchMode=yes",
		"-N", "-L", forward, vm.user+"@"+vm.addr)
	if err := tunnel.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = tunnel.Process.Kill()
		_ = tunnel.Wait()
	})
}

func answering(t *testing.T, server string) {
	t.Helper()
	deadline := time.Now().Add(90 * time.Second)
	var said string
	for time.Now().Before(deadline) {
		resp, err := http.Get("http://" + server + "/v2/")
		if err != nil {
			said = err.Error()
			time.Sleep(time.Second)
			continue
		}
		status := resp.StatusCode
		_ = resp.Body.Close()
		if status == http.StatusUnauthorized {
			return
		}
		said = fmt.Sprintf("it answered %d, want a registry that refuses an unauthenticated read", status)
		time.Sleep(time.Second)
	}
	t.Fatalf("the registry on the machine never answered at %s: %s", server, said)
}

func TestLiveTheMachinePullsTheImageAndIsLeftHoldingNoCredential(t *testing.T) {
	vm := live(t)
	bootstrapped(t, vm, providerkit.ClassProduction)
	_, _ = imported(t)

	target := vm.registry(t)
	coordinate := target.Coordinate(pullRepository, transferTag)
	push := providerkit.ImagePush{
		App:    pullRepository,
		Source: transferCoordinate(),
		Target: coordinate,
		Digest: transferDigest,
	}
	t.Cleanup(func() { vm.ssh(t, "sudo docker image rm -f "+coordinate+" >/dev/null 2>&1 || true") })

	ctx := context.Background()
	store, err := vm.deploying(t).Images(ctx, target)
	if err != nil {
		t.Fatalf("Images() = %v", err)
	}
	plan := providerkit.ImagePlan{Store: store, Pushes: []providerkit.ImagePush{push}}

	rows, err := plan.Rows(ctx)
	if err != nil {
		t.Fatalf("Rows() over a machine holding nothing = %v", err)
	}
	if len(rows) != 1 || rows[0].Action != providerkit.ActionCreate {
		t.Fatalf("the plan shows %v before anything carried the image, want one %q row", rows, providerkit.ActionCreate)
	}
	if err := plan.Ship(ctx, nil); err != nil {
		t.Fatalf("Ship() through a registry the machine can reach = %v", err)
	}

	named := strings.TrimSpace(vm.sshAs(t, deployLogin, "docker image ls --format '{{.Repository}}:{{.Tag}}' "+coordinate))
	if named != coordinate {
		t.Errorf("the machine's daemon names the pulled image %q, want %q: release, rollback and retention pin that coordinate whichever path carried it",
			named, coordinate)
	}

	for _, home := range []string{"/home/" + deployLogin, "/root"} {
		if _, err := vm.attempt(vm.user, "sudo test ! -e "+home+"/.docker/config.json"); err != nil {
			t.Errorf("%s/.docker/config.json stands after the pull, so the registry credential is resident on the machine", home)
		}
	}
	held, err := vm.attempt(vm.user, "sudo grep -rl "+shellQuoted(target.Password)+" /tmp /home /root 2>/dev/null")
	if strings.TrimSpace(held) != "" {
		t.Errorf("the registry password is written into %q on the machine", strings.TrimSpace(held))
	} else if err == nil {
		t.Error("grep found the registry password somewhere on the machine and named nothing")
	}

	after, err := plan.Rows(ctx)
	if err != nil {
		t.Fatalf("Rows() over a machine that holds the digest = %v", err)
	}
	if len(after) != 1 || after[0].Action != providerkit.ActionKeep {
		t.Errorf("the plan shows %v for an image the machine already pulled, want one %q row", after, providerkit.ActionKeep)
	}
	if err := plan.Ship(ctx, nil); err != nil {
		t.Fatalf("a second Ship over a machine that already holds the digest = %v", err)
	}
}
