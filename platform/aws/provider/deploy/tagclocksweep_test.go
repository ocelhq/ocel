package deploy

import (
	"context"
	"errors"
	"testing"

	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/providerkit"
)

type sweepingIndex struct {
	fakeStackIndex
	order []string
	err   error
}

func (s *sweepingIndex) SweepTagClock(_ context.Context, project string, stack naming.StackName) error {
	s.order = append(s.order, "sweep "+project+"/"+stack.String())
	return s.err
}

func (s *sweepingIndex) RemoveStack(ctx context.Context, project string, stack naming.StackName) error {
	s.order = append(s.order, "forget "+project+"/"+stack.String())
	return s.fakeStackIndex.RemoveStack(ctx, project, stack)
}

func teardownAccess(t *testing.T) PulumiAccess {
	t.Helper()
	return PulumiAccess{
		PulumiProject: "ocel",
		Passphrase:    "teardown",
		BackendURL:    "file://" + t.TempDir(),
	}
}

func teardownRef() providerkit.StackRef {
	return providerkit.StackRef{
		Project: "shop",
		Class:   providerkit.ClassProduction,
		Name:    naming.AppStack("production", "web", naming.NewRelease("dep1", "fp1")),
	}
}

func TestATeardownSweepsTheTagClockBeforeItForgetsTheStack(t *testing.T) {
	index := &sweepingIndex{}
	release := releaserFor(teardownAccess(t), index, &Realized{}, &fakeEngine{record: func(string) {}})

	if err := release.Destroy(context.Background(), teardownRef(), nil); err != nil {
		t.Fatalf("Destroy() = %v", err)
	}
	want := []string{
		"sweep shop/" + teardownRef().Name.String(),
		"forget shop/" + teardownRef().Name.String(),
	}
	if len(index.order) != len(want) || index.order[0] != want[0] || index.order[1] != want[1] {
		t.Fatalf("teardown did %v, want %v — a forgotten stack is one nothing will ever sweep the clock for", index.order, want)
	}
}

func TestATagClockThatWillNotSweepStopsTheTeardownShortOfForgettingTheStack(t *testing.T) {
	index := &sweepingIndex{err: errors.New("dynamo is down")}
	release := releaserFor(teardownAccess(t), index, &Realized{}, &fakeEngine{record: func(string) {}})

	if err := release.Destroy(context.Background(), teardownRef(), nil); err == nil {
		t.Fatal("Destroy() = nil where the tag clock refused to sweep, want the failure surfaced")
	}
	if len(index.removed) != 0 {
		t.Errorf("the stack was forgotten as %v despite an unswept clock, leaving rows nothing can reach", index.removed)
	}
}

func TestAnIndexThatKeepsNoTagClockTearsDownAllTheSame(t *testing.T) {
	index := &fakeStackIndex{}
	release := releaserFor(teardownAccess(t), index, &Realized{}, &fakeEngine{record: func(string) {}})

	if err := release.Destroy(context.Background(), teardownRef(), nil); err != nil {
		t.Fatalf("Destroy() through an index with no tag clock = %v", err)
	}
	if len(index.removed) != 1 {
		t.Errorf("the index forgot %v, want the one stack the teardown named", index.removed)
	}
}
