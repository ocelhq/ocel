package host

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/providerkit"
	"github.com/ocelhq/ocel/platform/vps/provider/session"
)

func TestAClassDestroyTakesTheVolumesItsResourcesKeptTheirDataOn(t *testing.T) {
	t.Parallel()

	empty := Reading{Arch: ArchAMD64, Class: providerkit.ClassProduction, Observed: map[string]string{}}
	beside := Reading{Arch: ArchAMD64, Class: providerkit.ClassPreview, Observed: map[string]string{}}
	taken := removing(empty, beside, appsStanding{containers: true, volumes: true})

	containers, volumes := -1, -1
	for at, removal := range taken {
		switch removal.kind {
		case KindApps:
			containers = at
		case KindResourceVolumes:
			volumes = at
		}
	}
	if volumes < 0 {
		t.Fatalf("the destroy plans %v and leaves every resource's data on the box, where nothing after it reclaims the disk", taken)
	}
	if containers < 0 || volumes < containers {
		t.Errorf("the volumes are taken at %d and the containers at %d, and the engine refuses a volume a container still mounts", volumes, containers)
	}
	command := taken[volumes].command()
	if !strings.Contains(command, "docker volume rm") || !strings.Contains(command, "label=ocel.class=production") {
		t.Errorf("the volumes are taken by %q, which is bounded by no class", command)
	}
}

func TestAClassProbeAsksForVolumesUnderItsOwnLabel(t *testing.T) {
	t.Parallel()

	probe := appsProbe(providerkit.ClassPreview)
	if !strings.Contains(probe, "docker volume ls") || !strings.Contains(probe, "label=ocel.class=preview") {
		t.Errorf("the probe reads %q and never learns whether preview kept a volume", probe)
	}
}

func TestAClassDestroyRemovesTheVolumesItPlannedToAfterTheContainersThatMountThem(t *testing.T) {
	t.Parallel()

	class := providerkit.ClassProduction
	stood := machine(map[providerkit.Class][]Item{class: bootstrapped(t, class)})
	stood.answer = func(command string) (session.Result, bool) {
		if strings.Contains(command, "echo volumes") {
			return session.Result{Stdout: "containers\nvolumes\n"}, true
		}
		return session.Result{}, false
	}
	report := &said{}
	if err := Bootstrap(stood.host(), testVendor, "shop").Remove(context.Background(), class, report); err != nil {
		t.Fatalf("Remove() = %v, and a class that kept a volume can never be destroyed", err)
	}
	if taken := "removed " + KindResourceVolumes + " " + classSelector(class); !slices.Contains(report.lines, taken) {
		t.Errorf("Remove() never said %q:\n%s", taken, strings.Join(report.lines, "\n"))
	}
	containers := stood.at("xargs -r docker rm --force")
	volumes := stood.at("xargs -r docker volume rm")
	if containers < 0 || volumes < containers {
		t.Errorf("the containers went at %d and the volumes at %d, and the engine refuses a volume a container still mounts", containers, volumes)
	}
}
