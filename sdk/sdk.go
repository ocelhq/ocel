// Package sdk is the Ocel Go SDK (scaffold).
//
// This stub exists to establish the module boundary: the SDK is its own Go
// module that depends only on the shared proto bindings, keeping its dependency
// footprint minimal for consumers. Real resource-declaration and dev-server
// client APIs land here later.
package sdk

import (
	resourcesv1 "github.com/ocelhq/ocel/pkg/proto/resources/v1"
)

// ResourceTypeName returns the wire name of a resource type. It exists only to
// exercise the dependency on the shared proto module; replace it with the real
// SDK surface.
func ResourceTypeName(t resourcesv1.ResourceType) string {
	return t.String()
}
