package provider

import "github.com/ocelhq/ocel/pkg/environment"

type PortForwardRequest struct {
	Tier     environment.Tier
	Bindings []Binding
}

type PortForward struct {
	Binding      string
	LocalAddress string
	Close        func()
}
