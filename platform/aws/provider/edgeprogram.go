package aws

import (
	"context"

	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/platform/aws/provider/deploy"
	cloudflare "github.com/ocelhq/ocel/platform/edge/cloudflare/deploy"
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
	facts := deploy.WorkerFacts{
		Region:             p.aws.Region,
		StateTable:         deployed.StateTable,
		AssetBucket:        deployed.AssetBucket,
		ImageOptimizerURL:  deployed.ImageOptimizerURL,
		RevalidateQueueURL: deployed.RevalidateQueueURL,
	}
	if params.EdgeCredentialsErr == nil {
		facts.EdgeAccessKeyID = params.EdgeCredentials.AccessKeyID
		facts.EdgeSecretKey = params.EdgeCredentials.SecretAccessKey
	}
	program := cloudflare.EntryProgram{
		Tier:                req.Tier,
		Entry:               req.Entry,
		Namespace:           string(p.namespace),
		Slug:                req.Slug,
		Env:                 req.Env,
		PreviewBaseDomain:   req.PreviewBaseDomain,
		PreviewKey:          req.PreviewKey,
		Origin:              facts.Bindings(),
		StoreScriptName:     params.DeploymentsStore.ScriptName,
		StoreEndpoint:       params.DeploymentsStore.Endpoint,
		StoreBootstrapCred:  params.DeploymentsStore.BootstrapCred,
		ISRWriterScriptName: params.ISRWriter.ScriptName,
	}
	if params.EdgeValuesErr == nil {
		program.Values = params.EdgeValues
	}
	return program.Build()
}
