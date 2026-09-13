package provider

import (
	"context"
	"net/url"

	"github.com/aws/aws-sdk-go-v2/service/cloudformation"
	"github.com/aws/aws-sdk-go-v2/service/s3"

	"github.com/ocelhq/ocel/pkg/providerkit"
	"github.com/ocelhq/ocel/pkg/target"
	awsconnector "github.com/ocelhq/ocel/platform/aws/provider/connector"
)

var _ providerkit.ConnectorHost = (*Provider)(nil)

const connectorCompute = providerkit.ComputeServerless

func (p *Provider) connectorAPIs() awsconnector.APIs {
	return awsconnector.APIs{
		CFN:     cloudformation.NewFromConfig(p.aws),
		Buckets: s3.NewFromConfig(p.aws),
		Objects: s3.NewFromConfig(p.aws),
	}
}

func (p *Provider) DescribeConnectorTarget(ctx context.Context) (providerkit.ConnectorTarget, error) {
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
		described.Installed = &providerkit.ConnectorRelease{Version: standing.Version, Compute: connectorCompute}
	}
	return described, nil
}

func (p *Provider) InstallConnector(ctx context.Context, install providerkit.ConnectorInstall,
	report providerkit.Reporter) (providerkit.ConnectorAddress, error) {
	compute, err := providerkit.ConnectorCompute(install.Compute, connectorCompute)
	if err != nil {
		return providerkit.ConnectorAddress{}, err
	}
	at, err := awsconnector.Install(ctx, p.connectorAPIs(), p.namespace, awsconnector.Release{
		Binary:  install.Binary,
		Version: install.Version,
		Config:  install.Config,
	}, providerkit.WriterFor(install.Version), saying(report))
	if err != nil {
		return providerkit.ConnectorAddress{}, err
	}
	return providerkit.ConnectorAddress{URL: at, Compute: compute}, nil
}

func (p *Provider) RemoveConnector(ctx context.Context, report providerkit.Reporter) error {
	return awsconnector.Remove(ctx, p.connectorAPIs(), p.namespace, saying(report))
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
