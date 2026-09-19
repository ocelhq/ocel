package resolve

import (
	resourcesv1 "github.com/ocelhq/ocel/pkg/proto/app/resources/v1"
)

type Resource struct {
	Name   string
	Type   resourcesv1.ResourceType
	Env    map[string]string
	Origin string
}
