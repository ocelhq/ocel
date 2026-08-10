package declare

import (
	"fmt"

	resourcesv1 "github.com/ocelhq/ocel/pkg/proto/resources/v1"
)

type Resource struct {
	Name     string
	Type     resourcesv1.ResourceType
	Postgres *resourcesv1.PostgresConfig
	Bucket   *resourcesv1.BucketConfig
}

func Parse(req *resourcesv1.DeclareRequest) (Resource, error) {
	id := req.GetResource()
	if id.GetType() == resourcesv1.ResourceType_RESOURCE_TYPE_UNSPECIFIED {
		return Resource{}, fmt.Errorf("unsupported resource type: %v", id.GetType())
	}

	return Resource{
		Name:     id.GetName(),
		Type:     id.GetType(),
		Postgres: req.GetPostgres(),
		Bucket:   req.GetBucket(),
	}, nil
}
