package providerprocess

import (
	"context"
	"fmt"
	"strings"

	"github.com/ocelhq/ocel/cli/internal/executables"
	"github.com/ocelhq/ocel/cli/internal/project"
	"github.com/ocelhq/ocel/cli/internal/run"
	planv1 "github.com/ocelhq/ocel/pkg/proto/common/plan/v1"
	progressv1 "github.com/ocelhq/ocel/pkg/proto/common/progress/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/proto/provider/contract/v1/contractv1connect"
	"github.com/ocelhq/ocel/pkg/proto/provider/cost/v1/costv1connect"
	"github.com/ocelhq/ocel/pkg/proto/provider/envvars/v1/envvarsv1connect"
)

type Provider struct {
	span      *run.Span
	questions Questions
	spec      LaunchSpec
	process   *Process
}

func Start(ctx context.Context, cfg *project.Project, span *run.Span, questions Questions, pinning executables.Pinning) (*Provider, error) {
	spec, err := newLaunchSpec(ctx, cfg, pinning)
	if err != nil {
		return nil, err
	}
	return start(ctx, span, questions, spec)
}

func start(ctx context.Context, span *run.Span, questions Questions, spec LaunchSpec) (*Provider, error) {
	spec.Stdout = processLines{span: span, subject: spec.ProviderName, stream: progressv1.Stream_STREAM_STDOUT}
	spec.Stderr = processLines{span: span, subject: spec.ProviderName, stream: progressv1.Stream_STREAM_STDERR}
	questions.run = span.Run()
	process, err := Spawn(ctx, spec)
	if err != nil {
		return nil, fmt.Errorf("spawn provider: %w", err)
	}
	if err := process.Ready(ctx); err != nil {
		process.Close()
		return nil, err
	}
	return &Provider{span: span, questions: questions, spec: spec, process: process}, nil
}

func (p *Provider) Name() string { return p.spec.ProviderName }

func (p *Provider) Facts() *contractv1.ProviderFacts { return p.process.Facts() }

func (p *Provider) Close() { p.process.Close() }

func (p *Provider) callAnswering(ctx context.Context, call func(*Process) error) error {
	err := call(p.process)
	if confirmed, err := p.questions.answer(ctx, p.process, err); !confirmed {
		return err
	}
	return call(p.process)
}

func (p *Provider) Call(ctx context.Context, call func(contractv1connect.ProviderServiceClient) error) error {
	return p.callAnswering(ctx, func(r *Process) error {
		client, err := r.Client()
		if err != nil {
			return err
		}
		return call(client)
	})
}

func Stream[Req any](ctx context.Context, p *Provider, rpc string, req *Req, call streamCall[Req]) (*progressv1.OperationResult, error) {
	return forward(ctx, p, rpc, req, call, p.span.Forward)
}

func Plan[Req any](ctx context.Context, p *Provider, rpc string, req *Req, call streamCall[Req]) (*planv1.ChangePlan, error) {
	var plan *planv1.ChangePlan
	_, err := forward(ctx, p, rpc, req, call, func(ev *progressv1.OperationEvent) {
		if shown := ev.GetPlan(); shown != nil {
			plan = shown
			return
		}
		p.span.Forward(ev)
	})
	return plan, err
}

func forward[Req any](ctx context.Context, p *Provider, rpc string, req *Req, call streamCall[Req], each func(*progressv1.OperationEvent)) (*progressv1.OperationResult, error) {
	var result *progressv1.OperationResult
	err := p.callAnswering(ctx, func(r *Process) error {
		var err error
		result, err = stream(ctx, r, rpc, req, call, each)
		return err
	})
	return result, err
}

func (p *Provider) Vars() (envvarsv1connect.EnvVarsServiceClient, error) {
	return p.process.Vars()
}

func (p *Provider) Cost() (costv1connect.CostServiceClient, error) {
	return p.process.Cost()
}

type processLines struct {
	span    *run.Span
	subject string
	stream  progressv1.Stream
}

func (w processLines) Write(line []byte) (int, error) {
	w.span.Forward(&progressv1.OperationEvent{
		Level:   progressv1.Level_LEVEL_DEBUG,
		Phase:   w.span.Phase(),
		Subject: w.subject,
		Message: strings.TrimSuffix(string(line), "\n"),
		Body:    &progressv1.OperationEvent_Output{Output: &progressv1.Output{Stream: w.stream}},
	})
	return len(line), nil
}
