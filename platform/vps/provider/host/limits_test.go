package host

import (
	"context"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/platform/vps/provider/docker"
	"github.com/ocelhq/ocel/platform/vps/provider/shell"
	"github.com/ocelhq/ocel/platform/vps/provider/ssh"
)

const (
	benchMemoryBytes = 8 << 30
	benchCPUs        = 4
)

func flagOf(command, flag string) (string, bool) {
	fields := strings.Fields(command)
	at := slices.Index(fields, shell.Quote(flag))
	if at < 0 || at+1 >= len(fields) {
		return "", false
	}
	return strings.Trim(fields[at+1], "'"), true
}

func runOnBench(t *testing.T, box *bench) string {
	t.Helper()
	for _, command := range box.commands() {
		if strings.Contains(command, shell.Quote("docker")+" "+shell.Quote("run")) {
			return command
		}
	}
	t.Fatalf("nothing was run:\n%s", strings.Join(box.commands(), "\n"))
	return ""
}

func TestAProductionAppMayUseHalfTheBoxsMemoryAndEveryCPU(t *testing.T) {
	t.Parallel()

	box := machine(nil)
	if _, err := box.host().RunContainer(context.Background(), valued()); err != nil {
		t.Fatalf("RunContainer() = %v", err)
	}
	run := runOnBench(t, box)
	if memory, _ := flagOf(run, "--memory"); memory != strconv.Itoa(benchMemoryBytes/2) {
		t.Errorf("a production app runs with --memory %q on a box of %d bytes, want half of it, so one runaway app cannot take the memory everything else on the box needs", memory, benchMemoryBytes)
	}
	if cpus, capped := flagOf(run, "--cpus"); capped {
		t.Errorf("a production app is capped at %s CPUs, and nothing on the box outranks it", cpus)
	}
}

func TestAPreviewAppMayUseAQuarterOfTheBoxsMemoryAndHalfItsCPUs(t *testing.T) {
	t.Parallel()

	spec := valued()
	spec.Tier = environment.TierPreview
	box := machine(nil)
	if _, err := box.host().RunContainer(context.Background(), spec); err != nil {
		t.Fatalf("RunContainer() = %v", err)
	}
	run := runOnBench(t, box)
	if memory, _ := flagOf(run, "--memory"); memory != strconv.Itoa(benchMemoryBytes/4) {
		t.Errorf("a preview app runs with --memory %q on a box of %d bytes, want a quarter of it, so a preview cannot starve production of memory", memory, benchMemoryBytes)
	}
	if cpus, _ := flagOf(run, "--cpus"); cpus != strconv.Itoa(benchCPUs/2) {
		t.Errorf("a preview app runs with --cpus %q on a box of %d CPUs, want half of them", cpus, benchCPUs)
	}
}

func TestAResourceWithNoMemoryOfItsOwnIsLimitedByItsTiersShareOfTheBox(t *testing.T) {
	t.Parallel()

	box := machine(nil)
	if err := box.host().RunResource(context.Background(), resourced(), "secret"); err != nil {
		t.Fatalf("RunResource() = %v", err)
	}
	if memory, _ := flagOf(runOnBench(t, box), "--memory"); memory != strconv.Itoa(benchMemoryBytes/2) {
		t.Errorf("a production resource runs with --memory %q, want half the box", memory)
	}

	spec := resourced()
	spec.Memory = "512m"
	box = machine(nil)
	if err := box.host().RunResource(context.Background(), spec, "secret"); err != nil {
		t.Fatalf("RunResource() = %v", err)
	}
	if memory, _ := flagOf(runOnBench(t, box), "--memory"); memory != "512m" {
		t.Errorf("a resource whose transform set its memory runs with --memory %q, want the memory it asked for", memory)
	}
}

func TestABoxWhoseSizeCannotBeReadRunsNothingUnlimited(t *testing.T) {
	t.Parallel()

	box := machine(nil)
	box.answer = func(command string) (ssh.Result, bool) {
		if strings.Contains(command, docker.SizeFields) {
			return ssh.Result{Stderr: "Cannot connect to the Docker daemon", Code: 1}, true
		}
		return ssh.Result{}, false
	}
	if _, err := box.host().RunContainer(context.Background(), valued()); err == nil {
		t.Fatal("RunContainer() = nil on a box whose size could not be read, so the app ran with no memory limit")
	}
}
