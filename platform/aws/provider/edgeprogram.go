package aws

import (
	"context"

	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/platform/aws/provider/deploy"
)

func (p *Provider) ProgramEdge(ctx context.Context, req provider.EdgeProgramRequest) (provider.EdgeProgram, error) {
	deployed, err := p.bootstrapped(ctx, req.Tier)
	if err != nil {
		return provider.EdgeProgram{}, err
	}
	params, err := p.tierParams(ctx, req.Tier, req.Kind)
	if err != nil {
		return provider.EdgeProgram{}, err
	}
	program := deploy.EdgeProgram{
		Tier:              req.Tier,
		Kind:              req.Kind,
		Entry:             req.Entry,
		Namespace:         string(p.namespace),
		Slug:              req.Slug,
		Env:               req.Env,
		PreviewBaseDomain: req.PreviewBaseDomain,
		PreviewKey:        req.PreviewKey,
		Worker: deploy.WorkerFacts{
			Region:             p.aws.Region,
			StateTable:         deployed.StateTable,
			AssetBucket:        deployed.AssetBucket,
			ImageOptimizerURL:  deployed.ImageOptimizerURL,
			RevalidateQueueURL: deployed.RevalidateQueueURL,
		},
		StoreScriptName:     params.DeploymentsStore.ScriptName,
		StoreEndpoint:       params.DeploymentsStore.Endpoint,
		StoreBootstrapCred:  params.DeploymentsStore.BootstrapCred,
		ISRWriterScriptName: params.ISRWriter.ScriptName,
	}
	if params.EdgeCredentialsErr == nil {
		program.Worker.EdgeAccessKeyID = params.EdgeCredentials.AccessKeyID
		program.Worker.EdgeSecretKey = params.EdgeCredentials.SecretAccessKey
	}
	if params.EdgeValuesErr == nil {
		program.Values = params.EdgeValues
	}
	return program.Build()
}
