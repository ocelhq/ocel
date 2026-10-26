package gcp

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/fake"
	"github.com/ocelhq/ocel/pkg/provider/transform"
	"github.com/ocelhq/ocel/platform/gcp/provider/resources/topic"
)

const ordersKey = "projects/acme/locations/europe-west1/keyRings/r/cryptoKeys/orders"

func topicStack() provider.StackSpec {
	return provider.StackSpec{
		Ref:  provider.StackRef{Project: "shop", Tier: environment.TierProduction, Name: naming.InfraStack("prod")},
		Kind: provider.StackInfra,
		Resources: []provider.Resource{
			{Name: "orders", Declared: "orders", Type: provider.BindingTopic, Topic: consumersNamed(false, "ship")},
		},
	}
}

func transformingTopics(t *testing.T, pass transform.Pass) (*Provider, *topicsServer) {
	t.Helper()
	p, server := servingTopics(t)
	p.transforms = pass
	return p, server
}

func provisionTopicStack(t *testing.T, p *Provider, spec provider.StackSpec) provider.StackResult {
	t.Helper()
	result, err := p.Stacks().Provision(context.Background(), spec, &fake.Log{})
	if err != nil {
		t.Fatalf("Provision() = %v", err)
	}
	return result
}

func planTopicStack(t *testing.T, p *Provider, spec provider.StackSpec) provider.Change {
	t.Helper()
	plan, err := p.Stacks().Plan(context.Background(), spec, nil)
	if err != nil {
		t.Fatalf("Plan() = %v", err)
	}
	if len(plan.Groups) != 1 || len(plan.Groups[0].Changes) != 1 {
		t.Fatalf("Plan() = %+v, want the one topic", plan.Groups)
	}
	return plan.Groups[0].Changes[0]
}

func ordersTopicPath(t *testing.T, p *Provider) string {
	t.Helper()
	return "projects/acme-prod/topics/" + topicNamed(t, topic.Names(namesOf(t, p), topicStack().Ref), "orders")
}

func TestATransformIsAskedAboutTheTopicsAndTasksAStackProvisions(t *testing.T) {
	pass := &patching{}
	p, _ := transformingTopics(t, pass)
	spec := topicStack()
	spec.Resources = append(spec.Resources, provider.Resource{Name: "resize", Declared: "resize", Type: provider.BindingTask, Topic: cronTask("")})

	provisionTopicStack(t, p, spec)

	if want := []transform.Resource{{Type: "topic", Name: "orders"}, {Type: "task", Name: "resize"}}; !slices.Equal(pass.seen.Resources, want) {
		t.Errorf("the transform was offered %v, want %v", pass.seen.Resources, want)
	}
}

func TestATransformedTopicIsCreatedWithWhatThePatchSet(t *testing.T) {
	p, server := transformingTopics(t, &patching{patches: transform.Patches{"topic": {
		"messageRetentionDuration": "86400s",
		"kmsKeyName":               ordersKey,
		"messageStoragePolicy":     map[string]any{"allowedPersistenceRegions": []any{"europe-west1"}, "enforceInTransit": true},
	}}, tags: map[string]string{"team": "orders"}})

	provisionTopicStack(t, p, topicStack())

	created := server.created[ordersTopicPath(t, p)]
	if created["messageRetentionDuration"] != "86400s" || created["kmsKeyName"] != ordersKey {
		t.Errorf("created the topic %v, want a day's retention under the patched key", created)
	}
	policy, _ := created["messageStoragePolicy"].(map[string]any)
	if regions, _ := policy["allowedPersistenceRegions"].([]any); len(regions) != 1 || regions[0] != "europe-west1" || policy["enforceInTransit"] != true {
		t.Errorf("created the storage policy %v, want messages held in europe-west1 alone", policy)
	}
	if labels, _ := created["labels"].(map[string]any); labels["team"] != "orders" {
		t.Errorf("created labels %v, want the rule's tag", created["labels"])
	}

	provisionTopicStack(t, p, topicStack())
	if sent := server.sent(); len(sent) != 0 {
		t.Errorf("a second deploy patched %+v, want nothing sent over a topic already as patched", sent)
	}
}

func TestAPatchAddedToATopicInPlaceIsPlannedAndSent(t *testing.T) {
	pass := &patching{}
	p, server := transformingTopics(t, pass)
	provisionTopicStack(t, p, topicStack())

	pass.patches = transform.Patches{"topic": {"messageRetentionDuration": "86400s"}}
	pass.tags = map[string]string{"team": "orders"}
	change := planTopicStack(t, p, topicStack())
	if change.Action != provider.ActionUpdate || !strings.Contains(change.Reason, "message retention changes from off to 86400s") ||
		!strings.Contains(change.Reason, "labels team are set") {
		t.Fatalf("Plan() = %+v, want the topic updated for its retention and labels", change)
	}

	provisionTopicStack(t, p, topicStack())
	sent := server.sent()
	if len(sent) != 1 || !slices.Equal(sent[0].mask, []string{"messageRetentionDuration", "labels"}) || sent[0].body["messageRetentionDuration"] != "86400s" {
		t.Fatalf("patched %+v, want the retention and labels sent under their mask", sent)
	}
	if change := planTopicStack(t, p, topicStack()); strings.Contains(change.Reason, "Pub/Sub topic") {
		t.Errorf("Plan() after the patch = %+v, want nothing left to change on the Pub/Sub topic", change)
	}
}

func TestRemovingAPatchPutsTheTopicBackToWhatOcelSets(t *testing.T) {
	pass := &patching{patches: transform.Patches{"topic": {
		"messageRetentionDuration": "86400s",
		"kmsKeyName":               ordersKey,
		"messageStoragePolicy":     map[string]any{"allowedPersistenceRegions": []any{"europe-west1"}},
	}}, tags: map[string]string{"team": "orders"}}
	p, server := transformingTopics(t, pass)
	provisionTopicStack(t, p, topicStack())

	pass.patches, pass.tags = nil, nil
	provisionTopicStack(t, p, topicStack())

	sent := server.sent()
	if len(sent) != 1 {
		t.Fatalf("patched %+v, want the topic put back once", sent)
	}
	for _, cleared := range []string{"messageRetentionDuration", "kmsKeyName", "messageStoragePolicy", "labels"} {
		if !slices.Contains(sent[0].mask, cleared) {
			t.Errorf("sent the mask %v, want %s in it", sent[0].mask, cleared)
		}
	}
	if held := server.topic(ordersTopicPath(t, p)); held["messageRetentionDuration"] != nil || held["kmsKeyName"] != nil || held["messageStoragePolicy"] != nil || held["labels"] != nil {
		t.Errorf("the topic holds %v, want Google's defaults back", held)
	}

	provisionTopicStack(t, p, topicStack())
	if sent := server.sent(); len(sent) != 1 {
		t.Errorf("patched %d times, want nothing sent once the topic is back as ocel sets it", len(sent))
	}
}

func TestATopicRetentionGoogleCannotTakeIsRefusedAtPlan(t *testing.T) {
	p, server := transformingTopics(t, &patching{patches: transform.Patches{"topic": {"messageRetentionDuration": "7d"}}})

	_, err := p.Stacks().Plan(context.Background(), topicStack(), nil)

	if err == nil || !strings.Contains(err.Error(), "topic.messageRetentionDuration") {
		t.Errorf("Plan() = %v, want the retention refused by name", err)
	}
	if wrote := server.wrote(); len(wrote) != 0 {
		t.Errorf("wrote %v, want Pub/Sub untouched", wrote)
	}
}

func TestRemovingATopicForgetsWhatItsTransformsPatched(t *testing.T) {
	p, server := transformingTopics(t, &patching{patches: transform.Patches{
		"topic":        {"messageRetentionDuration": "86400s"},
		"subscription": {"retainAckedMessages": true},
	}, tags: map[string]string{"team": "orders"}})
	server.holdSubscription(subscriptionPath(t, p, "ship"))
	spec := topicStack()
	result := provisionTopicStack(t, p, spec)
	if len(p.records.(*fake.KeyValues).Snapshot()) == 0 {
		t.Fatal("the provision recorded nothing, want what its patches reached recorded")
	}

	if err := p.topicHooks().Remove(context.Background(), spec.Ref, result.Bindings[0], &fake.Log{}); err != nil {
		t.Fatalf("Remove() = %v", err)
	}

	for key := range p.records.(*fake.KeyValues).Snapshot() {
		if strings.Contains(key, "patches") {
			t.Errorf("%s is left behind, want every record a topic's patches wrote removed with it", key)
		}
	}
}
