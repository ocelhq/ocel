package deploy

import (
	"context"
	"errors"
	"testing"

	"github.com/pulumi/pulumi/sdk/v3/go/auto"

	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/providerkit"
	kitpulumi "github.com/ocelhq/ocel/pkg/providerkit/pulumi"
)

type fakeEngine struct {
	record func(string)
}

var _ kitpulumi.Engine = (*fakeEngine)(nil)

func (f *fakeEngine) Preview(context.Context, kitpulumi.Setup, providerkit.Reporter) ([]providerkit.Change, error) {
	return nil, nil
}

func (f *fakeEngine) PreviewDestroy(context.Context, kitpulumi.Setup, providerkit.Reporter) ([]providerkit.Change, error) {
	return nil, nil
}

func (f *fakeEngine) Up(_ context.Context, setup kitpulumi.Setup, _ providerkit.Reporter) (auto.OutputMap, error) {
	f.record("up-stack " + setup.Stack)
	return auto.OutputMap{}, nil
}

func (f *fakeEngine) Destroy(_ context.Context, setup kitpulumi.Setup, _ providerkit.Reporter) error {
	f.record("destroy-stack " + setup.Stack)
	return nil
}

func (f *fakeEngine) Outputs(context.Context, kitpulumi.Setup) (auto.OutputMap, error) {
	return auto.OutputMap{}, nil
}

type sweepingClock struct {
	order []string
	err   error
}

func (s *sweepingClock) SweepTagClock(_ context.Context, project string, stack naming.StackName) error {
	s.order = append(s.order, "sweep "+project+"/"+stack.String())
	return s.err
}

func tearingDown(t *testing.T, clock TagSweeper, engine kitpulumi.Engine) *Releaser {
	t.Helper()
	cfg := Config{
		PulumiProject: "ocel",
		Passphrase:    "teardown",
		BackendURL:    "file://" + t.TempDir(),
		Tags:          clock,
	}
	return newReleaser(fixed(cfg), &Realized{}, engine)
}

func teardownRef() providerkit.StackRef {
	return providerkit.StackRef{
		Project: "shop",
		Class:   providerkit.ClassProduction,
		Name:    naming.AppStack("production", "web", naming.NewRelease("dep1", "fp1")),
	}
}

func TestATeardownSweepsTheTagClockOfTheStackItDestroyed(t *testing.T) {
	clock := &sweepingClock{}
	var engine []string
	release := tearingDown(t, clock, &fakeEngine{record: func(s string) { engine = append(engine, s) }})

	if err := release.Destroy(context.Background(), teardownRef(), nil); err != nil {
		t.Fatalf("Destroy() = %v", err)
	}
	if len(engine) != 1 {
		t.Fatalf("the engine was asked for %v, want the one stack destroyed", engine)
	}
	want := "sweep shop/" + teardownRef().Name.String()
	if len(clock.order) != 1 || clock.order[0] != want {
		t.Fatalf("teardown did %v, want %q — a destroyed stack leaves clock rows nothing else will reach", clock.order, want)
	}
}

func TestATagClockThatWillNotSweepFailsTheTeardown(t *testing.T) {
	clock := &sweepingClock{err: errors.New("dynamo is down")}
	release := tearingDown(t, clock, &fakeEngine{record: func(string) {}})

	if err := release.Destroy(context.Background(), teardownRef(), nil); err == nil {
		t.Fatal("Destroy() = nil where the tag clock refused to sweep, want the failure surfaced")
	}
}

func TestAnAccountThatKeepsNoTagClockTearsDownAllTheSame(t *testing.T) {
	release := tearingDown(t, nil, &fakeEngine{record: func(string) {}})

	if err := release.Destroy(context.Background(), teardownRef(), nil); err != nil {
		t.Fatalf("Destroy() with no tag clock = %v", err)
	}
}
