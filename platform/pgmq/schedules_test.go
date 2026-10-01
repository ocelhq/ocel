package pgmq

import (
	"context"
	"testing"
	"time"

	"github.com/ocelhq/ocel/pkg/cron"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
)

func nightly(topic *contractv1.ManifestTopic) { topic.Cron = "0 3 * * *" }

func scheduledAt(t *testing.T, engine *Engine, task string) (time.Time, bool) {
	t.Helper()
	var next time.Time
	err := engine.pool.QueryRow(context.Background(), "SELECT next_at FROM ocel.schedules WHERE topic = $1", task).Scan(&next)
	if err != nil {
		return time.Time{}, false
	}
	return next, true
}

func TestACronTaskIsScheduledForTheNextTimeItsExpressionMatches(t *testing.T) {
	engine := applied(t, map[string]*contractv1.ManifestTopic{"report": aTask(nightly)}, nil)

	schedule, err := cron.Parse("0 3 * * *")
	if err != nil {
		t.Fatal(err)
	}
	next, found := scheduledAt(t, engine, "report")
	if want := schedule.Next(time.Now()); !found || !next.Equal(want) {
		t.Errorf("the report is scheduled at %v, want %v", next, want)
	}
}

func TestADueScheduleTriggersOneRunForEveryTimeItMissedAndMovesToItsNextTime(t *testing.T) {
	worker := newWorker(t, succeeding)
	engine := applied(t, map[string]*contractv1.ManifestTopic{"report": aTask(nightly)}, map[string]Worker{"worker": {URL: worker.server.URL}})
	missed := time.Now().Add(-72 * time.Hour).UTC().Truncate(time.Minute)
	if _, err := engine.pool.Exec(context.Background(), "UPDATE ocel.schedules SET next_at = $1 WHERE topic = 'report'", missed); err != nil {
		t.Fatal(err)
	}
	startDispatch(t, engine)

	got := awaitDelivered(t, worker, 1)
	time.Sleep(1500 * time.Millisecond)
	if n := len(worker.received()); n != 1 {
		t.Errorf("the schedule ran %d times, want one run for the days it missed", n)
	}
	timestamp := got[0].envelope.GetPayload().GetStructValue().GetFields()["timestamp"].GetStringValue()
	if fired, err := time.Parse(time.RFC3339, timestamp); err != nil || !fired.Equal(missed) {
		t.Errorf("the run's payload timestamp = %q, want the time it was scheduled for, %s", timestamp, missed.Format(time.RFC3339))
	}
	next, _ := scheduledAt(t, engine, "report")
	if !next.After(time.Now()) || next.UTC().Hour() != 3 || next.Minute() != 0 {
		t.Errorf("the next run is scheduled at %v, want the next 03:00 UTC", next)
	}
}

func TestAScheduleKeepsItsTimeWhenReappliedAndGoesWhenItsCronDoes(t *testing.T) {
	engine := applied(t, map[string]*contractv1.ManifestTopic{"report": aTask(nightly)}, nil)
	pinned := time.Now().Add(time.Minute).UTC().Truncate(time.Microsecond)
	if _, err := engine.pool.Exec(context.Background(), "UPDATE ocel.schedules SET next_at = $1 WHERE topic = 'report'", pinned); err != nil {
		t.Fatal(err)
	}

	if err := engine.Apply(context.Background(), Deployment{Topics: map[string]*contractv1.ManifestTopic{"report": aTask(nightly, named("report"))}}); err != nil {
		t.Fatal(err)
	}
	if next, _ := scheduledAt(t, engine, "report"); !next.Equal(pinned) {
		t.Errorf("reapplying the same cron moved the schedule to %v, want it kept at %v", next, pinned)
	}

	if err := engine.Apply(context.Background(), Deployment{Topics: map[string]*contractv1.ManifestTopic{"report": aTask(named("report"))}}); err != nil {
		t.Fatal(err)
	}
	if _, found := scheduledAt(t, engine, "report"); found {
		t.Error("a task without a cron is still scheduled")
	}
}

func named(name string) func(*contractv1.ManifestTopic) {
	return func(topic *contractv1.ManifestTopic) { topic.Consumers[0].Name = name }
}
