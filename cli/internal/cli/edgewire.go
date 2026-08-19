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
	wire := edgeWire{kind: cfg.EdgeKind(), allowDegraded: cfg.AllowDegraded}
	if cfg.Edge != nil {
		wire.options = cfg.Edge.Options
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

func (e edgeWire) applyToPreflight(req *deploymentsv1.PreflightRequest) *deploymentsv1.PreflightRequest {
	req.EdgeKind = string(e.kind)
	return req
}

func (e edgeWire) applyToUseDomain(req *deploymentsv1.UseDomainRequest) *deploymentsv1.UseDomainRequest {
	req.EdgeKind, req.Dns = string(e.kind), e.dns
	return req
}

func (e edgeWire) applyToListDomain(req *deploymentsv1.ListDomainRequest) *deploymentsv1.ListDomainRequest {
	req.EdgeKind = string(e.kind)
	return req
}

func (e edgeWire) applyToPlanReleaseDomain(req *deploymentsv1.PlanReleaseDomainRequest) *deploymentsv1.PlanReleaseDomainRequest {
	req.EdgeKind = string(e.kind)
	return req
}

func (e edgeWire) applyToReleaseDomain(req *deploymentsv1.ReleaseDomainRequest) *deploymentsv1.ReleaseDomainRequest {
	req.EdgeKind, req.Dns = string(e.kind), e.dns
	return req
}

func (e edgeWire) applyToAddDomain(req *deploymentsv1.AddDomainRequest) *deploymentsv1.AddDomainRequest {
	req.EdgeKind, req.Dns = string(e.kind), e.dns
	return req
}

func (e edgeWire) applyToRemoveDomain(req *deploymentsv1.RemoveDomainRequest) *deploymentsv1.RemoveDomainRequest {
	req.EdgeKind, req.Dns = string(e.kind), e.dns
	return req
}

func (e edgeWire) applyToDomainStatus(req *deploymentsv1.DomainStatusRequest) *deploymentsv1.DomainStatusRequest {
	req.EdgeKind, req.Dns = string(e.kind), e.dns
	return req
}

func (e edgeWire) applyToPlanDestroyProject(req *deploymentsv1.PlanDestroyProjectRequest) *deploymentsv1.PlanDestroyProjectRequest {
	req.EdgeKind = string(e.kind)
	return req
}

func (e edgeWire) applyToDestroyProject(req *deploymentsv1.DestroyProjectRequest) *deploymentsv1.DestroyProjectRequest {
	req.EdgeKind, req.Dns = string(e.kind), e.dns
	return req
}

func (e edgeWire) applyToDestroyPreview(req *deploymentsv1.DestroyPreviewRequest) *deploymentsv1.DestroyPreviewRequest {
	req.EdgeKind = string(e.kind)
	return req
}

func (e edgeWire) applyToListPromotions(req *deploymentsv1.ListPromotionsRequest) *deploymentsv1.ListPromotionsRequest {
	req.EdgeKind = string(e.kind)
	return req
}

func (e edgeWire) applyToRollback(req *deploymentsv1.RollbackRequest) *deploymentsv1.RollbackRequest {
	req.EdgeKind = string(e.kind)
	return req
}

func (e edgeWire) applyToPrune(req *deploymentsv1.PruneRequest) *deploymentsv1.PruneRequest {
	req.EdgeKind = string(e.kind)
	return req
}

func (e edgeWire) applyToPlanTeardown(req *deploymentsv1.PlanTeardownRequest) *deploymentsv1.PlanTeardownRequest {
	req.EdgeKind = string(e.kind)
	return req
}

func (e edgeWire) applyToTeardown(req *deploymentsv1.TeardownRequest) *deploymentsv1.TeardownRequest {
	req.EdgeKind = string(e.kind)
	return req
}
