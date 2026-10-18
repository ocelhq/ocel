package host

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/platform/vps/provider/machine"
	"github.com/ocelhq/ocel/platform/vps/provider/ssh"
)

func TestATierDestroyTakesTheVolumesItsResourcesKeptTheirDataOn(t *testing.T) {
	t.Parallel()

	empty := Reading{Arch: ssh.AMD64, Tier: environment.TierProduction, Observed: map[string]string{}}
	beside := Reading{Arch: ssh.AMD64, Tier: environment.TierPreview, Observed: map[string]string{}}
	taken := removing(empty, beside, appsPresent{containers: true, volumes: true})

	containers, volumes := -1, -1
	for at, removal := range taken {
		switch removal.Kind {
		case machine.KindApps:
			containers = at
		case machine.KindResourceVolumes:
			volumes = at
		}
	}
	if volumes < 0 {
		t.Fatalf("the destroy plans %v and leaves every resource's data on the box, where nothing after it reclaims the disk", taken)
	}
	if containers < 0 || volumes < containers {
		t.Errorf("the volumes are taken at %d and the containers at %d, and the engine refuses a volume a container still mounts", volumes, containers)
	}
	command := taken[volumes].Command()
	if !strings.Contains(command, "docker volume rm") || !strings.Contains(command, "label=ocel.tier=production") {
		t.Errorf("the volumes are taken by %q, which is bounded by no tier", command)
	}
}

func TestATierProbeAsksForVolumesUnderItsOwnLabel(t *testing.T) {
	t.Parallel()

	probe := appsProbe(environment.TierPreview)
	if !strings.Contains(probe, "docker volume ls") || !strings.Contains(probe, "label=ocel.tier=preview") {
		t.Errorf("the probe reads %q and never learns whether preview kept a volume", probe)
	}
}

func TestATierDestroyRemovesTheVolumesItPlannedToAfterTheContainersThatMountThem(t *testing.T) {
	t.Parallel()

	tier := environment.TierProduction
	box := newBench(map[environment.Tier][]machine.Item{tier: bootstrapped(t, tier)})
	box.answer = func(command string) (ssh.Result, bool) {
		if strings.Contains(command, "echo volumes") {
			return ssh.Result{Stdout: "containers\nvolumes\n"}, true
		}
		return ssh.Result{}, false
	}
	progress := &said{}
	if err := NewBootstrap(box.host(), testVendor, "shop").Remove(context.Background(), tier, progress); err != nil {
		t.Fatalf("Remove() = %v, and a tier that kept a volume can never be destroyed", err)
	}
	if taken := "Removed the resource volumes labelled " + tierSelector(tier); !slices.Contains(progress.lines, taken) {
		t.Errorf("Remove() never said %q:\n%s", taken, strings.Join(progress.lines, "\n"))
	}
	containers := box.at("xargs -r docker rm --force")
	volumes := box.at("xargs -r docker volume rm")
	if containers < 0 || volumes < containers {
		t.Errorf("the containers went at %d and the volumes at %d, and the engine refuses a volume a container still mounts", containers, volumes)
	}
}
