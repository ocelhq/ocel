// Package declare parses and validates a resources.v1 DeclareRequest into a
// validated resource record. It is the single unit that both the dev server
// and the deploy collector call, so the two can only ever diverge in what
// happens after a Declare, never in how a Declare is understood. This
// package depends only on the resources/v1 proto bindings, so it can be
// imported without pulling in dev-server or provisioning concerns.
package declare

import (
	"fmt"

	resourcesv1 "github.com/ocelhq/ocel/pkg/proto/resources/v1"
)

// Resource is a declared resource, parsed and validated from a
// resources.v1 DeclareRequest. Exactly one typed config field is set,
// matching the DeclareRequest config oneof.
type Resource struct {
	Name     string
	Type     resourcesv1.ResourceType
	Postgres *resourcesv1.PostgresConfig
}

// Parse validates req and returns the resource record it declares.
func Parse(req *resourcesv1.DeclareRequest) (Resource, error) {
	id := req.GetResource()
	if id.GetType() == resourcesv1.ResourceType_RESOURCE_TYPE_UNSPECIFIED {
		return Resource{}, fmt.Errorf("unsupported resource type: %v", id.GetType())
	}

	return Resource{
		Name:     id.GetName(),
		Type:     id.GetType(),
		Postgres: req.GetPostgres(),
	}, nil
}
