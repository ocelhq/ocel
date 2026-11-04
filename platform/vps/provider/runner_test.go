package vps

import (
	"context"
	"testing"

	"github.com/ocelhq/ocel/pkg/progress"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/platform/vps/provider/boxapi"
	"github.com/ocelhq/ocel/platform/vps/provider/host"
	"github.com/ocelhq/ocel/platform/vps/provider/machine"
	"github.com/ocelhq/ocel/platform/vps/provider/ssh"
)

type boxRunner struct {
	present bool
	applied []host.RunnerSettings
	removed int
}

func (b *boxRunner) Read(context.Context, host.RunnerSettings) (host.RunnerReading, error) {
	observed := map[string]string{}
	if b.present {
		observed[machine.KindFile+" "+boxapi.RunnerConfigPath] = "written"
	}
	return host.RunnerReading{Observed: observed, CPUs: 4, MemoryKB: 8 << 20}, nil
}

func (b *boxRunner) Apply(_ context.Context, settings host.RunnerSettings, _ host.RunnerReading, _ progress.Log) error {
	b.applied, b.present = append(b.applied, settings), true
	return nil
}

func (b *boxRunner) Remove(context.Context, progress.Log) error {
	b.removed++
	b.present = false
	return nil
}

func jobsOnBox() (connectorJobs, *boxRunner) {
	runner := &boxRunner{}
	return connectorJobs{runner: runner, arch: func(context.Context) (string, error) { return ssh.ARM64, nil }}, runner
}

func TestAConnectorAddedAsARunnerGetsTheRunnerWithItsEndpointAndCaps(t *testing.T) {
	jobs, runner := jobsOnBox()
	if err := jobs.Reconcile(context.Background(), "box.example.com", "1.2.3", &provider.ConnectorRunner{}, progress.Discard()); err != nil {
		t.Fatal(err)
	}
	if len(runner.applied) != 1 {
		t.Fatalf("applied %d runners, want one", len(runner.applied))
	}
	settings := runner.applied[0]
	if settings.URL != "https://box.example.com"+boxapi.ConnectorPath || settings.Arch != ssh.ARM64 || settings.Image() != "ocel-job:1.2.3" {
		t.Errorf("settings = %+v, want the box's endpoint, its arch and the job image of this release", settings)
	}
	if settings.CPUs != "3" || settings.Memory != "6144m" {
		t.Errorf("caps = %s, %s, want all but one CPU and three quarters of the memory", settings.CPUs, settings.Memory)
	}
}

func TestTheCapsAndImageTheFlagsNameWin(t *testing.T) {
	jobs, runner := jobsOnBox()
	asked := &provider.ConnectorRunner{CPUs: "2", Memory: "2g", Image: "registry.example.com/ocel-job:dev"}
	if err := jobs.Reconcile(context.Background(), "box.example.com", "dev", asked, progress.Discard()); err != nil {
		t.Fatal(err)
	}
	if settings := runner.applied[0]; settings.CPUs != "2" || settings.Memory != "2g" || settings.Image() != asked.Image {
		t.Errorf("settings = %+v, want the flags' caps and image", settings)
	}
}

func TestAConnectorAddedWithoutTheRunnerTakesAnEarlierRunnerOff(t *testing.T) {
	jobs, runner := jobsOnBox()
	runner.present = true
	if err := jobs.Reconcile(context.Background(), "box.example.com", "1.2.3", nil, progress.Discard()); err != nil {
		t.Fatal(err)
	}
	if runner.removed != 1 || len(runner.applied) != 0 {
		t.Errorf("removed %d, applied %d, want the runner removed and none applied", runner.removed, len(runner.applied))
	}
}

func TestABoxWithNoRunnerHasNoneToRemove(t *testing.T) {
	jobs, runner := jobsOnBox()
	if err := jobs.Remove(context.Background(), progress.Discard()); err != nil || runner.removed != 0 {
		t.Errorf("Remove = %v after %d removals, want nothing done", err, runner.removed)
	}
}

func TestADevelopmentOcelBuildsNoJobImageWithoutANamedOne(t *testing.T) {
	jobs, _ := jobsOnBox()
	err := jobs.Reconcile(context.Background(), "box.example.com", "dev", &provider.ConnectorRunner{}, progress.Discard())
	if !refusedWith(err, refusal.CodeInvalid) {
		t.Fatalf("err = %v, want a development build refused", err)
	}
}
