package declare

import (
	"fmt"

	"github.com/ocelhq/ocel/pkg/naming"
	resourcesv1 "github.com/ocelhq/ocel/pkg/proto/resources/v1"
)

type Resource struct {
	Name     string
	Type     string
	Postgres *resourcesv1.PostgresConfig
	Bucket   *resourcesv1.BucketConfig
	Stack    string
}

func Parse(req *resourcesv1.DeclareRequest) (Resource, error) {
	id := req.GetResource()
	if _, ok := naming.TokenKind(id.GetType()); !ok {
		return Resource{}, fmt.Errorf("unsupported resource type: %q", id.GetType())
	}

	return Resource{
		Name:     id.GetName(),
		Type:     id.GetType(),
		Postgres: req.GetPostgres(),
		Bucket:   req.GetBucket(),
		Stack:    req.GetStack(),
	}, nil
}
