package topics

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"slices"

	cloudscheduler "google.golang.org/api/cloudscheduler/v1"
	pubsub "google.golang.org/api/pubsub/v1"

	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/transform"
	"github.com/ocelhq/ocel/pkg/records"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/surface"
	"github.com/ocelhq/ocel/platform/gcp/provider/ports"
)

const patchesKind = "topic"

type Asked struct {
	Resource string
	Result   transform.Result
}

func (n Names) patchesKey(topic string, under ...string) records.Key {
	ref := provider.StackRef{Project: n.Scope.Slug, Tier: n.Scope.Tier, Name: naming.InfraStack(n.Scope.Environment)}
	return surface.KeyFor(ref, patchesKind, topic, under...)
}

func patch[D, U any](ctx context.Context, store records.Store, key records.Key, s surface.Surface[D, U], asked Asked, desired D) (surface.RecordedPatch[D, U], error) {
	return s.PatchRecorded(ctx, surface.Record{Store: store, Key: key}, asked.Resource, desired, asked.Result)
}

func unpatched[D, U any](desired D) surface.RecordedPatch[D, U] {
	return surface.RecordedPatch[D, U]{Patched: surface.Patched[D, U]{Desired: desired}}
}

func (t Topology) RefuseInvalidPatches() error {
	for _, name := range slices.Sorted(maps.Keys(t.Asked)) {
		asked := t.Asked[name]
		if _, err := TopicSurface.Patch(asked.Resource, pubsub.Topic{}, asked.Result.Patches[TopicSurface.Name], asked.Result.Tags, surface.Recorded{}); err != nil {
			return err
		}
		if _, err := SubscriptionSurface.Patch(asked.Resource, pubsub.Subscription{}, asked.Result.Patches[SubscriptionSurface.Name], asked.Result.Tags, surface.Recorded{}); err != nil {
			return err
		}
		job := asked.Result.Patches[JobSurface.Name]
		if len(job) > 0 && !isScheduled(t.Topics[name]) {
			return refusal.Refuse(refusal.CodeInvalid,
				"a transform patches %s's job, and it fires on no cron: give it a cron or drop the patch", asked.Resource)
		}
		if _, err := JobSurface.Patch(asked.Resource, cloudscheduler.Job{}, job, nil, surface.Recorded{}); err != nil {
			return err
		}
	}
	return nil
}

func (n Names) subscriptionPatchesKey(topic, consumer string) records.Key {
	return n.patchesKey(topic, "subscription", consumer)
}

func (n Names) jobPatchesKey(topic string) records.Key {
	return n.patchesKey(topic, "job")
}

func (n Names) askedSubscriptionsKey(topic string) records.Key {
	return n.patchesKey(topic, "subscriptions")
}

type askedSubscriptions struct {
	Resource string            `json:"resource"`
	Patch    map[string]any    `json:"patch,omitempty"`
	Tags     map[string]string `json:"tags,omitempty"`
}

func writeAskedSubscriptions(ctx context.Context, store records.Store, key records.Key, asked Asked) error {
	patch, tags := asked.Result.Patches[SubscriptionSurface.Name], asked.Result.Tags
	if len(patch) == 0 && len(tags) == 0 {
		return records.Forget(ctx, store, key)
	}
	value, err := json.Marshal(askedSubscriptions{Resource: asked.Resource, Patch: patch, Tags: tags})
	if err != nil {
		return err
	}
	return records.Change(ctx, store, key, func(entry records.Entry) ([]byte, bool, error) {
		return value, !bytes.Equal(entry.Value, value), nil
	})
}

func readAskedSubscriptions(ctx context.Context, store records.Store, key records.Key) (Asked, error) {
	entry, err := records.ReadOrEmpty(ctx, store, key)
	if err != nil {
		return Asked{}, fmt.Errorf("read what a topic's transforms ask of its subscriptions under %s: %w", key, err)
	}
	var recorded askedSubscriptions
	if len(entry.Value) > 0 {
		if err := json.Unmarshal(entry.Value, &recorded); err != nil {
			return Asked{}, fmt.Errorf("read what a topic's transforms ask of its subscriptions under %s: %w", key, err)
		}
	}
	return Asked{Resource: recorded.Resource, Result: transform.Result{
		Patches: transform.Patches{SubscriptionSurface.Name: recorded.Patch},
		Tags:    recorded.Tags,
	}}, nil
}

func forgetPatches(ctx context.Context, clients *ports.Clients, keys ...records.Key) error {
	if clients.Records == nil {
		return nil
	}
	for _, key := range keys {
		if err := (surface.Record{Store: clients.Records, Key: key}).Forget(ctx); err != nil {
			return err
		}
	}
	return nil
}
