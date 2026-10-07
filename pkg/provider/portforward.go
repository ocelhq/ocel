package provider

import "github.com/ocelhq/ocel/pkg/environment"

type PortForwardRequest struct {
	Tier     environment.Tier
	Slug     string
	Env      string
	Bindings []Binding
}

type PortForward struct {
	Binding      string
	LocalAddress string
}
