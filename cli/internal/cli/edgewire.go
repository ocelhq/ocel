package cli

import (
	"github.com/ocelhq/ocel/cli/internal/projectconfig"
	deploymentsv1 "github.com/ocelhq/ocel/pkg/proto/deployments/v1"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

type edgeWire struct {
	kind          edge.Kind
	options       []byte
	dns           *deploymentsv1.Dns
	allowDegraded []string
}

func edgeSettings(cfg *projectconfig.Config) edgeWire {
	wire := edgeWire{kind: edge.KindNative, allowDegraded: cfg.AllowDegraded}
	switch {
	case cfg.EdgeDisabled:
		wire.kind = edge.KindNone
	case cfg.Edge != nil:
		wire.kind, wire.options = edge.Kind(cfg.Edge.Kind), cfg.Edge.Options
	}
	if cfg.DNS != nil {
		wire.dns = &deploymentsv1.Dns{Kind: cfg.DNS.Kind, Zone: cfg.DNS.Zone}
	}
	return wire
}

func (e edgeWire) applyToDeploy(req *deploymentsv1.DeployRequest) *deploymentsv1.DeployRequest {
	req.EdgeKind, req.EdgeOptions, req.Dns, req.AllowDegraded = string(e.kind), e.options, e.dns, e.allowDegraded
	return req
}

func (e edgeWire) applyToBootstrap(req *deploymentsv1.BootstrapRequest) *deploymentsv1.BootstrapRequest {
	req.EdgeKind, req.EdgeOptions, req.Dns, req.AllowDegraded = string(e.kind), e.options, e.dns, e.allowDegraded
	return req
}
