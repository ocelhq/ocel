package runs_test

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"google.golang.org/protobuf/types/known/durationpb"
	"google.golang.org/protobuf/types/known/timestamppb"

	taskv1 "github.com/ocelhq/ocel/pkg/proto/app/task/v1"
	"github.com/ocelhq/ocel/pkg/provider"
)

func aDebounceKeyNamingACanceledRun(t *testing.T, p pushed) (*taskv1.TriggerOptions, string) {
	t.Helper()
	options := &taskv1.TriggerOptions{Debounce: &taskv1.Debounce{Key: "k", Delay: durationpb.New(time.Hour)}}
	first := p.trigger(t, "remind", options)
	if _, err := p.engine.Tasks().CancelRun(context.Background(), &taskv1.CancelRunRequest{Id: first}); err != nil {
		t.Fatal(err)
	}
	return options, first
}

func TestADebouncedTriggerOutlastsItsKeyGoingStaleNineWritesRunning(t *testing.T) {
	t.Parallel()

	p, raced := aRacedEngine(t, aTaskNamed("remind", 3, false), http.StatusOK)
	options, first := aDebounceKeyNamingACanceledRun(t, p)
	raced.goStale(0, 0, staleWritesRunning, nil)
	resp, err := p.engine.Tasks().Trigger(context.Background(), &taskv1.TriggerRequest{Task: "remind", Payload: []byte(`{}`), Options: options})
	if err != nil {
		t.Fatalf("Trigger: %v", err)
	}
	if resp.GetId() == first {
		t.Fatalf("the trigger answered the canceled run %s, want a new run that took the key", first)
	}
	if run, err := p.store.ReadRun(context.Background(), "remind", resp.GetId()); err != nil || run.Status != provider.RunDelayed {
		t.Errorf("the run that took the key reads %s (%v), want it delayed", run.Status, err)
	}
}

func TestARescheduleOutlastsItsRunGoingStaleNineWritesRunning(t *testing.T) {
	t.Parallel()

	p, raced := aRacedEngine(t, aTaskNamed("remind", 3, false), http.StatusOK)
	ctx := context.Background()
	id := p.trigger(t, "remind", &taskv1.TriggerOptions{DueAt: timestamppb.New(time.Now().Add(time.Hour))})
	due := time.Now().Add(2 * time.Hour)
	raced.goStale(0, staleWritesRunning, 0, nil)
	if _, err := p.engine.Tasks().RescheduleRun(ctx, &taskv1.RescheduleRunRequest{Id: id, DueAt: timestamppb.New(due)}); err != nil {
		t.Fatalf("RescheduleRun: %v", err)
	}
	if run, err := p.store.ReadRun(ctx, "remind", id); err != nil || !run.DueAt.Equal(due.Truncate(time.Microsecond)) && !run.DueAt.Equal(due) {
		t.Errorf("the run is due at %v (%v), want %v", run.DueAt, err, due)
	}
}

func TestADebouncedTriggerWaitsBeforeTryingItsStaleKeyAgainAndStopsWhenItsContextEnds(t *testing.T) {
	t.Parallel()

	p, raced := aRacedEngine(t, aTaskNamed("remind", 3, false), http.StatusOK)
	options, _ := aDebounceKeyNamingACanceledRun(t, p)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	raced.goStale(0, 0, -1, cancel)
	_, err := p.engine.Tasks().Trigger(ctx, &taskv1.TriggerRequest{Task: "remind", Payload: []byte(`{}`), Options: options})
	if !errors.Is(err, context.Canceled) || raced.staleAnswers() != 1 {
		t.Errorf("Trigger = %v after %d stale writes, want it to stop at the canceled context before a second try", err, raced.staleAnswers())
	}
}

func TestARescheduleWaitsBeforeTryingItsStaleRunAgainAndStopsWhenItsContextEnds(t *testing.T) {
	t.Parallel()

	p, raced := aRacedEngine(t, aTaskNamed("remind", 3, false), http.StatusOK)
	id := p.trigger(t, "remind", &taskv1.TriggerOptions{DueAt: timestamppb.New(time.Now().Add(time.Hour))})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	raced.goStale(0, -1, 0, cancel)
	_, err := p.engine.Tasks().RescheduleRun(ctx, &taskv1.RescheduleRunRequest{Id: id, DueAt: timestamppb.New(time.Now().Add(2 * time.Hour))})
	if !errors.Is(err, context.Canceled) || raced.staleAnswers() != 1 {
		t.Errorf("RescheduleRun = %v after %d stale writes, want it to stop at the canceled context before a second try", err, raced.staleAnswers())
	}
}
