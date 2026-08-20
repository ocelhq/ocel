package declare

import (
	"fmt"

	"github.com/ocelhq/ocel/pkg/naming"
	resourcesv1 "github.com/ocelhq/ocel/pkg/proto/app/resources/v1"
	linksv1 "github.com/ocelhq/ocel/pkg/proto/common/links/v1"
)

type Resource struct {
	Name     string
	Type     linksv1.LinkType
	Postgres *resourcesv1.PostgresConfig
	Bucket   *resourcesv1.BucketConfig
	Stack    string
}

func Parse(req *resourcesv1.DeclareRequest) (Resource, error) {
	id := req.GetResource()
	if _, ok := naming.KindOf(id.GetType()); !ok {
		return Resource{}, fmt.Errorf("unsupported resource type: %s", id.GetType())
	}
	if !configMatches(req, id.GetType()) {
		return Resource{}, fmt.Errorf("resource %s declares itself a %s but carries %s config", id.GetName(), id.GetType(), configName(req))
	}

	return Resource{
		Name:     id.GetName(),
		Type:     id.GetType(),
		Postgres: req.GetPostgres(),
		Bucket:   req.GetBucket(),
		Stack:    req.GetStack(),
	}, nil
}

func configMatches(req *resourcesv1.DeclareRequest, t linksv1.LinkType) bool {
	switch t {
	case linksv1.LinkType_LINK_TYPE_POSTGRES:
		return req.GetPostgres() != nil
	case linksv1.LinkType_LINK_TYPE_BUCKET:
		return req.GetBucket() != nil
	}
	return false
}

func configName(req *resourcesv1.DeclareRequest) string {
	switch req.GetConfig().(type) {
	case *resourcesv1.DeclareRequest_Postgres:
		return "postgres"
	case *resourcesv1.DeclareRequest_Bucket:
		return "bucket"
	}
	return "no"
}
