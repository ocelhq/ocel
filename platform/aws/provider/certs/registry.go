package certs

import (
	"net/http"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/acm"
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

func ACMFor(region string, deps Deps) ACM {
	if region == "" {
		return ACM{}
	}
	return newACM(deps, region)
}

func DiscardACMFor(cert Certificate, deps Deps) ACM {
	if cert.ARN == "" {
		return ACM{}
	}
	region := cert.Region
	if region == "" {
		region = deps.AWS.Region
	}
	return newACM(deps, region)
}

func newACM(deps Deps, region string) ACM {
	return ACM{
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
