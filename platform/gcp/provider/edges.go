package gcp

import (
	"context"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/keyvalue"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/refusal"
	cloudflare "github.com/ocelhq/ocel/platform/edge/cloudflare/deploy"
	"github.com/ocelhq/ocel/platform/gcp/provider/cloudrun"
	"github.com/ocelhq/ocel/platform/gcp/provider/edges/alb"
	"github.com/ocelhq/ocel/platform/gcp/provider/pin"
)

//go:generate go generate -C ../../edge/cloudflare/deploy ./...

type edges struct {
	namespace provider.Namespace
	keyValues keyvalue.Store
	pins      pin.Pins
	stacks    alb.Stacks
	routes    alb.Routes
	entries   alb.Entries
	warmURL   func(ctx context.Context, url, address string) error
	project   string
	region    string

	certificates  provider.Certificates
	readWorkerCAs func(ctx context.Context, tier environment.Tier) ([]string, error)
}

var supportedEdges = []edge.Kind{alb.Kind, cloudflare.Kind}

func (p *Provider) edges() edges {
	return edges{
		namespace: p.namespace,
		keyValues: p.KeyValues(),
		pins:      p,
		stacks:    albStacks{p: p},
		routes:    p,
		entries:   p,
		warmURL:   p.warmThrough,
		project:   p.options.Project,
		region:    p.options.Region,

		certificates:  p.Certificates(),
		readWorkerCAs: p.readWorkerCAs,
	}
}

func (e edges) openCloudRun() *cloudrun.Edge { return cloudrun.New(e.pins) }

func (e edges) openALB() *alb.Edge {
	return alb.New(alb.Deps{
		KeyValues: e.keyValues,
		Stacks:    e.stacks,
		Routes:    e.routes,
		Entries:   e.entries,
		Pins:      e.pins,
		WarmURL:   e.warmURL,
		Project:   e.project,
		Region:    e.region,
	})
}

func (e edges) Open(kind edge.Kind, options provider.Options) (edge.Edge, error) {
	switch kind {
	case edge.None:
		return e.openCloudRun(), nil
	case alb.Kind:
		if _, err := provider.DecodeEdgeOptions[alb.Options](alb.Kind, options); err != nil {
			return nil, err
		}
		return e.openALB(), nil
	case cloudflare.Kind:
		decoded, err := cloudflare.DecodeOptions(options)
		if err != nil {
			return nil, err
		}
		shielded := e.openALB().Shielded()
		wildcards := originWildcards{
			keyValues:     e.keyValues,
			certificates:  e.certificates,
			openDNS:       func() edge.DNSRecords { return cloudflare.NewDNS("") },
			balancer:      shielded,
			readWorkerCAs: e.readWorkerCAs,
		}
		return cloudflareFront{
			Edge:      cloudflare.NewWithWorkerClientCertificate(string(e.namespace), decoded, wildcards.trustWorkerClientCertificate),
			proxy:     cloudflare.NewProxy(string(e.namespace), decoded),
			origin:    shielded,
			options:   decoded,
			wildcards: wildcards,
		}, nil
	}
	return nil, refusal.Refuse(refusal.CodeInvalid,
		"this provider cannot front deployments with the %q edge: leave `edge` out, and each service answers on the url Cloud Run gives it, "+
			"or name %s, which provisions one load balancer per bootstrap tier at %s",
		kind, alb.Kind, alb.BaselineCost)
}
