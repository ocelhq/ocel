package provider

import "github.com/ocelhq/ocel/pkg/environment"

type PortForwardRequest struct {
	Tier          environment.Tier
	Bindings      []Binding
	ReportFailure func(error)
}

type PortForward struct {
	Binding      string
	LocalAddress string
	Close        func()
}

type BindingProxyRequest struct {
	Tier          environment.Tier
	Bindings      []Binding
	ReportFailure func(error)
}

type BindingProxy struct {
	Address      string
	SessionToken string
	Close        func()
}
