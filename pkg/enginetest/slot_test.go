//go:build unix

package enginetest

import (
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/ocelhq/ocel/pkg/images"
)

const heldSlotDir = "OCEL_ENGINETEST_HOLD_SLOT_IN"

func leased(t *testing.T, dir string, count int) (int, *os.File) {
	t.Helper()
	slot, held, err := leaseSlot(dir, count)
	if err != nil {
		t.Fatalf("leaseSlot = %v", err)
	}
	return slot, held
}

func TestARunLeasesTheFirstFreeSlotAndALaterRunTakesItBack(t *testing.T) {
	dir := t.TempDir()

	first, held := leased(t, dir, 3)
	second, other := leased(t, dir, 3)
	defer other.Close()
	if first != 0 || second != 1 {
		t.Fatalf("two runs at once leased slots %d and %d, want 0 and 1: runs that share a slot share its networks and resolve each other's switchboard", first, second)
	}
	held.Close()
	if again, held := leased(t, dir, 3); again != 0 {
		t.Errorf("a run after the first ended leased slot %d, want 0: every slot a later run skips is another set of networks left on the machine", again)
	} else {
		held.Close()
	}
}

func TestAChildHoldsASlotAndDies(t *testing.T) {
	dir := os.Getenv(heldSlotDir)
	if dir == "" {
		t.Skip("run only as the child of the test that kills it")
	}
	if _, _, err := leaseSlot(dir, 1); err != nil {
		t.Fatal(err)
	}
	os.Exit(0)
}

func TestASlotIsFreedWhenTheRunHoldingItDies(t *testing.T) {
	dir := t.TempDir()
	child := exec.Command(os.Args[0], "-test.run=^TestAChildHoldsASlotAndDies$")
	child.Env = append(os.Environ(), heldSlotDir+"="+dir)
	if said, err := child.CombinedOutput(); err != nil {
		t.Fatalf("the child that holds the slot: %v\n%s", err, said)
	}

	got := make(chan int, 1)
	go func() {
		slot, held, err := leaseSlot(dir, 1)
		if err == nil {
			defer held.Close()
		}
		got <- slot
	}()
	select {
	case <-got:
	case <-time.After(5 * time.Second):
		t.Fatal("the only slot was still held after the run holding it died: a killed run would then keep its networks from every run after it")
	}
}

func TestWhenEverySlotIsHeldTheNextRunWaitsForOne(t *testing.T) {
	dir := t.TempDir()
	_, held := leased(t, dir, 1)

	got := make(chan error, 1)
	go func() {
		_, other, err := leaseSlot(dir, 1)
		if err == nil {
			defer other.Close()
		}
		got <- err
	}()
	select {
	case err := <-got:
		t.Fatalf("a run leased a slot another run holds (err = %v): two runs would share the slot's networks", err)
	case <-time.After(300 * time.Millisecond):
	}
	held.Close()
	select {
	case err := <-got:
		if err != nil {
			t.Fatalf("the waiting run's lease = %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the waiting run never leased the slot the other run gave back")
	}
}

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
