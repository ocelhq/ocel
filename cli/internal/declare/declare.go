package declare

import (
	"fmt"

	"github.com/ocelhq/ocel/pkg/naming"
	resourcesv1 "github.com/ocelhq/ocel/pkg/proto/app/resources/v1"
)

type Resource struct {
	Name     string
	Type     resourcesv1.ResourceType
	Postgres *resourcesv1.PostgresConfig
	Bucket   *resourcesv1.BucketConfig
	Source   string
}

type Reference struct {
	Type   resourcesv1.ResourceType
	Name   string
	Source string
}

type Collected struct {
	Resources  []Resource
	References []Reference
}

func ParseReference(req *resourcesv1.ReferenceRequest) (Reference, error) {
	id := req.GetResource()
	if _, ok := naming.BindableAs(id.GetType()); !ok {
		return Reference{}, fmt.Errorf("unsupported resource type: %s", id.GetType())
	}
	if id.GetName() == "" {
		return Reference{}, fmt.Errorf("a reference to a %s names no resource", id.GetType())
	}
	if req.GetSource() == "" {
		return Reference{}, fmt.Errorf("the reference to the %s %q names no source file, so a deploy could not say where it was written", id.GetType(), id.GetName())
	}
	return Reference{Type: id.GetType(), Name: id.GetName(), Source: req.GetSource()}, nil
}

func Parse(req *resourcesv1.DeclareRequest) (Resource, error) {
	id := req.GetResource()
	if _, ok := naming.BindableAs(id.GetType()); !ok {
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
		Source:   req.GetSource(),
	}, nil
}

func configMatches(req *resourcesv1.DeclareRequest, t resourcesv1.ResourceType) bool {
	switch t {
	case resourcesv1.ResourceType_RESOURCE_TYPE_POSTGRES:
		return req.GetPostgres() != nil
	case resourcesv1.ResourceType_RESOURCE_TYPE_BUCKET:
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
