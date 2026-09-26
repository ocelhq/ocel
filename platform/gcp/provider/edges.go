package gcp

import (
	"github.com/ocelhq/ocel/pkg/providerkit/provider"
	"github.com/ocelhq/ocel/pkg/providerkit/records"
	"github.com/ocelhq/ocel/pkg/providerkit/refusal"
	cloudflare "github.com/ocelhq/ocel/platform/edge/cloudflare/deploy"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
	"github.com/ocelhq/ocel/platform/gcp/provider/direct"
	"github.com/ocelhq/ocel/platform/gcp/provider/edges/alb"
	"github.com/ocelhq/ocel/platform/gcp/provider/pin"
)

type edges struct {
	namespace provider.Namespace
	records   records.Store
	pins      pin.Pins
	stacks    alb.Stacks
	routes    alb.Routes
	entries   alb.Entries
	project   string
	region    string
}

var supportedEdges = []edge.Kind{direct.Kind, alb.Kind}

func (e edges) Open(kind edge.Kind) (edge.Edge, error) {
	switch kind {
	case direct.Kind:
		return direct.New(e.records, e.pins), nil
	case alb.Kind:
		return alb.New(alb.Deps{
			Records: e.records,
			Stacks:  e.stacks,
			Routes:  e.routes,
			Entries: e.entries,
			Pins:    e.pins,
			Project: e.project,
			Region:  e.region,
		}), nil
	case cloudflare.Kind:
		return nil, refusal.Refuse(refusal.CodeInvalid,
			"this provider cannot front deployments with the %q edge yet: that edge answers every request from a worker it runs, "+
				"and nothing here builds the program that worker would run, so a bootstrap of it would stand resources no deploy could use.\n"+
				"Front them with %s, which answers on the url Cloud Run gives each service, or with %s, which stands one load balancer up per bootstrap class at %s",
			kind, direct.Kind, alb.Kind, alb.BaselineCost)
	}
	return nil, refusal.Refuse(refusal.CodeInvalid,
		"this provider cannot front deployments with the %q edge; it fronts them with %s, which answers on the url Cloud Run gives each service, "+
			"and with %s, which stands one load balancer up per bootstrap class at %s",
		kind, direct.Kind, alb.Kind, alb.BaselineCost)
}
