package gcp

import (
	"context"
	"maps"
	"slices"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/fake"
	"github.com/ocelhq/ocel/pkg/provider/transform"
	"github.com/ocelhq/ocel/pkg/stackrecords"
	"github.com/ocelhq/ocel/platform/gcp/provider/resources/topic"
)

func cronTaskStack(cron string) provider.StackSpec {
	spec := topicStack()
	spec.Resources = []provider.Resource{{Name: "orders", Declared: "orders", Type: provider.BindingTask, Topic: cronTask(cron)}}
	return spec
}

func ordersJobPath(t *testing.T, p *Provider) string {
	t.Helper()
	return "projects/acme-prod/locations/europe-west1/jobs/" + topicNamed(t, topic.Names(namesOf(t, p), topicStack().Ref), "orders")
}

func sentToJobs(server *topicsServer) []sentPatch {
	var sent []sentPatch
	for _, each := range server.sent() {
		if strings.Contains(each.name, "/jobs/") {
			sent = append(sent, each)
		}
	}
	return sent
}

func TestATasksCronJobIsCreatedWithThePatchedRetryPolicyAndDescription(t *testing.T) {
	p, server := transformingTopics(t, &patching{patches: transform.Patches{"job": {
		"retryConfig": map[string]any{"retryCount": 3, "minBackoffDuration": "10s"},
		"description": "the nightly order digest",
	}}})

	provisionTopicStack(t, p, cronTaskStack("0 3 * * *"))

	job := server.job(ordersJobPath(t, p))
	if job == nil || job.RetryConfig == nil || job.RetryConfig.RetryCount != 3 || job.RetryConfig.MinBackoffDuration != "10s" || job.Description != "the nightly order digest" {
		t.Fatalf("made the job %+v, want three retries backing off from 10s, and the patched description", job)
	}
	if job.Schedule != "0 3 * * *" || job.PubsubTarget == nil {
		t.Errorf("made the job firing at %q onto %+v, want ocel's schedule and target kept", job.Schedule, job.PubsubTarget)
	}

	provisionTopicStack(t, p, cronTaskStack("0 3 * * *"))
	for _, sent := range sentToJobs(server) {
		if slices.Contains(sent.mask, "retryConfig") || slices.Contains(sent.mask, "description") {
			t.Errorf("a second deploy sent the mask %v, want nothing patched sent again", sent.mask)
		}
	}
}

func TestAPatchAddedToATasksCronJobIsPlannedAndSent(t *testing.T) {
	pass := &patching{}
	p, server := transformingTopics(t, pass)
	provisionTopicStack(t, p, cronTaskStack("0 3 * * *"))

	pass.patches = transform.Patches{"job": {"retryConfig": map[string]any{"retryCount": 2}}}
	change := planTopicStack(t, p, cronTaskStack("0 3 * * *"))
	if change.Action != provider.ActionUpdate || !strings.Contains(change.Reason, "the Cloud Scheduler job's retry policy changes from 0 retries to 2 retries") {
		t.Fatalf("Plan() = %+v, want the job updated for its retry policy", change)
	}

	provisionTopicStack(t, p, cronTaskStack("0 3 * * *"))
	sent := sentToJobs(server)
	if last := sent[len(sent)-1]; !slices.Contains(last.mask, "retryConfig") || server.job(ordersJobPath(t, p)).RetryConfig.RetryCount != 2 {
		t.Errorf("sent the job %+v, want two retries sent under the retryConfig mask", last)
	}
}

func TestRemovingAJobPatchPutsBackGooglesRetryPolicy(t *testing.T) {
	pass := &patching{patches: transform.Patches{"job": {
		"retryConfig": map[string]any{"retryCount": 3, "maxBackoffDuration": "60s"},
		"description": "the nightly order digest",
	}}}
	p, server := transformingTopics(t, pass)
	provisionTopicStack(t, p, cronTaskStack("0 3 * * *"))

	pass.patches = nil
	provisionTopicStack(t, p, cronTaskStack("0 3 * * *"))

	job := server.job(ordersJobPath(t, p))
	if job.RetryConfig.RetryCount != 0 || job.RetryConfig.MaxBackoffDuration != "3600s" || job.Description != "" {
		t.Errorf("the job holds %+v, %q, want Google's retry policy back and no description", job.RetryConfig, job.Description)
	}
	before := len(sentToJobs(server))
	provisionTopicStack(t, p, cronTaskStack("0 3 * * *"))
	if last := sentToJobs(server)[before]; slices.Contains(last.mask, "retryConfig") {
		t.Errorf("sent the mask %v once the job is back as ocel sets it, want only ocel's own fields", last.mask)
	}
}

func TestAJobPatchOnATaskWithNoCronIsRefusedAtPlan(t *testing.T) {
	p, server := transformingTopics(t, &patching{patches: transform.Patches{"job": {"description": "never fires"}}})

	_, err := p.Stacks().Plan(context.Background(), cronTaskStack(""), nil)

	if err == nil || !strings.Contains(err.Error(), "no cron") {
		t.Errorf("Plan() = %v, want the job patch refused for a task that fires on no cron", err)
	}
	if wrote := server.wrote(); len(wrote) != 0 {
		t.Errorf("wrote %v, want nothing touched", wrote)
	}
}

func TestATaskThatDropsItsCronForgetsWhatItsJobPatchReached(t *testing.T) {
	pass := &patching{patches: transform.Patches{"job": {"description": "the nightly order digest"}}}
	p, _ := transformingTopics(t, pass)
	spec := cronTaskStack("0 3 * * *")
	result := provisionTopicStack(t, p, spec)
	if err := stackrecords.Write(context.Background(), p.KeyValues(), spec.Ref.Tier, spec.Ref.Project, spec.Ref.Name, stackrecords.Stack{Kind: provider.StackInfra, Bindings: result.Bindings}); err != nil {
		t.Fatal(err)
	}

	if !slices.ContainsFunc(slices.Collect(maps.Keys(p.records.(*fake.KeyValues).Snapshot())), func(key string) bool {
		return strings.Contains(key, "patches") && strings.Contains(key, "job")
	}) {
		t.Fatal("nothing recorded what the job's patch reached")
	}

	pass.patches = nil
	provisionTopicStack(t, p, cronTaskStack(""))

	for key := range p.records.(*fake.KeyValues).Snapshot() {
		if strings.Contains(key, "patches") && strings.Contains(key, "job") {
			t.Errorf("%s is left behind, want what the job's patch reached forgotten with the job", key)
		}
	}
}

func TestATopicTakesNoJobPatch(t *testing.T) {
	p, _ := transformingTopics(t, &patching{patches: transform.Patches{"job": {"description": "a topic has no job"}}})

	_, err := p.Stacks().Plan(context.Background(), topicStack(), nil)

	if err == nil || !strings.Contains(err.Error(), "job") {
		t.Errorf("Plan() = %v, want a job patch on a topic refused", err)
	}
}
