package topic

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/durationpb"

	"github.com/ocelhq/ocel/pkg/images"
	"github.com/ocelhq/ocel/pkg/progress"
	resourcesv1 "github.com/ocelhq/ocel/pkg/proto/app/resources/v1"
	topicv1 "github.com/ocelhq/ocel/pkg/proto/app/topic/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/resources"
	"github.com/ocelhq/ocel/pkg/records"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/platform/vps/provider/boxapi"
	"github.com/ocelhq/ocel/platform/vps/provider/component"
	"github.com/ocelhq/ocel/platform/vps/provider/docker"
	"github.com/ocelhq/ocel/platform/vps/provider/resources/container"
	"github.com/ocelhq/ocel/platform/vps/provider/resources/postgres"
)

const (
	queueKind = "queue"
	queueData = "/var/lib/postgresql"
)

type QueueDatabases struct {
	sync.Mutex
	ensured map[string]bool
}

type Resource struct {
	box    component.Box
	queues *QueueDatabases
}

func New(box component.Box, queues *QueueDatabases) Resource {
	return Resource{box: box, queues: queues}
}

func NewQueueContainer(ref provider.StackRef) (container.Spec, error) {
	spec, err := postgres.NewContainer(ref, boxapi.QueueDatabaseName, queueKind, images.QueueDatabase(),
		container.Volume{Path: queueData, Generation: images.QueueDatabaseMajor()})
	if err != nil {
		return container.Spec{}, err
	}
	spec.Args = []string{"postgres", "-c", "listen_addresses="}
	spec.Sockets = boxapi.QueueSocketsPath
	spec.Ready = []string{"sh", "-c", `[ "$(cat /proc/1/comm)" = postgres ] && exec pg_isready -U ` + postgres.Superuser}
	spec.Labels = map[string]string{docker.LabelOwner: boxapi.QueueRole}
	superuser := spec.Credential.Reassert
	spec.Credential.Reassert = func(secret string) ([]string, string) {
		argv, stdin := superuser(secret)
		return argv, boxapi.QueueRoleSQL() + stdin
	}
	return spec, nil
}

func newRecord(in resources.ProvisionRequest) (records.Key, []byte, error) {
	if in.Resource.Topic == nil {
		return records.Key{}, nil, refusal.Refuse(refusal.CodeInvalid,
			"%s %s reached the box with no topic config, so nothing knows how to deliver it", in.Resource.Type, in.Resource.Declared)
	}
	config, err := protojson.Marshal(encodeManifestTopic(in.Resource.Topic))
	if err != nil {
		return records.Key{}, nil, err
	}
	return boxapi.QueueTopicKey(in.Ref.Tier, in.Ref.Project, in.Ref.Name.Env, component.ChooseDeclaredName(in.Resource.Declared, in.Resource.Name)), config, nil
}

func (r Resource) Plan(ctx context.Context, in resources.ProvisionRequest, _ *provider.Binding) (provider.ChangeAction, string, error) {
	key, config, err := newRecord(in)
	if err != nil {
		return "", "", err
	}
	var change component.Change
	if err := r.planQueue(ctx, in.Ref, &change); err != nil {
		return "", "", err
	}
	recorded, err := records.ReadOrEmpty(ctx, r.box.KeyValues, key)
	if err != nil {
		return "", "", err
	}
	switch {
	case len(recorded.Value) == 0:
		change.Add(provider.ActionCreate, "")
	case !sameRecord(recorded.Value, config):
		change.Add(provider.ActionUpdate, describeChange(recorded.Value, in.Resource.Topic))
	}
	return change.Report()
}

func sameRecord(kept, wanted []byte) bool {
	var a, b bytes.Buffer
	if json.Compact(&a, kept) != nil || json.Compact(&b, wanted) != nil {
		return false
	}
	return bytes.Equal(a.Bytes(), b.Bytes())
}

func describeChange(recorded []byte, spec *provider.TopicSpec) string {
	held := &contractv1.ManifestTopic{}
	if err := protojson.Unmarshal(recorded, held); err != nil {
		return "its delivery settings are recorded again"
	}
	want := encodeManifestTopic(spec)
	var changes []string
	if held.GetSchema() != want.GetSchema() {
		changes = append(changes, "its schema changes")
	}
	if held.GetOrdered() != want.GetOrdered() {
		changes = append(changes, fmt.Sprintf("its ordering changes from %t to %t", held.GetOrdered(), want.GetOrdered()))
	}
	if !proto.Equal(held.GetTtl(), want.GetTtl()) {
		changes = append(changes, "its retention changes from "+held.GetTtl().AsDuration().String()+" to "+want.GetTtl().AsDuration().String())
	}
	if held.GetCron() != want.GetCron() {
		changes = append(changes, fmt.Sprintf("its schedule changes from %q to %q", held.GetCron(), want.GetCron()))
	}
	if !slices.EqualFunc(held.GetConsumers(), want.GetConsumers(), func(a, b *contractv1.ManifestConsumer) bool { return proto.Equal(a, b) }) {
		changes = append(changes, "its consumers change")
	}
	if len(changes) == 0 {
		return "its delivery settings are recorded again"
	}
	return strings.Join(changes, ", ")
}

func (r Resource) Provision(ctx context.Context, in resources.ProvisionRequest, progress progress.Log) (provider.Binding, error) {
	key, config, err := newRecord(in)
	if err != nil {
		return provider.Binding{}, err
	}
	if err := r.ensureQueue(ctx, in.Ref, progress); err != nil {
		return provider.Binding{}, err
	}
	if err := records.Change(ctx, r.box.KeyValues, key, func(recorded records.Entry) ([]byte, bool, error) {
		return config, !sameRecord(recorded.Value, config), nil
	}); err != nil {
		return provider.Binding{}, err
	}
	return provider.Binding{Type: in.Resource.Type, Name: in.Resource.Name, Resource: in.Resource.Declared}, nil
}

var laneMessages = map[provider.Lane]topicv1.Lane{
	provider.LaneHigh:    topicv1.Lane_LANE_HIGH,
	provider.LaneDefault: topicv1.Lane_LANE_DEFAULT,
	provider.LaneLow:     topicv1.Lane_LANE_LOW,
}

func encodeManifestTopic(spec *provider.TopicSpec) *contractv1.ManifestTopic {
	topic := &contractv1.ManifestTopic{Schema: spec.Schema, Ordered: spec.Ordered, Ttl: encodeDuration(spec.TTL), Cron: spec.Cron}
	for _, consumer := range spec.Consumers {
		message := &contractv1.ManifestConsumer{
			Name:      consumer.Name,
			Worker:    consumer.Worker,
			Exclusive: consumer.Exclusive,
			Retry: &resourcesv1.RetryPolicy{
				MaxAttempts: int32(consumer.Retry.MaxAttempts),
				MinDelay:    durationpb.New(consumer.Retry.MinDelay),
				MaxDelay:    durationpb.New(consumer.Retry.MaxDelay),
			},
			Concurrency: int32(consumer.Concurrency),
			MaxDuration: encodeDuration(consumer.MaxDuration),
		}
		for _, lane := range consumer.Lanes {
			message.Lanes = append(message.Lanes, laneMessages[lane])
		}
		if consumer.Batch != nil {
			message.Batch = &resourcesv1.BatchPolicy{Size: int32(consumer.Batch.Size), Timeout: encodeDuration(consumer.Batch.Timeout)}
		}
		topic.Consumers = append(topic.Consumers, message)
	}
	return topic
}

func encodeDuration(d time.Duration) *durationpb.Duration {
	if d == 0 {
		return nil
	}
	return durationpb.New(d)
}

func newQueueDatabaseRecord(ref provider.StackRef, spec container.Spec, deliverySealed []byte) ([]byte, error) {
	return json.Marshal(boxapi.QueueDatabase{
		Container: spec.Name, Stack: ref.Name.String(),
		DeliverySealed: base64.StdEncoding.EncodeToString(deliverySealed),
	})
}

func (r Resource) planQueue(ctx context.Context, ref provider.StackRef, change *component.Change) error {
	spec, err := NewQueueContainer(ref)
	if err != nil {
		return err
	}
	action, reason, err := r.box.Containers.Plan(ctx, spec)
	if err != nil {
		return err
	}
	switch action {
	case provider.ActionKeep:
	case provider.ActionCreate:
		change.Add(provider.ActionUpdate, "the queue database for "+ref.Name.Env+"'s topics and tasks is started in container "+spec.Name)
	default:
		change.Add(provider.ActionUpdate, "the queue database container "+spec.Name+" is replaced: "+reason)
	}
	secret, err := r.box.Containers.Kept(ctx, ref.Tier, spec.Name)
	if err != nil {
		return err
	}
	if len(secret) == 0 && action != provider.ActionCreate {
		change.Add(provider.ActionUpdate, "the box keeps no password for the queue database "+spec.Name+", so a new one is minted that its running server was never handed")
	}
	deliverySealed, err := r.box.Containers.Kept(ctx, ref.Tier, nameDeliverySecret(spec))
	if err != nil {
		return err
	}
	if len(deliverySealed) == 0 {
		change.Add(provider.ActionUpdate, "a new delivery secret is minted for the queue database "+spec.Name)
		return nil
	}
	record, err := newQueueDatabaseRecord(ref, spec, deliverySealed)
	if err != nil {
		return err
	}
	recorded, err := records.ReadOrEmpty(ctx, r.box.KeyValues, boxapi.QueueDatabaseKey(ref.Tier, ref.Project, ref.Name.Env))
	if err != nil {
		return err
	}
	if string(recorded.Value) != string(record) {
		change.Add(provider.ActionUpdate, "the queue database "+spec.Name+" is recorded for the agent to deliver from")
	}
	return nil
}

func (r Resource) ensureQueue(ctx context.Context, ref provider.StackRef, progress progress.Log) error {
	spec, err := NewQueueContainer(ref)
	if err != nil {
		return err
	}
	r.queues.Lock()
	defer r.queues.Unlock()
	if r.queues.ensured[spec.Name] {
		return nil
	}
	progress.Say("Provisioning the queue database for " + ref.Name.Env + "'s topics and tasks in container " + spec.Name)
	bound, err := boxapi.NewQueueSecretAssociatedData(ref.Project, ref.Tier, ref.Name.String())
	if err != nil {
		return err
	}
	_, secret, err := r.box.KeptSealed(ctx, ref.Tier, spec.Name, spec.Resource, bound, component.MintSecret)
	if err != nil {
		return err
	}
	deliveryBound, err := boxapi.NewQueueDeliverySecretAssociatedData(ref.Project, ref.Tier, ref.Name.String())
	if err != nil {
		return err
	}
	deliverySealed, _, err := r.box.KeptSealed(ctx, ref.Tier, nameDeliverySecret(spec), spec.Resource, deliveryBound, component.MintSecret)
	if err != nil {
		return err
	}
	if err := r.box.Containers.Run(ctx, spec, secret); err != nil {
		return err
	}
	record, err := newQueueDatabaseRecord(ref, spec, deliverySealed)
	if err != nil {
		return err
	}
	key := boxapi.QueueDatabaseKey(ref.Tier, ref.Project, ref.Name.Env)
	if err := records.Change(ctx, r.box.KeyValues, key, func(recorded records.Entry) ([]byte, bool, error) {
		return record, string(recorded.Value) != string(record), nil
	}); err != nil {
		return err
	}
	if r.queues.ensured == nil {
		r.queues.ensured = map[string]bool{}
	}
	r.queues.ensured[spec.Name] = true
	return nil
}

func nameDeliverySecret(queue container.Spec) string {
	return queue.Name + "-" + boxapi.QueueDeliverySecretName
}

func (r Resource) Remove(ctx context.Context, ref provider.StackRef, binding provider.Binding, progress progress.Log) error {
	name := component.ChooseDeclaredName(binding.Resource, binding.Name)
	if err := records.Forget(ctx, r.box.KeyValues, boxapi.QueueTopicKey(ref.Tier, ref.Project, ref.Name.Env, name)); err != nil {
		return err
	}
	partition, under := boxapi.QueueTopicsUnder(ref.Tier, ref.Project, ref.Name.Env)
	remaining, err := r.box.KeyValues.List(ctx, partition, under...)
	if err != nil {
		return err
	}
	if len(remaining) > 0 {
		return nil
	}
	spec, err := NewQueueContainer(ref)
	if err != nil {
		return err
	}
	progress.Say("Removing the queue database " + spec.Name + " and its data: " + ref.Name.Env + " declares no topic or task any more")
	if err := r.box.Containers.Remove(ctx, container.Ref{Tier: ref.Tier, Project: ref.Project, Resource: spec.Resource, Name: spec.Name}); err != nil {
		return err
	}
	if err := r.box.Containers.ForgetKept(ctx, ref.Tier, []string{nameDeliverySecret(spec)}); err != nil {
		return err
	}
	r.queues.Lock()
	delete(r.queues.ensured, spec.Name)
	r.queues.Unlock()
	if err := records.Forget(ctx, r.box.KeyValues, boxapi.QueueDatabaseKey(ref.Tier, ref.Project, ref.Name.Env)); err != nil && !errors.Is(err, records.ErrNotFound) {
		return fmt.Errorf("forget the queue database %s: %w", spec.Name, err)
	}
	return nil
}
