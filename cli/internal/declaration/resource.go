package declaration

import (
	"fmt"

	"google.golang.org/protobuf/reflect/protoreflect"

	"github.com/ocelhq/ocel/pkg/naming"
	resourcesv1 "github.com/ocelhq/ocel/pkg/proto/app/resources/v1"
)

type Resource struct {
	Name     string
	Type     resourcesv1.ResourceType
	Postgres *resourcesv1.PostgresConfig
	Bucket   *resourcesv1.BucketConfig
	Topic    *resourcesv1.TopicConfig
	Task     *resourcesv1.TaskConfig
	Consumer *resourcesv1.ConsumerConfig
	Worker   *resourcesv1.WorkerConfig
	KV       *resourcesv1.KvConfig
	Realtime *resourcesv1.RealtimeConfig
	Source   string
}

func Parse(req *resourcesv1.DeclareRequest) (Resource, error) {
	id := req.GetResource()
	configs := req.ProtoReflect().Descriptor().Oneofs().ByName("config")
	expected := configs.Fields().ByName(protoreflect.Name(naming.ResourceTypeName(id.GetType())))
	if expected == nil {
		return Resource{}, fmt.Errorf("unsupported resource type: %s", id.GetType())
	}
	if got := req.ProtoReflect().WhichOneof(configs); got != expected {
		return Resource{}, fmt.Errorf("resource %s declares itself a %s but has %s config", id.GetName(), id.GetType(), configName(got))
	}

	return Resource{
		Name:     id.GetName(),
		Type:     id.GetType(),
		Postgres: req.GetPostgres(),
		Bucket:   req.GetBucket(),
		Topic:    req.GetTopic(),
		Task:     req.GetTask(),
		Consumer: req.GetConsumer(),
		Worker:   req.GetWorker(),
		KV:       req.GetKv(),
		Realtime: req.GetRealtime(),
		Source:   req.GetSource(),
	}, nil
}

func configName(field protoreflect.FieldDescriptor) string {
	if field == nil {
		return "no"
	}
	return string(field.Name())
}
