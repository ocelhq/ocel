package vps_test

import (
	"archive/tar"
	"bytes"
	"context"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/providerkit"
)

const (
	transferRepository = "ocel/live-transfer"
	transferDigest     = "sha256:1111111111111111111111111111111111111111111111111111111111111111"
	transferTag        = "sha256-1111111111111111111111111111111111111111111111111111111111111111"
)

func transferCoordinate() string { return transferRepository + ":" + transferTag }

func rootfs(t *testing.T) []byte {
	t.Helper()
	var raw bytes.Buffer
	written := tar.NewWriter(&raw)
	body := []byte("the image ocel carried\n")
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

func localDaemon(t *testing.T) (providerkit.DockerHost, *http.Client) {
	t.Helper()
	daemon, err := providerkit.OpenDockerHost()
	if err != nil {
		t.Fatalf("no docker daemon this machine can name, and the image a transfer carries is read out of one: %v", err)
	}
	transport := daemon.Transport()
	t.Cleanup(transport.CloseIdleConnections)
	return daemon, &http.Client{Transport: transport}
}

func imported(t *testing.T) *http.Client {
	t.Helper()
	daemon, client := localDaemon(t)

	query := url.Values{"fromSrc": {"-"}, "repo": {transferRepository}, "tag": {transferTag}}
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
		t.Fatalf("the daemon at %s answered %q importing %s", daemon.Address, resp.Status, transferCoordinate())
	}
	if _, err := io.Copy(io.Discard, resp.Body); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { forget(client) })
	return client
}

func forget(client *http.Client) {
	req, err := http.NewRequestWithContext(context.Background(), http.MethodDelete,
		"http://docker/images/"+transferCoordinate()+"?force=1", nil)
	if err != nil {
		return
	}
	resp, err := client.Do(req)
	if err != nil {
		return
	}
	_ = resp.Body.Close()
}

func transferPush() providerkit.ImagePush {
	return providerkit.ImagePush{
		App:    "live-transfer",
		Source: transferRepository + "@" + transferDigest,
		Target: transferCoordinate(),
		Digest: transferDigest,
	}
}

func TestLiveAnImageIsCarriedOntoTheMachineUnderTheCoordinateItWasBuiltAs(t *testing.T) {
	vm := live(t)
	bootstrapped(t, vm, providerkit.ClassProduction)
	imported(t)

	coordinate := transferCoordinate()
	vm.ssh(t, "sudo docker image rm -f "+coordinate+" >/dev/null 2>&1 || true")
	t.Cleanup(func() { vm.ssh(t, "sudo docker image rm -f "+coordinate+" >/dev/null 2>&1 || true") })

	ctx := context.Background()
	store, err := vm.deploying(t).DirectImages(ctx)
	if err != nil {
		t.Fatalf("DirectImages() = %v", err)
	}
	push := transferPush()

	held, err := store.Has(ctx, push)
	if err != nil {
		t.Fatalf("Has() over a machine that holds nothing = %v", err)
	}
	if held {
		t.Fatalf("the machine claims %s before anything carried it, so the transfer cannot be proven here", coordinate)
	}

	plan := providerkit.ImagePlan{Store: store, Pushes: []providerkit.ImagePush{push}}
	if err := plan.Ship(ctx, nil); err != nil {
		t.Fatalf("Ship() onto a machine with no registry account = %v", err)
	}

	named := strings.TrimSpace(vm.sshAs(t, deployLogin, "docker image ls --format '{{.Repository}}:{{.Tag}}' "+coordinate))
	if named != coordinate {
		t.Errorf("the machine's daemon names the carried image %q, want %q: release, rollback and retention pin that coordinate and nothing else",
			named, coordinate)
	}
	if _, err := vm.attempt(deployLogin, "docker image inspect "+coordinate); err != nil {
		t.Errorf("%s cannot reach the image it was handed: %v", deployLogin, err)
	}
}

func TestLiveARedeployOfAnUnchangedAppCarriesTheImageNoSecondTime(t *testing.T) {
	vm := live(t)
	bootstrapped(t, vm, providerkit.ClassProduction)
	client := imported(t)

	coordinate := transferCoordinate()
	vm.ssh(t, "sudo docker image rm -f "+coordinate+" >/dev/null 2>&1 || true")
	t.Cleanup(func() { vm.ssh(t, "sudo docker image rm -f "+coordinate+" >/dev/null 2>&1 || true") })

	ctx := context.Background()
	store, err := vm.deploying(t).DirectImages(ctx)
	if err != nil {
		t.Fatalf("DirectImages() = %v", err)
	}
	plan := providerkit.ImagePlan{Store: store, Pushes: []providerkit.ImagePush{transferPush()}}
	if err := plan.Ship(ctx, nil); err != nil {
		t.Fatalf("Ship() = %v", err)
	}

	forget(client)

	rows, err := plan.Rows(ctx)
	if err != nil {
		t.Fatalf("Rows() = %v", err)
	}
	if len(rows) != 1 || rows[0].Action != providerkit.ActionKeep {
		t.Errorf("the plan shows %v for an image the machine already holds, want one %q row", rows, providerkit.ActionKeep)
	}
	if err := plan.Ship(ctx, nil); err != nil {
		t.Fatalf("a second Ship over a machine that already holds the digest = %v: the image is gone from this machine's daemon, so the transfer was attempted rather than skipped", err)
	}
	if _, err := vm.attempt(deployLogin, "docker image inspect "+coordinate); err != nil {
		t.Errorf("the redeploy left the machine without the image it already had: %v", err)
	}
}
