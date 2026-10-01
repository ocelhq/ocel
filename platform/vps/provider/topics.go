package vps

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"google.golang.org/protobuf/encoding/protojson"
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
	queueKind       = "queue"
	queueData       = "/var/lib/postgresql"
	queueGeneration = "18"
)

type queueDatabases struct {
	sync.Mutex
	ensured map[string]bool
}

var workerCeilings = []provider.WorkerCeiling{{Compute: provider.ComputeContainer, Unbounded: true}}

func queueContainer(ref provider.StackRef) host.ResourceContainer {
	return host.ResourceContainer{
		Name:     host.ResourceName(ref.Project, ref.Name.String(), live.QueueResource, queueKind),
		Project:  ref.Project,
		Resource: live.QueueResource,
		Tier:     ref.Tier,

		Image:        images.QueueDatabase(),
		Env:          map[string]string{"POSTGRES_DB": live.QueueResource},
		Capabilities: postgresCapabilities,

		Volume: host.Volume{Path: queueData, Generation: queueGeneration},
		Credential: host.Credential{
			Env: postgresSecretEnv,
			Reassert: func(secret string) ([]string, string) {
				return []string{"psql", "-U", postgresSuperuser, "-v", "ON_ERROR_STOP=1"},
					"ALTER USER " + postgresSuperuser + " PASSWORD '" + secret + "';\n"
			},
		},
		Ready:    []string{"pg_isready", "-h", "127.0.0.1", "-U", postgresSuperuser},
		Backup:   host.BackupPostgres,
		Database: live.QueueResource,
	}
}

func (p *Provider) ProvisionTopic(ctx context.Context, in resources.ProvisionRequest, progress progress.Log) (provider.Binding, error) {
	if in.Resource.Topic == nil {
		return provider.Binding{}, refusal.Refuse(refusal.CodeInvalid,
			"%s %s reached the box with no topic config, so nothing knows how to deliver it", in.Resource.Type, in.Resource.Declared)
	}
	if err := p.ensureQueue(ctx, in.Ref, progress); err != nil {
		return provider.Binding{}, err
	}
	config, err := protojson.Marshal(manifestTopic(in.Resource.Topic))
	if err != nil {
		return provider.Binding{}, err
	}
	key := live.QueueTopicKey(in.Ref.Tier, in.Ref.Project, in.Ref.Name.Env, declaredName(in.Resource))
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

func manifestTopic(spec *provider.TopicSpec) *contractv1.ManifestTopic {
	topic := &contractv1.ManifestTopic{Schema: spec.Schema, Ordered: spec.Ordered, Ttl: durationOrNil(spec.TTL), Cron: spec.Cron}
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
			MaxDuration: durationOrNil(consumer.MaxDuration),
		}
		for _, lane := range consumer.Lanes {
			message.Lanes = append(message.Lanes, laneMessages[lane])
		}
		if consumer.Batch != nil {
			message.Batch = &resourcesv1.BatchPolicy{Size: int32(consumer.Batch.Size), Timeout: durationOrNil(consumer.Batch.Timeout)}
		}
		topic.Consumers = append(topic.Consumers, message)
	}
	return topic
}

func durationOrNil(d time.Duration) *durationpb.Duration {
	if d == 0 {
		return nil
	}
	return durationpb.New(d)
}

func declaredName(resource provider.Resource) string {
	if resource.Declared != "" {
		return resource.Declared
	}
	return resource.Name
}

func (p *Provider) ensureQueue(ctx context.Context, ref provider.StackRef, progress progress.Log) error {
	spec := queueContainer(ref)
	p.queues.Lock()
	defer p.queues.Unlock()
	if p.queues.ensured[spec.Name] {
		return nil
	}
	if progress != nil {
		progress.Say("Provisioning the queue database for " + ref.Name.Env + "'s topics and tasks in container " + spec.Name)
	}
	bound, err := live.NewQueueSecretAssociatedData(ref.Project, ref.Tier, ref.Name.String())
	if err != nil {
		return err
	}
	sealed, secret, err := p.keptSealed(ctx, ref.Tier, spec.Name, spec.Resource, bound)
	if err != nil {
		return err
	}
	if err := p.host.RunResource(ctx, spec, secret); err != nil {
		return err
	}
	record, err := json.Marshal(live.QueueDatabase{Container: spec.Name, Stack: ref.Name.String(), Sealed: base64.StdEncoding.EncodeToString(sealed)})
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

func (p *Provider) removeTopic(ctx context.Context, ref provider.StackRef, binding provider.Binding, progress progress.Log) error {
	name := binding.Resource
	if name == "" {
		name = binding.Name
	}
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
	spec := queueContainer(ref)
	if progress != nil {
		progress.Say("Removing the queue database " + spec.Name + " and its data: " + ref.Name.Env + " declares no topic or task any more")
	}
	if err := p.host.RemoveResource(ctx, host.ResourceRef{Tier: ref.Tier, Project: ref.Project, Resource: spec.Resource, Name: spec.Name}); err != nil {
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
