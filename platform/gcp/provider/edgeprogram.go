package gcp

import (
	"context"
	"fmt"

	"github.com/ocelhq/ocel/pkg/progress"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/refusal"
	cloudflare "github.com/ocelhq/ocel/platform/edge/cloudflare/deploy"
)

func (p *Provider) ProgramEdge(ctx context.Context, req provider.EdgeProgramRequest) (provider.EdgeProgram, error) {
	c, err := p.openClients(ctx)
	if err != nil {
		return provider.EdgeProgram{}, err
	}
	adopted, credentials, err := readAdoptedEdge(ctx, c, p.KeyValues(), req.Tier, req.Kind)
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
		Origin:                   cloudflare.OriginBindings{ClientCertificate: adopted.ClientCertificate.ID},
		StoreScriptName:          adopted.DeploymentsStore.ScriptName,
		StoreEndpoint:            adopted.DeploymentsStore.Endpoint,
		StoreBootstrapCredential: credentials.DeploymentsStore,
		ISRWriterScriptName:      adopted.ISRWriter.ScriptName,
		Values:                   adopted.Values,
	}.Build()
}

func (p *Provider) readEdgeISRWriter(ctx context.Context, spec provider.StackSpec) (cloudflare.ISRWriter, error) {
	app := spec.App
	if app == nil || app.ISR == nil || app.Compute != provider.ComputeServerless || !factsOf(spec.Edge).RunsCode {
		return cloudflare.ISRWriter{}, nil
	}
	c, err := p.openClients(ctx)
	if err != nil {
		return cloudflare.ISRWriter{}, err
	}
	adopted, credentials, err := readAdoptedEdge(ctx, c, p.KeyValues(), spec.Ref.Tier, spec.Edge.Kind())
	if err != nil {
		return cloudflare.ISRWriter{}, err
	}
	seed, err := readISRWriterSeed(ctx, c, spec.Ref.Tier, spec.Edge.Kind())
	if err != nil {
		return cloudflare.ISRWriter{}, err
	}
	return cloudflare.ISRWriter{Endpoint: adopted.ISRWriter.Endpoint, BootstrapCredential: credentials.ISRWriter, Seed: seed}, nil
}

type stacks struct {
	provider.Stacks
	p *Provider
}

func (s stacks) Provision(ctx context.Context, spec provider.StackSpec, runProgress progress.Log) (provider.StackResult, error) {
	writer, err := s.p.readEdgeISRWriter(ctx, spec)
	if err != nil {
		return provider.StackResult{}, err
	}
	if writer.IsConfigured() {
		if err := writer.Initialize(ctx, spec.App.ISR.Prefix); err != nil {
			return provider.StackResult{}, fmt.Errorf("register %s's ISR prefix with the %s edge's writer: %w", spec.App.App, spec.Edge.Kind(), err)
		}
	}
	result, err := s.Stacks.Provision(ctx, spec, runProgress)
	if err != nil {
		return result, err
	}
	if writer.IsConfigured() {
		result.ISRWriteSecret = cloudflare.DeriveISRWriteSecret(writer.Seed, spec.App.ISR.Prefix)
	}
	return result, nil
}

func (p *Provider) edgeISRStoreFor(ctx context.Context, spec provider.StackSpec) (*edgeISRStore, error) {
	if !keepsISRInEdgeStore(spec) {
		return nil, nil
	}
	writer, err := p.readEdgeISRWriter(ctx, spec)
	if err != nil {
		return nil, err
	}
	if !writer.IsConfigured() {
		return nil, refusal.Refuse(refusal.CodeNotReady,
			"the %s edge's ISR writer is not adopted, and %s keeps its pages and tags in it: run `%s` again",
			spec.Edge.Kind(), spec.App.App, provider.BootstrapCommand(spec.Ref.Tier))
	}
	return &edgeISRStore{writer: writer, secret: cloudflare.DeriveISRWriteSecret(writer.Seed, spec.App.ISR.Prefix)}, nil
}
