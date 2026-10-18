package vps

import (
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
	"github.com/ocelhq/ocel/pkg/keyvalue"
	"github.com/ocelhq/ocel/pkg/progress"
	resourcesv1 "github.com/ocelhq/ocel/pkg/proto/app/resources/v1"
	topicv1 "github.com/ocelhq/ocel/pkg/proto/app/topic/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/resources"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/platform/vps/provider/host"
	"github.com/ocelhq/ocel/platform/vps/provider/live"
)

const (
	queueKind = "queue"
	queueData = "/var/lib/postgresql"
)

type queueDatabases struct {
	sync.Mutex
	ensured map[string]bool
}

func newQueueContainer(ref provider.StackRef) host.ResourceContainer {
	spec := newPostgresContainer(ref, live.QueueDatabaseName, queueKind, images.QueueDatabase(),
		host.Volume{Path: queueData, Generation: images.QueueDatabaseMajor()})
	spec.Args = []string{"postgres", "-c", "listen_addresses="}
	spec.Sockets = live.QueueSocketsPath
	spec.Ready = []string{"pg_isready", "-U", postgresSuperuser}
	spec.Labels = map[string]string{host.LabelOwner: live.QueueRole}
	superuser := spec.Credential.Reassert
	spec.Credential.Reassert = func(secret string) ([]string, string) {
		argv, stdin := superuser(secret)
		return argv, live.QueueRoleSQL() + stdin
	}
	return spec
}

func newTopicRecord(in resources.ProvisionRequest) (keyvalue.Key, []byte, error) {
	if in.Resource.Topic == nil {
		return keyvalue.Key{}, nil, refusal.Refuse(refusal.CodeInvalid,
			"%s %s reached the box with no topic config, so nothing knows how to deliver it", in.Resource.Type, in.Resource.Declared)
	}
	config, err := protojson.Marshal(encodeManifestTopic(in.Resource.Topic))
	if err != nil {
		return keyvalue.Key{}, nil, err
	}
	return live.QueueTopicKey(in.Ref.Tier, in.Ref.Project, in.Ref.Name.Env, chooseDeclaredName(in.Resource.Declared, in.Resource.Name)), config, nil
}

func (p *Provider) PlanTopic(ctx context.Context, in resources.ProvisionRequest, _ *provider.Binding) (provider.ChangeAction, string, error) {
	key, config, err := newTopicRecord(in)
	if err != nil {
		return "", "", err
	}
	var change resourceChange
	if err := p.planQueue(ctx, in.Ref, &change); err != nil {
		return "", "", err
	}
	recorded, err := keyvalue.ReadOrEmpty(ctx, p.keyValues, key)
	if err != nil {
		return "", "", err
	}
	switch {
	case len(recorded.Value) == 0:
		change.add(provider.ActionCreate, "")
	case string(recorded.Value) != string(config):
		change.add(provider.ActionUpdate, describeTopicChange(recorded.Value, in.Resource.Topic))
	}
	return change.report()
}

func describeTopicChange(recorded []byte, spec *provider.TopicSpec) string {
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

func (p *Provider) ProvisionTopic(ctx context.Context, in resources.ProvisionRequest, progress progress.Log) (provider.Binding, error) {
	key, config, err := newTopicRecord(in)
	if err != nil {
		return provider.Binding{}, err
	}
	if err := p.ensureQueue(ctx, in.Ref, progress); err != nil {
		return provider.Binding{}, err
	}
	if err := keyvalue.Change(ctx, p.keyValues, key, func(recorded keyvalue.Entry) ([]byte, bool, error) {
		return config, string(recorded.Value) != string(config), nil
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

func chooseDeclaredName(declared, name string) string {
	if declared != "" {
		return declared
	}
	return name
}

func newQueueDatabaseRecord(ref provider.StackRef, spec host.ResourceContainer, deliverySealed []byte) ([]byte, error) {
	return json.Marshal(live.QueueDatabase{
		Container: spec.Name, Stack: ref.Name.String(),
		DeliverySealed: base64.StdEncoding.EncodeToString(deliverySealed),
	})
}

func (p *Provider) planQueue(ctx context.Context, ref provider.StackRef, change *resourceChange) error {
	spec := newQueueContainer(ref)
	action, reason, err := p.host.PlanResource(ctx, spec)
	if err != nil {
		return err
	}
	switch action {
	case provider.ActionKeep:
	case provider.ActionCreate:
		change.add(provider.ActionUpdate, "the queue database for "+ref.Name.Env+"'s topics and tasks is started in container "+spec.Name)
	default:
		change.add(provider.ActionUpdate, "the queue database container "+spec.Name+" is replaced: "+reason)
	}
	secret, err := p.host.Kept(ctx, ref.Tier, spec.Name)
	if err != nil {
		return err
	}
	if len(secret) == 0 && action != provider.ActionCreate {
		change.add(provider.ActionUpdate, "the box keeps no password for the queue database "+spec.Name+", so a new one is minted that its running server was never handed")
	}
	deliverySealed, err := p.host.Kept(ctx, ref.Tier, nameDeliverySecret(spec))
	if err != nil {
		return err
	}
	if len(deliverySealed) == 0 {
		change.add(provider.ActionUpdate, "a new delivery secret is minted for the queue database "+spec.Name)
		return nil
	}
	record, err := newQueueDatabaseRecord(ref, spec, deliverySealed)
	if err != nil {
		return err
	}
	recorded, err := keyvalue.ReadOrEmpty(ctx, p.keyValues, live.QueueDatabaseKey(ref.Tier, ref.Project, ref.Name.Env))
	if err != nil {
		return err
	}
	if string(recorded.Value) != string(record) {
		change.add(provider.ActionUpdate, "the queue database "+spec.Name+" is recorded for the agent to deliver from")
	}
	return nil
}

func (p *Provider) ensureQueue(ctx context.Context, ref provider.StackRef, progress progress.Log) error {
	spec := newQueueContainer(ref)
	p.queues.Lock()
	defer p.queues.Unlock()
	if p.queues.ensured[spec.Name] {
		return nil
	}
	progress.Say("Provisioning the queue database for " + ref.Name.Env + "'s topics and tasks in container " + spec.Name)
	bound, err := live.NewQueueSecretAssociatedData(ref.Project, ref.Tier, ref.Name.String())
	if err != nil {
		return err
	}
	_, secret, err := p.keptSealed(ctx, ref.Tier, spec.Name, spec.Resource, bound, mintResourceSecret)
	if err != nil {
		return err
	}
	deliveryBound, err := live.NewQueueDeliverySecretAssociatedData(ref.Project, ref.Tier, ref.Name.String())
	if err != nil {
		return err
	}
	deliverySealed, _, err := p.keptSealed(ctx, ref.Tier, nameDeliverySecret(spec), spec.Resource, deliveryBound, mintResourceSecret)
	if err != nil {
		return err
	}
	if err := p.host.RunResource(ctx, spec, secret); err != nil {
		return err
	}
	record, err := newQueueDatabaseRecord(ref, spec, deliverySealed)
	if err != nil {
		return err
	}
	key := live.QueueDatabaseKey(ref.Tier, ref.Project, ref.Name.Env)
	if err := keyvalue.Change(ctx, p.keyValues, key, func(recorded keyvalue.Entry) ([]byte, bool, error) {
		return record, string(recorded.Value) != string(record), nil
	}); err != nil {
		return err
	}
	if p.queues.ensured == nil {
		p.queues.ensured = map[string]bool{}
	}
	p.queues.ensured[spec.Name] = true
	return nil
}

func nameDeliverySecret(queue host.ResourceContainer) string {
	return queue.Name + "-" + live.QueueDeliverySecretName
}

func (p *Provider) RemoveTopic(ctx context.Context, ref provider.StackRef, binding provider.Binding, progress progress.Log) error {
	name := chooseDeclaredName(binding.Resource, binding.Name)
	if err := keyvalue.Forget(ctx, p.keyValues, live.QueueTopicKey(ref.Tier, ref.Project, ref.Name.Env, name)); err != nil {
		return err
	}
	partition, under := live.QueueTopicsUnder(ref.Tier, ref.Project, ref.Name.Env)
	remaining, err := p.keyValues.List(ctx, partition, under...)
	if err != nil {
		return err
	}
	if len(remaining) > 0 {
		return nil
	}
	spec := newQueueContainer(ref)
	progress.Say("Removing the queue database " + spec.Name + " and its data: " + ref.Name.Env + " declares no topic or task any more")
	if err := p.host.RemoveResource(ctx, host.ResourceRef{Tier: ref.Tier, Project: ref.Project, Resource: spec.Resource, Name: spec.Name}); err != nil {
		return err
	}
	if err := p.host.ForgetKept(ctx, ref.Tier, []string{nameDeliverySecret(spec)}); err != nil {
		return err
	}
	p.queues.Lock()
	delete(p.queues.ensured, spec.Name)
	p.queues.Unlock()
	if err := keyvalue.Forget(ctx, p.keyValues, live.QueueDatabaseKey(ref.Tier, ref.Project, ref.Name.Env)); err != nil && !errors.Is(err, keyvalue.ErrNotFound) {
		return fmt.Errorf("forget the queue database %s: %w", spec.Name, err)
	}
	return nil
}
