package aws

import (
	"context"

	"github.com/ocelhq/ocel/pkg/providerkit/provider"
	"github.com/ocelhq/ocel/platform/aws/provider/deploy"
)

func (p *Provider) ProgramEdge(ctx context.Context, req provider.EdgeProgramRequest) (provider.EdgeProgram, error) {
	held, err := p.bootstrapped(ctx, req.Class)
	if err != nil {
		return provider.EdgeProgram{}, err
	}
	params, err := p.classParams(ctx, req.Class, req.Kind)
	if err != nil {
		return provider.EdgeProgram{}, err
	}
	program := deploy.EdgeProgram{
		Class:             req.Class,
		Kind:              req.Kind,
		Namespace:         string(p.namespace),
		Slug:              req.Slug,
		Env:               req.Env,
		PreviewBaseDomain: req.PreviewBaseDomain,
		Apps:              req.Apps,
		Worker: deploy.WorkerFacts{
			Region:             p.aws.Region,
			StateTable:         held.StateTable,
			AssetBucket:        held.AssetBucket,
			ImageOptimizerURL:  held.ImageOptimizerURL,
			RevalidateQueueURL: held.RevalidateQueueURL,
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
