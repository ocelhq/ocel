package vps_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/providerkit/provider"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
	vps "github.com/ocelhq/ocel/platform/vps/provider"
	"github.com/ocelhq/ocel/platform/vps/provider/host"
)

const (
	sweepProject = "shop"
	sweepApp     = "sweeper"
	sweepRepo    = "ocel-live-retention"
)

func sweepAt(tag string) string { return sweepRepo + ":" + tag }

func onABoxSweeping(t *testing.T, tags ...string) (machine, *vps.Provider) {
	t.Helper()
	vm, p := onABoxServingContainers(t)
	for _, tag := range tags {
		if strings.TrimSpace(vm.ssh(t, "sudo docker image inspect "+sweepAt(tag)+" >/dev/null 2>&1 && echo present || echo gone")) == "present" {
			continue
		}
		vm.feeds(t, "sudo docker build -q -t "+sweepAt(tag)+" - >/dev/null",
			[]byte("FROM "+fixtureBase+"\nENV RELEASE="+tag+"\n"))
	}
	t.Cleanup(func() {
		vm.ssh(t, "sudo docker images -q --filter reference="+sweepRepo+":* | xargs -r sudo docker rmi -f >/dev/null 2>&1 || true")
		vm.ssh(t, "sudo rm -rf "+host.ReleasesDir()+"/"+sweepProject)
	})
	return vm, p
}

func sweepSpec(t *testing.T, tag string) provider.StackSpec {
	t.Helper()
	stack, err := naming.ParseStackName("prod--sweeper--r0a1b2c3d")
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte(tag))
	return provider.StackSpec{
		Ref:  provider.StackRef{Project: sweepProject, Class: edge.ClassProduction, Name: stack},
		Kind: provider.StackApp,
		App: &provider.AppSpec{
			App:             sweepApp,
			Compute:         provider.ComputeContainer,
			Deployment:      hex.EncodeToString(sum[:])[:32],
			Image:           sweepAt(tag),
			HealthCheckPath: healthPath,
		},
	}
}

func sweepsUp(t *testing.T, p *vps.Provider, tag string) {
	t.Helper()
	if _, err := p.ProvisionContainers(context.Background(), sweepSpec(t, tag), nil); err != nil {
		t.Fatalf("ProvisionContainers(%s) = %v", tag, err)
	}
}

func windowOf(t *testing.T, vm machine, project, app string, class edge.Class) []string {
	t.Helper()
	listed := strings.TrimSpace(vm.sshAs(t, deployLogin,
		"cat "+host.ReleasesDir()+"/"+project+"/"+app+"/"+string(class)))
	if listed == "" {
		return nil
	}
	return strings.Split(listed, "\n")
}

func sweepImages(t *testing.T, vm machine) string {
	t.Helper()
	return strings.TrimSpace(vm.ssh(t,
		"sudo docker images --filter reference="+sweepRepo+":* --format '{{.Repository}}:{{.Tag}}' | sort"))
}

func sweeping(t *testing.T, p *vps.Provider, tag string) {
	t.Helper()
	if err := p.ReconcileImages(context.Background(), sweepSpec(t, tag).Ref, sweepApp, sweepAt(tag), nil); err != nil {
		t.Fatalf("ReconcileImages() = %v", err)
	}
}

func TestLiveTheWindowKeepsThreeAndMovesARepeatedRefToTheHead(t *testing.T) {
	vm, p := onABoxSweeping(t, "r1", "r2", "r3", "r4")

	for _, tag := range []string{"r1", "r2", "r3", "r4"} {
		sweepsUp(t, p, tag)
	}
	window := windowOf(t, vm, sweepProject, sweepApp, edge.ClassProduction)
	want := []string{sweepAt("r4"), sweepAt("r3"), sweepAt("r2")}
	if strings.Join(window, ",") != strings.Join(want, ",") {
		t.Fatalf("the box's window reads %v, want %v: three deep, most recently served first", window, want)
	}

	sweepsUp(t, p, "r2")
	window = windowOf(t, vm, sweepProject, sweepApp, edge.ClassProduction)
	want = []string{sweepAt("r2"), sweepAt("r4"), sweepAt("r3")}
	if strings.Join(window, ",") != strings.Join(want, ",") {
		t.Errorf("the box's window reads %v, want %v: a ref already in the window moves to the head and evicts nothing", window, want)
	}
}

func TestLiveAReconcileNeverTakesTheImageUnderARunningContainer(t *testing.T) {
	vm, p := onABoxSweeping(t, "r1", "served", "orphan")

	sweepsUp(t, p, "r1")
	vm.ssh(t, "sudo docker run -d --name live-retention-served"+
		" --label "+host.LabelApp+"="+sweepApp+
		" --label "+host.LabelProject+"="+sweepProject+
		" --label "+host.LabelRef+"="+sweepAt("served")+
		" "+sweepAt("served"))
	t.Cleanup(func() { vm.ssh(t, "sudo docker rm -f live-retention-served >/dev/null 2>&1 || true") })

	sweeping(t, p, "r1")

	if !strings.Contains(sweepImages(t, vm), sweepAt("served")) {
		t.Errorf("the sweep took %s out from under the container serving it, and the label union is what makes a deploy that dies after the swap survivable", sweepAt("served"))
	}
	if strings.Contains(sweepImages(t, vm), sweepAt("orphan")) {
		t.Errorf("the sweep left %s in place, which no window and no container names", sweepAt("orphan"))
	}
}

func TestLiveASecondReconcileRemovesNothing(t *testing.T) {
	vm, p := onABoxSweeping(t, "r1", "orphan")

	sweepsUp(t, p, "r1")
	sweeping(t, p, "r1")
	swept := sweepImages(t, vm)
	sweeping(t, p, "r1")

	if again := sweepImages(t, vm); again != swept {
		t.Errorf("a second reconcile with no deploy between left %q, want %q: reconcile is a pure function of the window, the running containers and the listing", again, swept)
	}
	if strings.Contains(swept, sweepAt("orphan")) {
		t.Errorf("the first reconcile removed nothing at all, so the second proves nothing: %q", swept)
	}
}

func TestLiveAFailedReleaseSweepsItsOwnImage(t *testing.T) {
	vm, p := onABoxSweeping(t, "r1", "leak")

	sweepsUp(t, p, "r1")

	spec := sweepSpec(t, "leak")
	spec.App.HealthCheckPath = ""
	stacks := p.Stacks()
	_, err := stacks.Provision(context.Background(), spec, nil)
	if err == nil {
		t.Fatal("Provision() of an app with no health path succeeded, and this test needs the failure path")
	}
	if !strings.Contains(err.Error(), "health check path") {
		t.Fatalf("Provision() = %v, which is not the failure this test induces: a release that fell over somewhere earlier proves nothing about the release that leaked an image", err)
	}

	if strings.Contains(sweepImages(t, vm), sweepAt("leak")) {
		t.Errorf("the failed release left %s on the box, and nothing else will ever run: no timer, no cron, no unit sweeps between deploys", sweepAt("leak"))
	}
	if !strings.Contains(sweepImages(t, vm), sweepAt("r1")) {
		t.Errorf("the sweep took the release the box is still serving, and the window is what names it")
	}
}
