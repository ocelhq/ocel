package certs

import (
	"net/http"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/acm"

	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

const (
	CloudFrontRegion = "us-east-1"
	acmCallTimeout   = 30 * time.Second
)

type Deps struct {
	AWS  aws.Config
	HTTP *http.Client
}

func (d Deps) http() *http.Client {
	if d.HTTP != nil {
		return d.HTTP
	}
	return &http.Client{Timeout: acmCallTimeout}
}

type OriginCertifier interface {
	CertificateRegion(apiRegion string) string
}

func RegionFor(front edge.Edge, apiRegion string) string {
	certifier, ok := front.(OriginCertifier)
	if !ok {
		return ""
	}
	return certifier.CertificateRegion(apiRegion)
}

func IssuerFor(front edge.Edge, deps Deps) Issuer {
	region := RegionFor(front, deps.AWS.Region)
	if region == "" {
		return Issuer{}
	}
	return newIssuer(deps, region)
}

func DiscardIssuerFor(cert Certificate, deps Deps) Issuer {
	if cert.ARN == "" {
		return Issuer{}
	}
	region := cert.Region
	if region == "" {
		region = deps.AWS.Region
	}
	return newIssuer(deps, region)
}

func newIssuer(deps Deps, region string) Issuer {
	return Issuer{
		API: acm.NewFromConfig(deps.AWS, func(o *acm.Options) {
			o.Region = region
			o.HTTPClient = deps.http()
		}),
		Region:   region,
		Wait:     waitFor,
		Attempts: issueAttempts,
		Every:    issueEvery,
	}
}
