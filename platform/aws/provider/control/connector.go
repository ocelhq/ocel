package control

import (
	"context"
	"net/url"

	"github.com/aws/aws-sdk-go-v2/service/cloudformation"
	"github.com/aws/aws-sdk-go-v2/service/lambda"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/ssm"

	"github.com/ocelhq/ocel/pkg/progress"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/target"
	awsconnector "github.com/ocelhq/ocel/platform/aws/provider/connector"
)

const connectorCompute = provider.ComputeServerless

func (p *Provider) connectorAPIs() awsconnector.APIs {
	return awsconnector.APIs{
		CFN:     cloudformation.NewFromConfig(p.Config),
		Buckets: s3.NewFromConfig(p.Config),
		Objects: s3.NewFromConfig(p.Config),
		SSM:     ssm.NewFromConfig(p.Config),
		Lambda:  lambda.NewFromConfig(p.Config),
		Region:  p.Config.Region,
	}
}

type connector struct{ *Provider }

type fullConnector struct{ connector }

func NewFullConnector(p *Provider) provider.Connector { return fullConnector{connector{p}} }

func (p connector) Target(ctx context.Context) (provider.ConnectorTarget, error) {
	account, err := p.ReadAccountID(ctx)
	if err != nil {
		return provider.ConnectorTarget{}, err
	}
	fingerprint, err := target.Fingerprint("aws", account, p.Config.Region, string(p.Namespace))
	if err != nil {
		return provider.ConnectorTarget{}, err
	}
	installation, err := awsconnector.Read(ctx, cloudformation.NewFromConfig(p.Config), p.Namespace)
	if err != nil {
		return provider.ConnectorTarget{}, err
	}
	described := provider.ConnectorTarget{
		Fingerprint: fingerprint,
		Hostname:    hostOf(installation.URL),
		OS:          awsconnector.OS,
		Arch:        awsconnector.Arch,
	}
	if installation.Present {
		described.Installed = &provider.InstalledConnector{
			Version:   installation.Version,
			PublicKey: installation.PublicKey,
			Compute:   connectorCompute,
		}
	}
	return described, nil
}

func (connector) Install(context.Context, provider.ConnectorInstall, progress.Log) (provider.ConnectorAddress, error) {
	return provider.ConnectorAddress{}, provider.RefuseNeedsRunner("installing the connector")
}

func (connector) Remove(context.Context, progress.Log) error {
	return provider.RefuseNeedsRunner("removing the connector")
}

func (p fullConnector) Install(ctx context.Context, install provider.ConnectorInstall,
	progress progress.Log) (provider.ConnectorAddress, error) {
	compute, err := provider.ConnectorCompute(install.Compute, connectorCompute)
	if err != nil {
		return provider.ConnectorAddress{}, err
	}
	policies, err := ConnectorPolicies(p.Namespace, p.Options.VariablesKey)
	if err != nil {
		return provider.ConnectorAddress{}, err
	}
	installation, err := awsconnector.Install(ctx, p.connectorAPIs(), p.Namespace, awsconnector.Bundle{
		Binary:   install.Binary,
		Version:  install.Version,
		Config:   install.Config,
		Policies: policies,
	}, provider.WrittenByVersion(install.Version), progress)
	if err != nil {
		return provider.ConnectorAddress{}, err
	}
	return provider.ConnectorAddress{URL: installation.URL, PublicKey: installation.PublicKey, Compute: compute}, nil
}

func (p fullConnector) Remove(ctx context.Context, progress progress.Log) error {
	return awsconnector.Remove(ctx, p.connectorAPIs(), p.Namespace, progress)
}

func hostOf(rawURL string) string {
	if rawURL == "" {
		return ""
	}
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return ""
	}
	return parsed.Host
}
