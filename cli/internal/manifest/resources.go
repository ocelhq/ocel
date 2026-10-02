package manifest

import (
	"fmt"
	"regexp"
	"slices"
	"strings"

	"github.com/ocelhq/ocel/cli/internal/attribution"
	"github.com/ocelhq/ocel/cli/internal/declaration"
	"github.com/ocelhq/ocel/pkg/naming"
	resourcesv1 "github.com/ocelhq/ocel/pkg/proto/app/resources/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
)

type declaredResource struct {
	Type     resourcesv1.ResourceType
	Name     string
	Postgres *resourcesv1.PostgresConfig
	Bucket   *resourcesv1.BucketConfig
	Topic    *resourcesv1.TopicConfig
	Task     *resourcesv1.TaskConfig
	Consumer *resourcesv1.ConsumerConfig
	Worker   *resourcesv1.WorkerConfig
	KV       *resourcesv1.KvConfig
	Realtime *resourcesv1.RealtimeConfig
	Source   string

	KVEntries        []declaredEntry
	RealtimeChannels []declaredChannel
}

type declarationKind struct {
	namespace     string
	declaresTopic bool
	consumer      func(d declaredResource) *contractv1.ManifestConsumer
	refuseLimits  func(d declaredResource) error
}

var kinds = map[resourcesv1.ResourceType]declarationKind{
	resourcesv1.ResourceType_RESOURCE_TYPE_TOPIC: {
		namespace: "topic", declaresTopic: true, refuseLimits: refuseTopicLimits,
	},
	resourcesv1.ResourceType_RESOURCE_TYPE_TASK: {
		namespace: "topic", declaresTopic: true, consumer: taskConsumer, refuseLimits: refuseTaskLimits,
	},
	resourcesv1.ResourceType_RESOURCE_TYPE_CONSUMER: {
		namespace: "consumer", consumer: topicConsumer, refuseLimits: refuseConsumerLimits,
	},
	resourcesv1.ResourceType_RESOURCE_TYPE_WORKER: {
		namespace: "worker", refuseLimits: refuseWorkerLimits,
	},
}

type namespacedName struct {
	namespace string
	name      string
}

func (d declaredResource) label() string {
	if d.Type == resourcesv1.ResourceType_RESOURCE_TYPE_CONSUMER {
		return fmt.Sprintf("consumer %q of topic %q", d.Name, d.Consumer.GetTopic())
	}
	return fmt.Sprintf("%s %q", naming.ResourceTypeName(d.Type), d.Name)
}

func (d declaredResource) namespaced(kind declarationKind) namespacedName {
	if d.Type == resourcesv1.ResourceType_RESOURCE_TYPE_CONSUMER {
		return namespacedName{kind.namespace, d.Consumer.GetTopic() + "/" + d.Name}
	}
	return namespacedName{kind.namespace, d.Name}
}

type identity struct {
	typ  resourcesv1.ResourceType
	name string
}

type DuplicateError struct {
	TypeToken    string
	Name         string
	FirstSource  string
	SecondSource string
}

func (e *DuplicateError) Error() string {
	return fmt.Sprintf(
		"duplicate resource declaration for type=%s name=%q: declared at %s and %s",
		e.TypeToken, e.Name, sourceOrUnknown(e.FirstSource), sourceOrUnknown(e.SecondSource),
	)
}

type InvalidDeclarationError struct {
	Source string
	Reason string
}

func (e *InvalidDeclarationError) Error() string {
	return sourceOrUnknown(e.Source) + ": " + e.Reason
}

const maxNameBytes = 63

var namePattern = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)

func refuseInvalidName(d declaredResource, name string) error {
	if len(name) <= maxNameBytes && namePattern.MatchString(name) {
		return nil
	}
	return refuse(d, "is named %q, and a name is lowercase letters and digits in words joined by single hyphens, at most %d characters", name, maxNameBytes)
}

func refuseDuplicate(prior, d declaredResource, key namespacedName) error {
	if first, second := prior.consumer(), d.consumer(); first != nil && prior.Type == d.Type && first.GetWorker() != second.GetWorker() {
		return &ServedTwiceError{
			Subject:      d.label(),
			FirstSource:  prior.Source,
			FirstWorker:  first.GetWorker(),
			SecondSource: d.Source,
			SecondWorker: second.GetWorker(),
		}
	}
	return &DuplicateError{TypeToken: key.namespace, Name: key.name, FirstSource: prior.Source, SecondSource: d.Source}
}

func refuse(d declaredResource, format string, args ...any) error {
	return &InvalidDeclarationError{Source: d.Source, Reason: d.label() + " " + fmt.Sprintf(format, args...)}
}

func refuseAtDeclaration(d declaredResource, err error) error {
	if err == nil {
		return nil
	}
	return refuse(d, "%v", err)
}

func firstRefusal(errs ...error) error {
	for _, err := range errs {
		if err != nil {
			return err
		}
	}
	return nil
}

func declaredResources(configDir string, resources []declaration.Resource) []declaredResource {
	declared := make([]declaredResource, len(resources))
	for i, r := range resources {
		var source string
		if site, ok := attribution.DeclaringSite(configDir, r.Source); ok {
			source = site.String()
		}
		declared[i] = declaredResource{
			Type:     r.Type,
			Name:     r.Name,
			Postgres: r.Postgres,
			Bucket:   r.Bucket,
			Topic:    r.Topic,
			Task:     r.Task,
			Consumer: r.Consumer,
			Worker:   r.Worker,
			KV:       r.KV,
			Realtime: r.Realtime,
			Source:   source,
		}
		if r.KV != nil {
			declared[i].KVEntries = declaredEntries(configDir, r.KV, source)
		}
		if r.Realtime != nil {
			declared[i].RealtimeChannels = declaredChannels(configDir, r.Realtime, source)
		}
	}
	return declared
}

func manifestResources(declarations []declaredResource, topics map[string]*contractv1.ManifestTopic, named map[string]string) ([]*contractv1.ManifestResource, map[identity]declaredResource, error) {
	seen := make(map[identity]declaredResource, len(declarations))
	resources := make([]*contractv1.ManifestResource, 0, len(declarations))
	for _, d := range declarations {
		declaredKind, listed := kinds[d.Type]
		if listed && !declaredKind.declaresTopic {
			continue
		}
		if d.Name == "" {
			return nil, nil, fmt.Errorf("a resource declaration names no resource")
		}

		kind, err := typeKind(d.Type)
		if err != nil {
			return nil, nil, err
		}

		key := identity{d.Type, d.Name}
		if prior, ok := seen[key]; ok {
			return nil, nil, &DuplicateError{
				TypeToken:    string(kind),
				Name:         d.Name,
				FirstSource:  prior.Source,
				SecondSource: d.Source,
			}
		}
		seen[key] = d

		logical := resourceLogicalName(kind, d.Name)
		described := fmt.Sprintf("%s %q declared at %s", kind, d.Name, sourceOrUnknown(d.Source))
		if prior, ok := named[logical]; ok {
			return nil, nil, &CollisionError{LogicalName: logical, First: prior, Second: described}
		}
		named[logical] = described

		resource := &contractv1.ManifestResource{
			LogicalName: logical,
			Resource:    &resourcesv1.ResourceIdentifier{Type: d.Type, Name: d.Name},
		}
		if d.Postgres != nil {
			resource.Config = &contractv1.ManifestResource_Postgres{Postgres: d.Postgres}
		}
		if d.Bucket != nil {
			resource.Config = &contractv1.ManifestResource_Bucket{Bucket: d.Bucket}
		}
		if d.KV != nil {
			if err := firstRefusal(refuseInvalidName(d, d.Name), refuseKVStore(d)); err != nil {
				return nil, nil, err
			}
			resource.Config = &contractv1.ManifestResource_Kv{Kv: manifestKV(d)}
		}
		if d.Realtime != nil {
			config := manifestRealtime(d)
			if err := firstRefusal(refuseInvalidName(d, d.Name), refuseRealtime(d, config)); err != nil {
				return nil, nil, err
			}
			resource.Config = &contractv1.ManifestResource_Realtime{Realtime: config}
		}
		if topic, found := topics[d.Name]; found && declaredKind.declaresTopic {
			resource.Config = &contractv1.ManifestResource_Topic{Topic: topic}
		}
		resources = append(resources, resource)
	}
	slices.SortFunc(resources, func(a, b *contractv1.ManifestResource) int {
		return strings.Compare(a.GetLogicalName(), b.GetLogicalName())
	})
	return resources, seen, nil
}

func sourceOrUnknown(source string) string {
	if source == "" {
		return "<unknown source>"
	}
	return source
}

func typeKind(t resourcesv1.ResourceType) (naming.Kind, error) {
	bound, bindable := naming.BindableAs(t)
	if !bindable {
		return "", fmt.Errorf("unsupported resource type %s", t)
	}
	kind, ok := naming.KindOf(bound)
	if !ok {
		return "", fmt.Errorf("unsupported resource type %s", t)
	}
	return kind, nil
}

func resourceLogicalName(kind naming.Kind, name string) string {
	return naming.Join(naming.FieldSeparator, string(kind), name)
}
