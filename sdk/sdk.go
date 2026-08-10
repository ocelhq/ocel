package sdk

import (
	resourcesv1 "github.com/ocelhq/ocel/pkg/proto/resources/v1"
)

func ResourceTypeName(t resourcesv1.ResourceType) string {
	return t.String()
}
