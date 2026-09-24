package gcp

import (
	"context"
	"slices"

	"github.com/ocelhq/ocel/pkg/providerkit"
	cloudflare "github.com/ocelhq/ocel/platform/edge/cloudflare/deploy"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
	"github.com/ocelhq/ocel/platform/gcp/provider/direct"
	"github.com/ocelhq/ocel/platform/gcp/provider/edges/alb"
	"github.com/ocelhq/ocel/platform/gcp/provider/pin"
	"github.com/ocelhq/ocel/platform/gcp/provider/ports"
)

type edges struct {
	namespace providerkit.Namespace
	records   providerkit.RecordStore
	pins      pin.Pins
	stacks    alb.Stacks
	routes    alb.Routes
	entries   alb.Entries
	project   string
	region    string
}

var supportedEdges = []edge.Kind{direct.Kind, alb.Kind}

func (edges) Supported() []edge.Kind { return slices.Clone(supportedEdges) }

func (edges) Default() edge.Kind { return direct.Kind }

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
		return nil, providerkit.Refuse(providerkit.CodeInvalid,
			"this provider cannot front deployments with the %q edge yet: that edge answers every request from a worker it runs, "+
				"and nothing here builds the program that worker would run, so a bootstrap of it would stand resources no deploy could use.\n"+
				"Front them with %s, which answers on the url Cloud Run gives each service, or with %s, which stands one load balancer up per bootstrap class at %s",
			kind, direct.Kind, alb.Kind, alb.StandingCost)
	}
	return nil, providerkit.Refuse(providerkit.CodeInvalid,
		"this provider cannot front deployments with the %q edge; it fronts them with %s, which answers on the url Cloud Run gives each service, "+
			"and with %s, which stands one load balancer up per bootstrap class at %s",
		kind, direct.Kind, alb.Kind, alb.StandingCost)
}

type records struct{ p *Provider }

func (r records) stood(ctx context.Context) (ports.Records, error) {
	held, err := r.p.stood(ctx)
	if err != nil {
		return ports.Records{}, err
	}
	return ports.Records{Clients: held.Runtime()}, nil
}

func (r records) Read(ctx context.Context, name providerkit.RecordName) (providerkit.Record, error) {
	held, err := r.stood(ctx)
	if err != nil {
		return providerkit.Record{}, err
	}
	return held.Read(ctx, name)
}

func (r records) Write(ctx context.Context, record providerkit.Record) (providerkit.Revision, error) {
	held, err := r.stood(ctx)
	if err != nil {
		return "", err
	}
	return held.Write(ctx, record)
}

func (r records) WritePair(ctx context.Context, first, second providerkit.Record) error {
	held, err := r.stood(ctx)
	if err != nil {
		return err
	}
	return held.WritePair(ctx, first, second)
}

func (r records) Remove(ctx context.Context, name providerkit.RecordName, expected providerkit.Revision) error {
	held, err := r.stood(ctx)
	if err != nil {
		return err
	}
	return held.Remove(ctx, name, expected)
}

func (r records) List(ctx context.Context, under providerkit.RecordName) ([]providerkit.Record, error) {
	held, err := r.stood(ctx)
	if err != nil {
		return nil, err
	}
	return held.List(ctx, under)
}

type sealer struct{ p *Provider }

func (s sealer) stood(ctx context.Context) (ports.Sealer, error) {
	held, err := s.p.stood(ctx)
	if err != nil {
		return ports.Sealer{}, err
	}
	return ports.Sealer{Clients: held.Runtime()}, nil
}

func (s sealer) Seal(ctx context.Context, at providerkit.Coordinate, plaintext []byte) ([]byte, error) {
	held, err := s.stood(ctx)
	if err != nil {
		return nil, err
	}
	return held.Seal(ctx, at, plaintext)
}

func (s sealer) Open(ctx context.Context, at providerkit.Coordinate, sealed []byte) ([]byte, error) {
	held, err := s.stood(ctx)
	if err != nil {
		return nil, err
	}
	return held.Open(ctx, at, sealed)
}

type dns struct{}

const dnsCloudflare = providerkit.DNSKind(cloudflare.Kind)

func (dns) Supported() []providerkit.DNSKind { return []providerkit.DNSKind{dnsCloudflare} }

func (dns) Default() providerkit.DNSKind { return "" }

func (dns) Open(kind providerkit.DNSKind, zone string, _ edge.Kind) (edge.DNSWriter, error) {
	if kind != dnsCloudflare {
		return nil, providerkit.Refuse(providerkit.CodeInvalid,
			"this provider cannot write DNS records with %q; it writes them with %s", kind, dnsCloudflare)
	}
	writer, err := cloudflare.NewDNS(zone)
	if err != nil {
		return nil, providerkit.Refuse(providerkit.CodeInvalid, "%s", err)
	}
	return writer, nil
}

var (
	_ providerkit.Bootstrapper  = bootstrapper{}
	_ providerkit.ArtifactStore = artifacts{}
	_ providerkit.RecordStore   = ports.Records{}
	_ providerkit.Sealer        = ports.Sealer{}
	_ providerkit.EdgeRegistry  = edges{}
	_ providerkit.DNSRegistry   = dns{}
)
