package resolve

import (
	resourcesv1 "github.com/ocelhq/ocel/pkg/proto/app/resources/v1"
)

type Account struct {
	ProjectID string
	EnvVars   map[string]string
}

type Resource struct {
	Name   string
	Type   resourcesv1.ResourceType
	Env    map[string]string
	Origin string
}
