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
	Slug          string
	Tier          environment.Tier
	Env           string
	Grants        []BindingGrant
	ReportFailure func(error)
}

type BindingGrant struct {
	Grantee  string
	Bindings []Binding
}

type BindingProxy struct {
	Address  string
	Sessions []BindingSession
	Unserved []string
	Close    func()
}

type BindingSession struct {
	Grantee      string
	SessionToken string
}
