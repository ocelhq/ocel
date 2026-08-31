package vps_test

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/providerkit"
	vps "github.com/ocelhq/ocel/platform/vps/provider"
	"github.com/ocelhq/ocel/platform/vps/provider/host"
)

const foreignContainer = "not-ocels"

func liveDeployPreflight(t *testing.T, image string) providerkit.DeployPreflight {
	t.Helper()
	stack, err := naming.ParseStackName("prod--web--r0a1b2c3d")
	if err != nil {
		t.Fatal(err)
	}
	return providerkit.DeployPreflight{
		Plan: providerkit.DeployPlan{
			Slug:  "shop",
			Class: providerkit.ClassProduction,
			Apps: []providerkit.AppEntry{{
				App:             liveApp,
				Stack:           stack,
				Image:           image,
				HealthCheckPath: healthPath,
			}},
		},
	}
}

func preflightedOn(t *testing.T, p *vps.Provider) error {
	t.Helper()
	return preflightedFor(t, p, fixtureAt("one"))
}

func preflightedFor(t *testing.T, p *vps.Provider, image string) error {
	t.Helper()
	return p.PreflightDeploy(context.Background(), liveDeployPreflight(t, image))
}

func TestLiveAStandingBoxIsLetThroughAndAnEngineThatDoesNotAnswerIsNot(t *testing.T) {
	vm, p := onABoxServingContainers(t)

	if err := preflightedOn(t, p); err != nil {
		t.Fatalf("PreflightDeploy() on a bootstrapped box = %v, want it let through", err)
	}

	vm.ssh(t, "sudo systemctl stop docker.socket docker.service")
	defer func() {
		vm.ssh(t, "sudo systemctl start docker.socket docker.service")
		for range 60 {
			if strings.TrimSpace(vm.ssh(t, "sudo docker version >/dev/null 2>&1 && echo up || echo down")) == "up" {
				break
			}
			time.Sleep(time.Second)
		}
		vm.waitsFor(t, host.ProxyContainer)
	}()

	err := preflightedOn(t, vm.deploying(t))
	if err == nil {
		t.Fatal("PreflightDeploy() let a deploy past an engine that is not running, and it would surface as a failed image load halfway through the transfer")
	}
	if !strings.Contains(err.Error(), "docker") {
		t.Errorf("PreflightDeploy() = %q, want the engine named", err)
	}
	if !strings.Contains(err.Error(), "then run this again") {
		t.Errorf("PreflightDeploy() = %q, want the remedy named: a refusal an operator cannot act on is a wall", err)
	}
}

func (vm machine) waitsFor(t *testing.T, container string) {
	t.Helper()
	for range 60 {
		if vm.running(t, container) {
			return
		}
		time.Sleep(time.Second)
	}
	t.Fatalf("%s never came back up:\n%s", container, vm.ssh(t, "sudo docker logs --tail 20 "+container+" 2>&1 || true"))
}

const ballast = "/var/lib/ocel-preflight-ballast"

func (vm machine) dockerRoot(t *testing.T) string {
	t.Helper()
	root := strings.TrimSpace(vm.ssh(t, "sudo docker info --format '{{.DockerRootDir}}'"))
	if root == "" {
		t.Fatal("this box named no docker data root, so there is no filesystem to fill")
	}
	return root
}

func (vm machine) freeOn(t *testing.T, root string) int64 {
	t.Helper()
	free, err := strconv.ParseInt(strings.TrimSpace(vm.ssh(t, "df -Pk "+quote(root)+" | tail -n 1 | awk '{print $4}'")), 10, 64)
	if err != nil {
		t.Fatalf("read what is free on %s: %v", root, err)
	}
	return free * 1024
}

func (vm machine) fillsDownTo(t *testing.T, root string, keep int64) {
	t.Helper()
	free := vm.freeOn(t, root)
	if free <= keep {
		t.Skipf("%s has only %d bytes free, and this test fills a disk it must be able to leave %d on", root, free, keep)
	}
	vm.ssh(t, "sudo fallocate -l "+strconv.FormatInt((free-keep)/1024, 10)+"KiB "+quote(ballast))
	t.Cleanup(func() { vm.ssh(t, "sudo rm -f "+quote(ballast)) })
}

func TestLiveADiskWithoutRoomForTheKeepWindowRefusesAndNamesTheGuess(t *testing.T) {
	vm, p := onABoxServingContainers(t)

	root := vm.dockerRoot(t)
	keep := int64(400 * 1024 * 1024)
	vm.fillsDownTo(t, root, keep)

	err := preflightedOn(t, p)
	if err == nil {
		t.Fatalf("PreflightDeploy() let a deploy onto %s with %d bytes left, and a disk that fills while an image streams fails mid-transfer", root, keep)
	}
	said := err.Error()
	for _, want := range []string{root, "guessed constant", "Free space on " + root, "then run this again"} {
		if !strings.Contains(said, want) {
			t.Errorf("PreflightDeploy() = %q, want %q in it", said, want)
		}
	}
}

const (
	bulkRepo  = "ocel-live-bulk"
	bulkImage = 900 * 1024 * 1024
)

func TestLiveADiskThatClearsTheFloorIsStillRefusedByWhatThisBoxHolds(t *testing.T) {
	vm, p := onABoxServingContainers(t)

	root := vm.dockerRoot(t)
	vm.ssh(t, "sudo docker rmi -f "+bulkRepo+":one >/dev/null 2>&1 || true")
	t.Cleanup(func() { vm.ssh(t, "sudo docker rmi -f "+bulkRepo+":one >/dev/null 2>&1 || true") })
	vm.ssh(t, "sudo sh -c "+quote("head -c "+strconv.FormatInt(bulkImage, 10)+" /dev/zero > /tmp/bulk && "+
		"tar -cf - -C /tmp bulk | docker import - "+bulkRepo+":one >/dev/null; rm -f /tmp/bulk"))

	room, err := p.Host().Headroom(context.Background(), []string{bulkRepo})
	if err != nil {
		t.Fatalf("Headroom(%s) = %v", bulkRepo, err)
	}
	held := room.Repos[bulkRepo]
	if held.Count != 1 {
		t.Fatalf("%s holds %d image(s), want the one this test put there: an unfilled keep window is what the measurement extrapolates over", bulkRepo, held.Count)
	}
	if held.Measured() <= host.FirstDeployFloor {
		t.Fatalf("the one image held under %s is %d bytes, so its keep window measures %d, which the floor %d already covers and this box proves nothing the floor does not",
			bulkRepo, held.Largest, held.Measured(), int64(host.FirstDeployFloor))
	}

	keep := (int64(host.FirstDeployFloor) + held.Measured()) / 2
	vm.fillsDownTo(t, root, keep)

	err = preflightedFor(t, p, bulkRepo+":next")
	if err == nil {
		t.Fatalf("PreflightDeploy() let a deploy onto %s with %d bytes left against a box holding a %d byte image, and the keep window has %d unfilled slot(s) to fill with images that size",
			root, keep, held.Largest, host.KeepWindow-1)
	}
	if free := vm.freeOn(t, root); free <= host.FirstDeployFloor {
		t.Fatalf("%s was left with %d bytes free, under the %d floor, so this refusal is the floor's and the measurement was never the number applied",
			root, free, int64(host.FirstDeployFloor))
	}
	said := err.Error()
	for _, want := range []string{"image(s) held under " + bulkRepo, "unfilled slot(s) plus the incoming one measures", "Free space on " + root, "then run this again"} {
		if !strings.Contains(said, want) {
			t.Errorf("PreflightDeploy() = %q, want %q in it", said, want)
		}
	}
}

func TestLiveTheFourProxyStatesAreFourInducedConditionsAndFourMessages(t *testing.T) {
	vm, p := onABoxServingContainers(t)

	if err := preflightedOn(t, p); err != nil {
		t.Fatalf("PreflightDeploy() over a standing proxy = %v, and the answered state is the control the other three are read against", err)
	}

	held := map[string]string{}
	for _, induced := range []struct {
		what    string
		induce  func()
		restore func()
		wants   []string
	}{
		{
			what:    "the proxy container is stopped",
			induce:  func() { vm.ssh(t, "sudo docker stop "+host.ProxyContainer) },
			restore: func() { vm.ssh(t, "sudo docker start "+host.ProxyContainer); vm.waitsFor(t, host.ProxyContainer) },
			wants:   []string{"exited"},
		},
		{
			what:    "the flip helper is not executable at its bootstrap path",
			induce:  func() { vm.ssh(t, "sudo chmod 000 "+quote(host.ProxyHelper)) },
			restore: func() { vm.ssh(t, "sudo chmod 750 "+quote(host.ProxyHelper)) },
			wants:   []string{host.ProxyHelperMount, "bootstrap"},
		},
		{
			what: "the admin socket is not there",
			induce: func() {
				vm.ssh(t, "sudo docker exec "+host.ProxyContainer+" mv "+quote(host.ProxyAdminSocket)+" "+quote(host.ProxyAdminSocket+".moved"))
			},
			restore: func() {
				vm.ssh(t, "sudo docker exec "+host.ProxyContainer+" mv "+quote(host.ProxyAdminSocket+".moved")+" "+quote(host.ProxyAdminSocket))
			},
			wants: []string{"no socket at " + host.ProxyAdminSocket},
		},
		{
			what:    "the admin socket is there and nothing is listening on it",
			induce:  func() { vm.deafens(t) },
			restore: func() { vm.hears(t) },
			wants:   []string{"refused the one read"},
		},
	} {
		func() {
			induced.induce()
			defer induced.restore()
			err := preflightedOn(t, vm.deploying(t))
			if err == nil {
				t.Fatalf("PreflightDeploy() let a deploy past a box where %s, and a deploy into a proxy that cannot be flipped is a green deploy nothing routes to", induced.what)
			}
			said := err.Error()
			for _, want := range append(induced.wants, "then run this again") {
				if !strings.Contains(said, want) {
					t.Errorf("where %s the refusal is %q, want %q in it: a refusal an operator cannot act on is a wall", induced.what, said, want)
				}
			}
			for what, other := range held {
				if other == said {
					t.Errorf("%q and %q are refused with the same words, and their fixes differ:\n%s", induced.what, what, said)
				}
			}
			held[induced.what] = said
		}()
	}
	if len(held) != 4 {
		t.Fatalf("%d proxy states were induced, want the four this check tells apart", len(held))
	}
	if err := preflightedOn(t, vm.deploying(t)); err != nil {
		t.Fatalf("PreflightDeploy() after every induced state was undone = %v, so the four messages above were read off a box left broken", err)
	}
}

const deafSocket = host.ProxyAdminSocket + ".listening"

const deafPid = "/run/ocel-live-deafened.pid"

const deafScript = "import socket,time; s=socket.socket(socket.AF_UNIX); s.bind(%q); time.sleep(300)"

func (vm machine) deafens(t *testing.T) {
	t.Helper()
	if strings.TrimSpace(vm.ssh(t, "command -v python3 >/dev/null && echo held || echo gone")) != "held" {
		t.Skip("this box carries no python3, and a socket that is bound and not listening is what an admin endpoint that refuses looks like")
	}
	pid := strings.TrimSpace(vm.ssh(t, "sudo docker inspect -f '{{.State.Pid}}' "+host.ProxyContainer))
	if pid == "" {
		t.Fatalf("%s named no pid, so its filesystem cannot be reached from this host", host.ProxyContainer)
	}
	vm.ssh(t, "sudo docker exec "+host.ProxyContainer+" mv "+quote(host.ProxyAdminSocket)+" "+quote(deafSocket))
	vm.ssh(t, "sudo sh -c "+quote(
		"python3 -c "+quote(fmt.Sprintf(deafScript, "/proc/"+pid+"/root"+host.ProxyAdminSocket))+" >/dev/null 2>&1 </dev/null & echo $!")+
		" | sudo tee "+quote(deafPid)+" >/dev/null")
	for range 40 {
		if strings.TrimSpace(vm.ssh(t, "sudo docker exec "+host.ProxyContainer+" sh -c "+quote("test -S "+host.ProxyAdminSocket+" && echo bound || echo gone"))) == "bound" {
			return
		}
		time.Sleep(250 * time.Millisecond)
	}
	vm.hears(t)
	t.Fatalf("a socket bound and not listening never appeared at %s, and this induction proves nothing without one: a run that reports green having induced nothing is the shape this tier exists to rule out", host.ProxyAdminSocket)
}

func (vm machine) hears(t *testing.T) {
	t.Helper()
	vm.ssh(t, "sudo sh -c "+quote("kill $(cat "+deafPid+") || true; rm -f "+deafPid))
	vm.ssh(t, "sudo docker exec "+host.ProxyContainer+" sh -c "+quote(
		"rm -f "+host.ProxyAdminSocket+"; mv "+deafSocket+" "+host.ProxyAdminSocket))
}

func TestLiveAForeignContainerHoldingPortEightyIsRefusedByName(t *testing.T) {
	vm, p := onABoxServingContainers(t)

	vm.ssh(t, "sudo docker stop "+host.ProxyContainer)
	vm.ssh(t, "sudo docker rm -f "+foreignContainer+" >/dev/null 2>&1 || true")
	vm.ssh(t, "sudo docker run -d --name "+foreignContainer+" -p "+host.RenewalPort+":8080 "+fixtureAt("one"))
	defer func() {
		vm.ssh(t, "sudo docker rm -f "+foreignContainer+" >/dev/null 2>&1 || true")
		vm.ssh(t, "sudo docker start "+host.ProxyContainer)
		vm.waitsFor(t, host.ProxyContainer)
	}()
	if !vm.running(t, foreignContainer) {
		t.Fatalf("%s never came up, so nothing on this box is holding port %s and there is no condition to refuse", foreignContainer, host.RenewalPort)
	}

	err := preflightedOn(t, p)
	if err == nil {
		t.Fatalf("PreflightDeploy() let a deploy onto a box where %s holds port %s", foreignContainer, host.RenewalPort)
	}
	if !strings.Contains(err.Error(), foreignContainer) {
		t.Errorf("PreflightDeploy() = %q, want %q named: a foreign listener is refused by name", err, foreignContainer)
	}
	for _, want := range []string{"stop " + foreignContainer, "move it off " + host.RenewalPort, "then run this again"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("PreflightDeploy() = %q, want %q in it: a refusal an operator cannot act on is a wall", err, want)
		}
	}
}
