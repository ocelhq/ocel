package edge

import "context"

type Tunnel struct {
	ID      string
	Address string
}

type TunnelConnector struct {
	Image                string
	Command              []string
	TokenFileEnv         string
	Ready                []string
	VisitorAddressHeader string
	VisitorSchemeHeader  string
}

type TunnelHooks struct {
	Connector TunnelConnector
	Ensure    func(ctx context.Context, name string) (Tunnel, error)
	Configure func(ctx context.Context, id, service string) error
	ReadToken func(ctx context.Context, id string) (string, error)
	Delete    func(ctx context.Context, id string) error
}
