package aws

import (
	"context"
	"net/url"

	"github.com/aws/aws-sdk-go-v2/service/cloudformation"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/ssm"

	"github.com/ocelhq/ocel/pkg/providerkit"
	"github.com/ocelhq/ocel/pkg/target"
	awsconnector "github.com/ocelhq/ocel/platform/aws/provider/connector"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

const connectorCompute = providerkit.ComputeServerless

func (p *Provider) connectorAPIs() awsconnector.APIs {
	return awsconnector.APIs{
		CFN:     cloudformation.NewFromConfig(p.aws),
		Buckets: s3.NewFromConfig(p.aws),
		Objects: s3.NewFromConfig(p.aws),
		SSM:     ssm.NewFromConfig(p.aws),
	}
}

type connector struct{ *Provider }

func (p connector) Target(ctx context.Context) (providerkit.ConnectorTarget, error) {
	account, err := p.accountID(ctx)
	if err != nil {
		return providerkit.ConnectorTarget{}, err
	}
	fingerprint, err := target.Fingerprint("aws", account, p.aws.Region, string(p.namespace))
	if err != nil {
		return providerkit.ConnectorTarget{}, err
	}
	standing, err := awsconnector.Read(ctx, cloudformation.NewFromConfig(p.aws), p.namespace)
	if err != nil {
		return providerkit.ConnectorTarget{}, err
	}
	described := providerkit.ConnectorTarget{
		Fingerprint: fingerprint,
		Hostname:    hostOf(standing.URL),
		Arch:        awsconnector.Arch,
	}
	if standing.Present {
		described.Installed = &providerkit.ConnectorRelease{
			Version:   standing.Version,
			PublicKey: standing.PublicKey,
			Compute:   connectorCompute,
		}
	}
	return described, nil
}

func (p connector) Install(ctx context.Context, install providerkit.ConnectorInstall,
	progress edge.Progress) (providerkit.ConnectorAddress, error) {
	compute, err := providerkit.ConnectorCompute(install.Compute, connectorCompute)
	if err != nil {
		return providerkit.ConnectorAddress{}, err
	}
	standing, err := awsconnector.Install(ctx, p.connectorAPIs(), p.namespace, awsconnector.Release{
		Binary:  install.Binary,
		Version: install.Version,
		Config:  install.Config,
	}, providerkit.WrittenByVersion(install.Version), saying(progress))
	if err != nil {
		return providerkit.ConnectorAddress{}, err
	}
	return providerkit.ConnectorAddress{URL: standing.URL, PublicKey: standing.PublicKey, Compute: compute}, nil
}

func (p connector) Remove(ctx context.Context, progress edge.Progress) error {
	return awsconnector.Remove(ctx, p.connectorAPIs(), p.namespace, saying(progress))
}

func hostOf(held string) string {
	if held == "" {
		return ""
	}
	parsed, err := url.Parse(held)
	if err != nil {
		return ""
	}
	return parsed.Host
}
