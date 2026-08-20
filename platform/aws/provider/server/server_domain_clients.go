package server

import (
	"context"

	"github.com/aws/aws-sdk-go-v2/service/cloudformation"
	"github.com/aws/aws-sdk-go-v2/service/ssm"

	"github.com/ocelhq/ocel/platform/aws/provider/bootstrap"
	"github.com/ocelhq/ocel/platform/aws/provider/certs"
	"github.com/ocelhq/ocel/platform/aws/provider/dns"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

type domainSSM interface {
	bootstrap.SSMAPI
	bootstrap.SSMPathAPI
}

type domainClients struct {
	region       string
	ssm          domainSSM
	cfn          bootstrap.CFNDescriber
	poller       dns.Poller
	prober       certs.Prober
	issuerFor    func(edge.Edge) certs.Issuer
	discarderFor func(certs.Certificate) certs.Issuer
	writerFor    func(kind, zone string) (edge.DNSWriter, error)
}

func (s *Server) domainClients(ctx context.Context, region string) (domainClients, error) {
	if s.openDomain != nil {
		return s.openDomain(ctx, region)
	}
	awscfg, err := loadAWS(ctx, region)
	if err != nil {
		return domainClients{}, err
	}
	return domainClients{
		region: awscfg.Region,
		ssm:    ssm.NewFromConfig(awscfg),
		cfn:    cloudformation.NewFromConfig(awscfg),
		poller: dns.NewPoller(),
		prober: certs.NewProber(),
		issuerFor: func(front edge.Edge) certs.Issuer {
			return certs.IssuerFor(front, certs.Deps{AWS: awscfg})
		},
		discarderFor: func(cert certs.Certificate) certs.Issuer {
			return certs.DiscardIssuerFor(cert, certs.Deps{AWS: awscfg})
		},
		writerFor: func(kind, zone string) (edge.DNSWriter, error) {
			return dns.WriterFor(kind, zone, dns.Deps{AWS: awscfg})
		},
	}, nil
}
