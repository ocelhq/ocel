package vps_test

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"testing"

	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/tarball"

	"github.com/ocelhq/ocel/pkg/providerkit/images"
	"github.com/ocelhq/ocel/pkg/providerkit/provider"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
	"github.com/ocelhq/ocel/platform/vps/provider/host"
)

const (
	transferDigest = "sha256:1111111111111111111111111111111111111111111111111111111111111111"
	transferTag    = "sha256-1111111111111111111111111111111111111111111111111111111111111111"
)

var transferRepository = "ocel/live-transfer-" + strconv.Itoa(os.Getpid())

func transferBase() string { return transferRepository + ":" + transferTag }

func transferRuntime(t *testing.T, daemon images.DockerHost, client *http.Client) []byte {
	t.Helper()
	arch, err := daemon.Architecture(context.Background(), client, transferBase())
	if err != nil {
		t.Fatalf("read the architecture the daemon at %s imported %s as: %v", daemon.Address, transferBase(), err)
	}
	runtime, err := host.ContainerRuntime(arch)
	if err != nil {
		t.Fatal(err)
	}
	return runtime
}

func transferCoordinate(runtime []byte) string {
	return transferRepository + ":" + images.RuntimeTag(transferDigest, runtime)
}

func rootfs(t *testing.T) []byte {
	t.Helper()
	var raw bytes.Buffer
	written := tar.NewWriter(&raw)
	body := []byte("the image ocel moved\n")
	if err := written.WriteHeader(&tar.Header{Name: "ocel-live-transfer", Mode: 0o644, Size: int64(len(body))}); err != nil {
		t.Fatal(err)
	}
	if _, err := written.Write(body); err != nil {
		t.Fatal(err)
	}
	if err := written.Close(); err != nil {
		t.Fatal(err)
	}
	return raw.Bytes()
}

func localDaemon(t *testing.T) (images.DockerHost, *http.Client) {
	t.Helper()
	daemon, err := images.DockerHostFromEnv()
	if err != nil {
		t.Fatalf("no docker daemon this machine can name, and the image a transfer moves is read out of one: %v", err)
	}
	transport := daemon.Transport()
	t.Cleanup(transport.CloseIdleConnections)
	return daemon, &http.Client{Transport: transport}
}

func keepsImagesInContainerd(t *testing.T, daemon images.DockerHost, client *http.Client) {
	t.Helper()
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, "http://docker/info", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("the daemon at %s did not say which store it keeps images in: %v", daemon.Address, err)
	}
	defer func() { _ = resp.Body.Close() }()

	var info struct {
		Driver       string     `json:"Driver"`
		DriverStatus [][]string `json:"DriverStatus"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&info); err != nil {
		t.Fatal(err)
	}
	for _, row := range info.DriverStatus {
		if len(row) == 2 && row[0] == "driver-type" && strings.Contains(row[1], "containerd.snapshotter") {
			return
		}
	}
	t.Fatalf("the daemon at %s keeps images in its %s store, and a build the cli accepts always comes out of a containerd one: "+
		"exporting from a classic store here produces an archive production never produces, and the load onto the machine's own classic store "+
		"would prove a transition that never happens: turn the containerd image store on (\"features\": {\"containerd-snapshotter\": true} "+
		"in /etc/docker/daemon.json) or point %s at a daemon that has it",
		daemon.Address, info.Driver, images.DockerHostEnv)
}

func imported(t *testing.T) (images.DockerHost, *http.Client) {
	t.Helper()
	daemon, client := localDaemon(t)

	query := url.Values{"fromSrc": {"-"}, "repo": {transferRepository}, "tag": {transferTag}, "changes": {`CMD ["/ocel-live-transfer"]`}}
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost,
		"http://docker/images/create?"+query.Encode(), bytes.NewReader(rootfs(t)))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/x-tar")

	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("the daemon at %s did not answer an import: %v", daemon.Address, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("the daemon at %s answered %q importing %s", daemon.Address, resp.Status, transferBase())
	}
	if _, err := io.Copy(io.Discard, resp.Body); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { forget(client) })
	return daemon, client
}

func forget(client *http.Client) {
	req, err := http.NewRequestWithContext(context.Background(), http.MethodDelete,
		"http://docker/images/"+transferBase()+"?force=1", nil)
	if err != nil {
		return
	}
	resp, err := client.Do(req)
	if err != nil {
		return
	}
	_ = resp.Body.Close()
}

func transferPush(daemon images.DockerHost, client *http.Client, runtime []byte) provider.ImagePush {
	return provider.ImagePush{
		App:      "live-transfer",
		Source:   transferRepository + "@" + transferDigest,
		ImageRef: transferCoordinate(runtime),
		Digest:   transferDigest,
		Wrap: func(ctx context.Context) (v1.Image, func(), error) {
			return wrappedAsADeployDoes(ctx, daemon, client, runtime)
		},
	}
}

func wrappedAsADeployDoes(ctx context.Context, daemon images.DockerHost, client *http.Client, runtime []byte) (v1.Image, func(), error) {
	stream, err := daemon.Export(ctx, client, transferBase())
	if err != nil {
		return nil, nil, err
	}
	defer func() { _ = stream.Close() }()
	saved, err := os.CreateTemp("", "ocel-live-transfer-")
	if err != nil {
		return nil, nil, err
	}
	discard := func() { _ = os.Remove(saved.Name()) }
	if _, err := io.Copy(saved, stream); err != nil {
		discard()
		return nil, nil, err
	}
	if err := saved.Close(); err != nil {
		discard()
		return nil, nil, err
	}
	base, err := tarball.ImageFromPath(saved.Name(), nil)
	if err != nil {
		discard()
		return nil, nil, err
	}
	wrapped, err := images.WrapContainer(base, runtime)
	if err != nil {
		discard()
		return nil, nil, err
	}
	return wrapped, discard, nil
}

func TestLiveAnImageIsMovedOntoTheMachineUnderTheCoordinateItWasBuiltAs(t *testing.T) {
	vm := liveMachine(t)
	bootstrapped(t, vm, edge.ClassProduction)
	daemon, client := imported(t)
	keepsImagesInContainerd(t, daemon, client)

	runtime := transferRuntime(t, daemon, client)
	coordinate := transferCoordinate(runtime)
	vm.ssh(t, "sudo docker image rm -f "+coordinate+" >/dev/null 2>&1 || true")
	t.Cleanup(func() { vm.ssh(t, "sudo docker image rm -f "+coordinate+" >/dev/null 2>&1 || true") })

	ctx := context.Background()
	store, err := vm.deploying(t).OpenDirectImages(ctx)
	if err != nil {
		t.Fatalf("OpenDirectImages() = %v", err)
	}
	push := transferPush(daemon, client, runtime)

	present, err := store.Has(ctx, push)
	if err != nil {
		t.Fatalf("Has() over a machine that has nothing = %v", err)
	}
	if present {
		t.Fatalf("the machine claims %s before anything moved it, so the transfer cannot be proven here", coordinate)
	}

	plan := provider.ImagePushes{Store: store, Pushes: []provider.ImagePush{push}}
	if err := plan.PushMissing(ctx, nil); err != nil {
		t.Fatalf("PushMissing() onto a machine with no registry account = %v", err)
	}

	named := strings.TrimSpace(vm.sshAs(t, deployLogin, "docker image ls --format '{{.Repository}}:{{.Tag}}' "+coordinate))
	if named != coordinate {
		t.Errorf("the machine's daemon names the moved image %q, want %q: release, rollback and retention pin that coordinate and nothing else",
			named, coordinate)
	}
	if _, err := vm.attempt(deployLogin, "docker image inspect "+coordinate); err != nil {
		t.Errorf("%s cannot reach the image it was handed: %v", deployLogin, err)
	}
}

func TestLiveARedeployOfAnUnchangedAppSendsTheImageNoSecondTime(t *testing.T) {
	vm := liveMachine(t)
	bootstrapped(t, vm, edge.ClassProduction)
	daemon, client := imported(t)
	keepsImagesInContainerd(t, daemon, client)

	runtime := transferRuntime(t, daemon, client)
	coordinate := transferCoordinate(runtime)
	vm.ssh(t, "sudo docker image rm -f "+coordinate+" >/dev/null 2>&1 || true")
	t.Cleanup(func() { vm.ssh(t, "sudo docker image rm -f "+coordinate+" >/dev/null 2>&1 || true") })

	ctx := context.Background()
	store, err := vm.deploying(t).OpenDirectImages(ctx)
	if err != nil {
		t.Fatalf("OpenDirectImages() = %v", err)
	}
	plan := provider.ImagePushes{Store: store, Pushes: []provider.ImagePush{transferPush(daemon, client, runtime)}}
	if err := plan.PushMissing(ctx, nil); err != nil {
		t.Fatalf("PushMissing() = %v", err)
	}

	forget(client)

	rows, err := plan.Rows(ctx)
	if err != nil {
		t.Fatalf("Rows() = %v", err)
	}
	if len(rows) != 1 || rows[0].Action != provider.ActionKeep {
		t.Errorf("the plan shows %v for an image the machine already has, want one %q row", rows, provider.ActionKeep)
	}
	if err := plan.PushMissing(ctx, nil); err != nil {
		t.Fatalf("a second Ship over a machine that already has the digest = %v: the image is gone from this machine's daemon, so the transfer was attempted rather than skipped", err)
	}
	if _, err := vm.attempt(deployLogin, "docker image inspect "+coordinate); err != nil {
		t.Errorf("the redeploy left the machine without the image it already had: %v", err)
	}
}
