package topics_test

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/ocelhq/ocel/pkg/envelope"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/platform/gcp/provider/topics"
)

type jobShape struct {
	Schedule     string `json:"schedule"`
	TimeZone     string `json:"timeZone"`
	PubsubTarget struct {
		TopicName  string            `json:"topicName"`
		Attributes map[string]string `json:"attributes"`
	} `json:"pubsubTarget"`
}

func readJob(t *testing.T, endpoint, path string) (jobShape, int) {
	t.Helper()
	resp, err := http.Get(endpoint + "/v1/" + path)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var job jobShape
	_ = json.NewDecoder(resp.Body).Decode(&job)
	return job, resp.StatusCode
}

func TestLiveACronTaskIsACloudSchedulerJobPublishingToItsTopic(t *testing.T) {
	endpoint := emulatedEndpoint(t)
	clients := liveClients(t)
	names := topics.Names{Namespace: "ocel", Scope: scopeOf(t)}
	declared := map[string]*contractv1.ManifestTopic{
		"digest": {Cron: "*/5 * * * *", Consumers: []*contractv1.ManifestConsumer{{Name: "digest", Worker: "worker", Exclusive: true}}},
	}
	topology := topics.Topology{Names: names, Topics: declared, Pushes: map[string]topics.Push{"worker": {URL: "https://worker.run.app", ServiceAccount: invoker}}}

	if err := topology.Ensure(context.Background(), clients); err != nil {
		t.Fatalf("Ensure() = %v", err)
	}
	if err := topology.Ensure(context.Background(), clients); err != nil {
		t.Fatalf("Ensure() over the schedule it made = %v, want it kept", err)
	}
	path := "projects/" + clients.Project + "/locations/" + clients.Region + "/jobs/" + names.ScheduleJob("digest")
	job, status := readJob(t, endpoint, path)
	if status != http.StatusOK {
		t.Fatalf("the schedule of digest answered %d, want it made", status)
	}
	if job.Schedule != "*/5 * * * *" || job.TimeZone != "Etc/UTC" {
		t.Errorf("the schedule fires at %q in %q, want the task's cron in UTC", job.Schedule, job.TimeZone)
	}
	if job.PubsubTarget.TopicName != "projects/"+clients.Project+"/topics/"+names.Topic("digest") || job.PubsubTarget.Attributes[topics.ScheduleAttribute] == "" {
		t.Errorf("the schedule publishes to %s with %v, want the digest topic marked as scheduled", job.PubsubTarget.TopicName, job.PubsubTarget.Attributes)
	}

	if err := topology.Remove(context.Background(), clients); err != nil {
		t.Fatal(err)
	}
	if _, status := readJob(t, endpoint, path); status != http.StatusNotFound {
		t.Errorf("the schedule of digest answered %d after Remove(), want it gone", status)
	}
}

func TestLiveAScheduledMessageRunsWithTheMinuteItWasDueAt(t *testing.T) {
	worker := newFakeWorker(t, always(http.StatusOK, `{}`))
	d := newDelivering(t, worker)
	fired := time.Date(2026, 10, 2, 9, 5, 0, 412_000_000, time.UTC)

	if code := d.push("resize", "resize", push{publishedAt: fired, attributes: map[string]string{topics.ScheduleAttribute: "true"}}); !acked(code) {
		t.Fatalf("the scheduled push answered %d, want it acked", code)
	}
	received := worker.received()
	if len(received) != 1 {
		t.Fatalf("the worker got %d envelopes, want 1", len(received))
	}
	if want := `{"timestamp":"2026-10-02T09:05:00Z"}`; string(received[0]["payload"]) != want {
		t.Errorf("the scheduled run's payload is %s, want %s as pgmq hands it", received[0]["payload"], want)
	}
	execution := envelope.MessageIDFrom(fired, "17") + "-resize"
	if run := d.run(execution); run.Status != provider.RunCompleted {
		t.Errorf("the scheduled run is %s, want completed", run.Status)
	}
}
