package aws

import (
	"context"
	"net/url"

	"github.com/aws/aws-sdk-go-v2/service/cloudformation"
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
		CFN:     cloudformation.NewFromConfig(p.aws),
		Buckets: s3.NewFromConfig(p.aws),
		Objects: s3.NewFromConfig(p.aws),
		SSM:     ssm.NewFromConfig(p.aws),
	}
}

type connector struct{ *Provider }

func (p connector) Target(ctx context.Context) (provider.ConnectorTarget, error) {
	account, err := p.accountID(ctx)
	if err != nil {
		return provider.ConnectorTarget{}, err
	}
	fingerprint, err := target.Fingerprint("aws", account, p.aws.Region, string(p.namespace))
	if err != nil {
		return provider.ConnectorTarget{}, err
	}
	installation, err := awsconnector.Read(ctx, cloudformation.NewFromConfig(p.aws), p.namespace)
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
		described.Installed = &provider.ConnectorRelease{
			Version:   installation.Version,
			PublicKey: installation.PublicKey,
			Compute:   connectorCompute,
		}
	}
	return described, nil
}

func (p connector) Install(ctx context.Context, install provider.ConnectorInstall,
	progress progress.Progress) (provider.ConnectorAddress, error) {
	compute, err := provider.ConnectorCompute(install.Compute, connectorCompute)
	if err != nil {
		return provider.ConnectorAddress{}, err
	}
	installation, err := awsconnector.Install(ctx, p.connectorAPIs(), p.namespace, awsconnector.Release{
		Binary:  install.Binary,
		Version: install.Version,
		Config:  install.Config,
	}, provider.WrittenByVersion(install.Version), progress)
	if err != nil {
		return provider.ConnectorAddress{}, err
	}
	return provider.ConnectorAddress{URL: installation.URL, PublicKey: installation.PublicKey, Compute: compute}, nil
}

func (p connector) Remove(ctx context.Context, progress progress.Progress) error {
	return awsconnector.Remove(ctx, p.connectorAPIs(), p.namespace, progress)
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
