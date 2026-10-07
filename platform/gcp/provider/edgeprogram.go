package gcp

import (
	"context"

	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/refusal"
	cloudflare "github.com/ocelhq/ocel/platform/edge/cloudflare/deploy"
)

func (p *Provider) ProgramEdge(ctx context.Context, req provider.EdgeProgramRequest) (provider.EdgeProgram, error) {
	c, err := p.openClients(ctx)
	if err != nil {
		return provider.EdgeProgram{}, err
	}
	adopted, credentials, err := requireAdoptedEdge(ctx, c, p.KeyValues(), req.Tier, req.Kind)
	if err != nil {
		return provider.EdgeProgram{}, err
	}
	if adopted.ClientCertificate.ID == "" {
		return provider.EdgeProgram{}, refusal.Refuse(refusal.CodeNotReady,
			"the %s edge's worker reaches GCP only through its client certificate, and the bootstrap adopted none: run `%s` again",
			req.Kind, provider.BootstrapCommand(req.Tier))
	}
	return cloudflare.EntryProgram{
		Tier:                     req.Tier,
		Entry:                    req.Entry,
		Namespace:                string(p.namespace),
		Slug:                     req.Slug,
		Env:                      req.Env,
		PreviewBaseDomain:        req.PreviewBaseDomain,
		PreviewKey:               req.PreviewKey,
		Origin:                   cloudflare.OriginBindings{ClientCertificate: adopted.ClientCertificate.ID, RefreshesThroughQueue: true},
		StoreScriptName:          adopted.ReleasesStore.ScriptName,
		StoreEndpoint:            adopted.ReleasesStore.Endpoint,
		StoreBootstrapCredential: credentials.ReleasesStore,
		ISRWriterScriptName:      adopted.ISRWriter.ScriptName,
		Values:                   adopted.Values,
	}.Build()
}
