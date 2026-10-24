//go:build integration && unix

package enginetest

import (
	"os/exec"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/images"
)

func TestARunsNetworksOutliveItAndTheNextRunFindsThemEmpty(t *testing.T) {
	box := Network(t, "box")
	if again := Network(t, "box"); again != box {
		t.Errorf("a second test of the run was handed network %s, want %s", again, box)
	}
	if box != slotNetworkName(lease.slot, "box") {
		t.Errorf("the run was handed network %s, want the slot's own %s: a network named for the run is created and removed by every run, and each one is a bridge whose address appearing and going Chrome reads as the network changing", box, slotNetworkName(lease.slot, "box"))
	}
	labels, err := exec.Command(engine, "network", "inspect", "--format", `{{json .Labels}}`, box).Output()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(labels), runLabel) {
		t.Errorf("the slot's network carries %s, so the run's own sweep removes it: %s", runLabel, labels)
	}

	left := uniqueName("left")
	t.Cleanup(func() { exec.Command(engine, "rm", "--force", left).Run() })
	if said, err := exec.Command(engine, "run", "--detach", "--name", left, "--network", box,
		"--entrypoint", "sleep", images.ObjectStore(), "600").CombinedOutput(); err != nil {
		t.Fatalf("plant what a killed run left on the slot's network: %v\n%s", err, said)
	}
	if err := emptySlot(lease.slot); err != nil {
		t.Fatalf("emptySlot = %v", err)
	}
	if exec.Command(engine, "container", "inspect", left).Run() == nil {
		t.Errorf("%s, which a killed run left on the slot's network, is still there after the slot was leased again: the next run's switchboard alias would resolve to it", left)
	}
	if exec.Command(engine, "network", "inspect", box).Run() != nil {
		t.Errorf("emptying the slot removed its network %s, and the next run creates it again", box)
	}
}

func TestASlotNetworkPrunedBetweenTestsIsThereForTheNext(t *testing.T) {
	tunnel := Network(t, "tunnel")
	if err := docker("network", "rm", tunnel); err != nil {
		t.Fatalf("remove the slot's network the way `docker network prune` takes an idle one: %v", err)
	}
	if again := Network(t, "tunnel"); again != tunnel || exec.Command(engine, "network", "inspect", tunnel).Run() != nil {
		t.Errorf("the next test was handed %s after the slot's idle network was pruned, and it is not there: every container the test starts on it fails", again)
	}
}

func TestASlotNetworkSomethingElseCreatedIsEmptiedWithTheRest(t *testing.T) {
	box := Network(t, "box")
	if err := docker("network", "rm", box); err != nil {
		t.Fatal(err)
	}
	if err := docker("network", "create", box); err != nil {
		t.Fatalf("create the slot's network unlabelled, the way the box's own write does once a prune took it: %v", err)
	}
	left := uniqueName("left")
	t.Cleanup(func() { exec.Command(engine, "rm", "--force", left).Run() })
	if said, err := exec.Command(engine, "run", "--detach", "--name", left, "--network", box,
		"--entrypoint", "sleep", images.ObjectStore(), "600").CombinedOutput(); err != nil {
		t.Fatalf("plant what a killed run left: %v\n%s", err, said)
	}
	if err := emptySlot(lease.slot); err != nil {
		t.Fatalf("emptySlot = %v", err)
	}
	if exec.Command(engine, "container", "inspect", left).Run() == nil {
		t.Errorf("%s is still on %s after the slot was emptied: a slot network the box's write created carries no slot label, and what is on it outlives every run", left, box)
	}
}
