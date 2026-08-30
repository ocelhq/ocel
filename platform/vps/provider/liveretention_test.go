package vps_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/providerkit"
	"github.com/ocelhq/ocel/pkg/providerkit/resources"
	vps "github.com/ocelhq/ocel/platform/vps/provider"
	"github.com/ocelhq/ocel/platform/vps/provider/host"
)

const (
	sweepApp  = "sweeper"
	sweepRepo = "ocel-live-retention"
)

func sweepAt(tag string) string { return sweepRepo + ":" + tag }

func onABoxSweeping(t *testing.T, tags ...string) (machine, *vps.Provider) {
	t.Helper()
	vm, p := onABoxServingContainers(t)
	for _, tag := range tags {
		if strings.TrimSpace(vm.ssh(t, "sudo docker image inspect "+sweepAt(tag)+" >/dev/null 2>&1 && echo held || echo gone")) == "held" {
			continue
		}
		vm.feeds(t, "sudo docker build -q -t "+sweepAt(tag)+" - >/dev/null",
			[]byte("FROM "+fixtureBase+"\nENV RELEASE="+tag+"\n"))
	}
	t.Cleanup(func() {
		vm.ssh(t, "sudo docker images -q --filter reference="+sweepRepo+":* | xargs -r sudo docker rmi -f >/dev/null 2>&1 || true")
		vm.ssh(t, "sudo rm -rf "+host.ReleasesDir()+"/"+sweepApp)
	})
	return vm, p
}

func sweepPlan(t *testing.T, tag string) providerkit.StackPlan {
	t.Helper()
	stack, err := naming.ParseStackName("prod--sweeper--r0a1b2c3d")
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte(tag))
	return providerkit.StackPlan{
		Ref:  providerkit.StackRef{Project: "shop", Class: providerkit.ClassProduction, Name: stack},
		Kind: providerkit.StackApp,
		App: &providerkit.AppPlan{
			App:             sweepApp,
			Compute:         providerkit.ComputeContainer,
			Deployment:      hex.EncodeToString(sum[:])[:32],
			Image:           sweepAt(tag),
			HealthCheckPath: healthPath,
		},
	}
}

func sweepsUp(t *testing.T, p *vps.Provider, tag string) {
	t.Helper()
	if _, err := p.ProvisionContainers(context.Background(), sweepPlan(t, tag), nil); err != nil {
		t.Fatalf("ProvisionContainers(%s) = %v", tag, err)
	}
}

func windowOf(t *testing.T, vm machine, app string) []string {
	t.Helper()
	held := strings.TrimSpace(vm.sshAs(t, deployLogin,
		"cat "+host.ReleasesDir()+"/"+app+"/"+string(providerkit.ClassProduction)))
	if held == "" {
		return nil
	}
	return strings.Split(held, "\n")
}

func sweepImages(t *testing.T, vm machine) string {
	t.Helper()
	return strings.TrimSpace(vm.ssh(t,
		"sudo docker images --filter reference="+sweepRepo+":* --format '{{.Repository}}:{{.Tag}}' | sort"))
}

func sweeping(t *testing.T, p *vps.Provider, tag string) {
	t.Helper()
	if err := p.ReconcileImages(context.Background(), sweepPlan(t, tag).Ref, sweepApp, sweepAt(tag), nil); err != nil {
		t.Fatalf("ReconcileImages() = %v", err)
	}
}

func TestLiveTheWindowKeepsThreeAndMovesARepeatedRefToTheHead(t *testing.T) {
	vm, p := onABoxSweeping(t, "r1", "r2", "r3", "r4")

	for _, tag := range []string{"r1", "r2", "r3", "r4"} {
		sweepsUp(t, p, tag)
	}
	held := windowOf(t, vm, sweepApp)
	want := []string{sweepAt("r4"), sweepAt("r3"), sweepAt("r2")}
	if strings.Join(held, ",") != strings.Join(want, ",") {
		t.Fatalf("the box's window reads %v, want %v: three deep, most recently served first", held, want)
	}

	sweepsUp(t, p, "r2")
	held = windowOf(t, vm, sweepApp)
	want = []string{sweepAt("r2"), sweepAt("r4"), sweepAt("r3")}
	if strings.Join(held, ",") != strings.Join(want, ",") {
		t.Errorf("the box's window reads %v, want %v: a ref already held moves to the head and evicts nothing", held, want)
	}
}

func TestLiveAReconcileNeverTakesTheImageUnderARunningContainer(t *testing.T) {
	vm, p := onABoxSweeping(t, "r1", "held", "orphan")

	sweepsUp(t, p, "r1")
	vm.ssh(t, "sudo docker run -d --name live-retention-held"+
		" --label "+host.LabelApp+"="+sweepApp+
		" --label "+host.LabelRef+"="+sweepAt("held")+
		" "+sweepAt("held"))
	t.Cleanup(func() { vm.ssh(t, "sudo docker rm -f live-retention-held >/dev/null 2>&1 || true") })

	sweeping(t, p, "r1")

	if !strings.Contains(sweepImages(t, vm), sweepAt("held")) {
		t.Errorf("the sweep took %s out from under the container serving it, and the label union is what makes a deploy that dies after the swap survivable", sweepAt("held"))
	}
	if strings.Contains(sweepImages(t, vm), sweepAt("orphan")) {
		t.Errorf("the sweep left %s standing, which no window and no container names", sweepAt("orphan"))
	}
}

func TestLiveASecondReconcileRemovesNothing(t *testing.T) {
	vm, p := onABoxSweeping(t, "r1", "orphan")

	sweepsUp(t, p, "r1")
	sweeping(t, p, "r1")
	settled := sweepImages(t, vm)
	sweeping(t, p, "r1")

	if again := sweepImages(t, vm); again != settled {
		t.Errorf("a second reconcile with no deploy between left %q, want %q: reconcile is a pure function of the window, the running containers and the listing", again, settled)
	}
	if strings.Contains(settled, sweepAt("orphan")) {
		t.Errorf("the first reconcile removed nothing at all, so the second proves nothing: %q", settled)
	}
}

func TestLiveAFailedReleaseSweepsItsOwnImage(t *testing.T) {
	vm, p := onABoxSweeping(t, "r1", "leak")

	sweepsUp(t, p, "r1")

	plan := sweepPlan(t, "leak")
	plan.App.HealthCheckPath = ""
	releaser := resources.Releaser(p.Records(), p.Artifacts(), p)
	_, err := releaser.Provision(context.Background(), plan, nil)
	if err == nil {
		t.Fatal("Provision() of an app carrying no health path succeeded, and this test needs the failure path")
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
