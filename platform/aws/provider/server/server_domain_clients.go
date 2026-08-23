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

type domainClients struct {
	region       string
	ssm          bootstrap.SSMAPI
	cfn          bootstrap.CFNDescriber
	poller       dns.Poller
	prober       certs.Prober
	certifierFor func(edge.Edge) certs.Certifier
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
		certifierFor: func(front edge.Edge) certs.Certifier {
			return certs.CertifierFor(front, certs.Deps{AWS: awscfg}, s.config.get().Certificates)
		},
		discarderFor: func(cert certs.Certificate) certs.Issuer {
			return certs.DiscardIssuerFor(cert, certs.Deps{AWS: awscfg})
		},
		writerFor: func(kind, zone string) (edge.DNSWriter, error) {
			return dns.WriterFor(kind, zone, dns.Deps{AWS: awscfg})
		},
	}, nil
}
