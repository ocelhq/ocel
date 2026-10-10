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
	values := deploy.WorkerValues{
		ImageOptimizerURL:  deployed.ImageOptimizerURL,
		RevalidateQueueURL: deployed.RevalidateQueueURL,
		AssetBucket:        deployed.AssetBucket,
		Region:             p.aws.Region,
	}
	if params.EdgeCredentialsErr == nil {
		values.EdgeAccessKeyID = params.EdgeCredentials.AccessKeyID
		values.EdgeSecretKey = params.EdgeCredentials.SecretAccessKey
	}
	program := cloudflare.EntryProgram{
		Tier:                     req.Tier,
		Entry:                    req.Entry,
		Namespace:                string(p.namespace),
		Slug:                     req.Slug,
		Env:                      req.Env,
		PreviewBaseDomain:        req.PreviewBaseDomain,
		PreviewKey:               req.PreviewKey,
		Origin:                   values.Bindings(),
		StoreScriptName:          params.ReleasesStore.ScriptName,
		StoreEndpoint:            params.ReleasesStore.Endpoint,
		StoreBootstrapCredential: params.ReleasesStore.BootstrapCredential,
		ISRWriterScriptName:      params.ISRWriter.ScriptName,
	}
	if params.EdgeValuesErr == nil {
		program.Values = params.EdgeValues
	}
	return program.Build()
}
