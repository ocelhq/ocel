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

	"github.com/ocelhq/ocel/pkg/providerkit"
)

const (
	liveRegistryImage = "registry:2.8.3"
	liveHtpasswdImage = "httpd:2.4-alpine"
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

func (vm machine) hashed(t *testing.T, password string) string {
	t.Helper()
	rendered, err := vm.feeding(vm.user, password+"\n",
		"sudo docker run --rm -i --entrypoint htpasswd "+liveHtpasswdImage+" -niB "+liveRegistryLogin)
	if err != nil {
		t.Fatalf("hash the registry password on the machine: %v", err)
	}
	line := strings.TrimSpace(rendered)
	if !strings.HasPrefix(line, liveRegistryLogin+":$2") {
		t.Fatalf("htpasswd answered %q, want a bcrypt line for %s: the registry reads no other hash", line, liveRegistryLogin)
	}
	return line
}

func (vm machine) registry(t *testing.T) providerkit.RegistryTarget {
	t.Helper()
	port := freePort(t)
	password := secretOf(t)
	run := secretOf(t)
	name, dir := "ocel-live-registry-"+run, "/tmp/ocel-live-registry-"+run
	hashed := vm.hashed(t, password)

	t.Cleanup(func() {
		vm.ssh(t, "sudo docker rm -f "+name+" >/dev/null 2>&1 || true; sudo rm -rf "+dir)
	})
	vm.ssh(t, strings.Join([]string{
		"set -e",
		"mkdir -p " + dir,
		"printf '%s\\n' " + shellQuoted(hashed) + " > " + dir + "/htpasswd",
		"chmod 644 " + dir + "/htpasswd",
		fmt.Sprintf("sudo docker run -d --name %s -p 127.0.0.1:%d:5000 -v %s:/auth "+
			"-e REGISTRY_AUTH=htpasswd -e REGISTRY_AUTH_HTPASSWD_REALM=ocel -e REGISTRY_AUTH_HTPASSWD_PATH=/auth/htpasswd %s >/dev/null",
			name, port, dir, liveRegistryImage),
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

func liveDigest(t *testing.T, target providerkit.RegistryTarget, coordinate string) string {
	t.Helper()
	seed := providerkit.ImagePush{
		App:    pullRepository,
		Source: transferCoordinate(),
		Target: coordinate,
		Digest: transferDigest,
	}
	if err := providerkit.RegistryImages(target).Push(context.Background(), seed, nil); err != nil {
		t.Fatalf("push %s into the registry the machine pulls from = %v", coordinate, err)
	}
	req, err := http.NewRequest(http.MethodHead,
		"http://"+target.Server+"/v2/"+pullNamespace+"/"+pullRepository+"/manifests/"+transferTag, nil)
	if err != nil {
		t.Fatal(err)
	}
	req.SetBasicAuth(target.Username, target.Password)
	req.Header.Set("Accept", strings.Join([]string{
		"application/vnd.oci.image.index.v1+json",
		"application/vnd.oci.image.manifest.v1+json",
		"application/vnd.docker.distribution.manifest.list.v2+json",
		"application/vnd.docker.distribution.manifest.v2+json",
	}, ", "))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	digest := resp.Header.Get("Docker-Content-Digest")
	if resp.StatusCode != http.StatusOK || digest == "" {
		t.Fatalf("the registry answered %q with digest %q for %s, and a pull is pinned to the digest it hands back", resp.Status, digest, coordinate)
	}
	return digest
}

func TestLiveTheMachinePullsTheImageAndIsLeftHoldingNoCredential(t *testing.T) {
	vm := live(t)
	bootstrapped(t, vm, providerkit.ClassProduction)
	_, _ = imported(t)

	target := vm.registry(t)
	coordinate := target.Coordinate(pullRepository, transferTag)
	digest := liveDigest(t, target, coordinate)
	push := providerkit.ImagePush{
		App:    pullRepository,
		Source: transferCoordinate(),
		Target: coordinate,
		Digest: digest,
	}
	t.Cleanup(func() {
		vm.ssh(t, "sudo docker image rm -f "+coordinate+" "+pinnedAt(coordinate, digest)+" >/dev/null 2>&1 || true")
	})

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
	held, err := vm.feeding(vm.user, target.Password+"\n", "sudo grep -rlF -f - /tmp /home /root /var/log 2>/dev/null")
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
