package gcp

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/resources"
	"github.com/ocelhq/ocel/pkg/refusal"
)

func aTopic(t *testing.T, kind provider.BindingType, spec *provider.TopicSpec) resources.ProvisionRequest {
	t.Helper()
	return resources.ProvisionRequest{
		Ref:      productionRef(t),
		Resource: provider.Resource{Name: string(kind) + "-orders", Declared: "orders", Type: kind, Topic: spec},
	}
}

func consumersNamed(ordered bool, consumers ...string) *provider.TopicSpec {
	spec := &provider.TopicSpec{Ordered: ordered}
	for _, consumer := range consumers {
		spec.Consumers = append(spec.Consumers, provider.ConsumerSpec{
			Name: consumer, Worker: "worker", Retry: provider.RetryPolicy{MaxAttempts: 3, MinDelay: time.Second, MaxDelay: time.Minute},
		})
	}
	return spec
}

func cronTask(cron string) *provider.TopicSpec {
	return &provider.TopicSpec{Cron: cron, Consumers: []provider.ConsumerSpec{{Name: "orders", Worker: "worker", Exclusive: true}}}
}

func provisionTopic(t *testing.T, p *Provider, in resources.ProvisionRequest) *provider.Binding {
	t.Helper()
	binding, err := p.ProvisionTopic(context.Background(), in, nil)
	if err != nil {
		t.Fatalf("ProvisionTopic() = %v", err)
	}
	return &binding
}

func planTopic(t *testing.T, p *Provider, server *topicsServer, in resources.ProvisionRequest, recorded *provider.Binding) (provider.ChangeAction, string, error) {
	t.Helper()
	before := len(server.wrote())
	action, reason, err := p.PlanTopic(context.Background(), in, recorded)
	if wrote := server.wrote()[before:]; len(wrote) != 0 {
		t.Errorf("PlanTopic() wrote %q, want a plan that only reads", wrote)
	}
	return action, reason, err
}

func TestAMissingTopicPlansACreate(t *testing.T) {
	p, server := servingTopics(t)

	action, _, err := planTopic(t, p, server, aTopic(t, provider.BindingTopic, consumersNamed(false, "ship")), nil)
	if err != nil || action != provider.ActionCreate {
		t.Errorf("PlanTopic() = %s, %v, want %s", action, err, provider.ActionCreate)
	}
}

func TestATopicAsDeclaredPlansKeep(t *testing.T) {
	p, server := servingTopics(t)
	for _, in := range []resources.ProvisionRequest{
		aTopic(t, provider.BindingTopic, consumersNamed(false, "ship", "bill")),
		aTopic(t, provider.BindingTask, cronTask("*/5 * * * *")),
	} {
		recorded := provisionTopic(t, p, in)

		action, reason, err := planTopic(t, p, server, in, recorded)
		if err != nil || action != provider.ActionKeep || reason != provider.ReasonCurrent {
			t.Errorf("PlanTopic(%s) = %s %q, %v, want %s %q", in.Resource.Type, action, reason, err, provider.ActionKeep, provider.ReasonCurrent)
		}
	}
}

func TestATaskWhoseCronChangesPlansAnUpdate(t *testing.T) {
	p, server := servingTopics(t)
	recorded := provisionTopic(t, p, aTopic(t, provider.BindingTask, cronTask("*/5 * * * *")))

	action, reason, err := planTopic(t, p, server, aTopic(t, provider.BindingTask, cronTask("0 * * * *")), recorded)
	if err != nil || action != provider.ActionUpdate {
		t.Fatalf("PlanTopic() = %s, %v, want %s: the provision that follows reschedules the job", action, err, provider.ActionUpdate)
	}
	if want := `from firing at "*/5 * * * *" to "0 * * * *"`; !strings.Contains(reason, want) {
		t.Errorf("PlanTopic() reason = %q, want it to say %q", reason, want)
	}
}

func TestATaskWhoseScheduleJobWasDeletedPlansAnUpdate(t *testing.T) {
	p, server := servingTopics(t)
	in := aTopic(t, provider.BindingTask, cronTask("*/5 * * * *"))
	recorded := provisionTopic(t, p, in)
	job := "projects/acme-prod/locations/europe-west1/jobs/" + taskNames(names(t, p), in.Ref).ScheduleJob("orders")
	if server.job(job) == nil {
		t.Fatalf("no Cloud Scheduler job %s was made", job)
	}
	server.forgetJob(job)

	action, reason, err := planTopic(t, p, server, in, recorded)
	if err != nil || action != provider.ActionUpdate || !strings.Contains(reason, "is created") {
		t.Errorf("PlanTopic() = %s %q, %v, want an update saying the schedule job is created", action, reason, err)
	}
}

func TestATaskThatDropsItsCronPlansAnUpdate(t *testing.T) {
	p, server := servingTopics(t)
	recorded := provisionTopic(t, p, aTopic(t, provider.BindingTask, cronTask("*/5 * * * *")))

	action, reason, err := planTopic(t, p, server, aTopic(t, provider.BindingTask, cronTask("")), recorded)
	if err != nil || action != provider.ActionUpdate || !strings.Contains(reason, "is deleted") {
		t.Errorf("PlanTopic() = %s %q, %v, want an update saying the schedule job is deleted", action, reason, err)
	}
}

func TestATopicThatDropsAConsumerPlansAnUpdateNamingItsSubscription(t *testing.T) {
	p, server := servingTopics(t)
	in := aTopic(t, provider.BindingTopic, consumersNamed(false, "ship", "bill"))
	recorded := provisionTopic(t, p, in)
	subscription := "projects/acme-prod/subscriptions/" + taskNames(names(t, p), in.Ref).Subscription("orders", "bill")
	server.holdSubscription(subscription)

	action, reason, err := planTopic(t, p, server, aTopic(t, provider.BindingTopic, consumersNamed(false, "ship")), recorded)
	if err != nil || action != provider.ActionUpdate || !strings.Contains(reason, subscription) {
		t.Errorf("PlanTopic() = %s %q, %v, want an update naming the subscription %s it deletes", action, reason, err, subscription)
	}
}

func TestATopicWhoseConsumerRetryChangesPlansAnUpdate(t *testing.T) {
	p, server := servingTopics(t)
	in := aTopic(t, provider.BindingTopic, consumersNamed(false, "ship"))
	recorded := provisionTopic(t, p, in)
	changed := consumersNamed(false, "ship")
	changed.Consumers[0].Retry.MaxAttempts = 5

	if action, _, err := planTopic(t, p, server, aTopic(t, provider.BindingTopic, changed), recorded); err != nil || action != provider.ActionUpdate {
		t.Errorf("PlanTopic() = %s, %v, want %s: the binding the provision records changes", action, err, provider.ActionUpdate)
	}
}

func TestATopicWhoseOrderingChangesUnderAKeptConsumerIsRefusedAtPlan(t *testing.T) {
	p, server := servingTopics(t)
	recorded := provisionTopic(t, p, aTopic(t, provider.BindingTopic, consumersNamed(false, "ship")))

	_, _, err := planTopic(t, p, server, aTopic(t, provider.BindingTopic, consumersNamed(true, "ship")), recorded)
	var refused refusal.Refusal
	if !errors.As(err, &refused) || refused.Code != refusal.CodeInvalid {
		t.Errorf("PlanTopic() = %v, want the %s refusal the provision would give", err, refusal.CodeInvalid)
	}
}
