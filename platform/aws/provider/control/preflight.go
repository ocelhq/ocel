package control

import (
	"context"

	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/lambda"
	"github.com/aws/aws-sdk-go-v2/service/s3"

	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/platform/aws/provider/account"
)

func refusePlanningPreflight(context.Context, provider.DeployPreflight) (provider.Plan, error) {
	return provider.Plan{}, provider.RefuseNeedsRunner("a deploy's preflight")
}

func refuseApplyingPreflight(context.Context, provider.DeployPreflight) error {
	return provider.RefuseNeedsRunner("a deploy's preflight")
}

func ResolveRuntimeLayers(ctx context.Context, p *Provider, pre provider.DeployPreflight, resolve func(account.APIs, account.Deployed) (map[string]string, error)) error {
	tier := pre.Deploy.Tier
	deployed, err := p.ReadDeployed(ctx, tier)
	if err != nil || !deployed.Present {
		return err
	}
	p.layers.forget()
	_, err = p.layers.resolve(tier, func() (map[string]string, error) {
		return resolve(account.APIs{
			Layers:  lambda.NewFromConfig(p.Config),
			Store:   s3.NewFromConfig(p.Config),
			Records: account.DynamoRecords(dynamodb.NewFromConfig(p.Config)),
		}, deployed)
	})
	return err
}
