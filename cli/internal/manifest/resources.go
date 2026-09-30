package manifest

import (
	"fmt"
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
	Source   string
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
			Source:   source,
		}
	}
	return declared
}

func manifestResources(declarations []declaredResource, topics map[string]*contractv1.ManifestTopic, named map[string]string) ([]*contractv1.ManifestResource, map[identity]declaredResource, error) {
	seen := make(map[identity]declaredResource, len(declarations))
	resources := make([]*contractv1.ManifestResource, 0, len(declarations))
	for _, d := range declarations {
		declaredKind, topicTaskOrWorker := topicTaskAndWorkerKinds[d.Type]
		if topicTaskOrWorker && !declaredKind.declaresTopic {
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
