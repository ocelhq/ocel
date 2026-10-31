package providerprocess

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/ocelhq/ocel/cli/internal/executables"
	"github.com/ocelhq/ocel/cli/internal/project"
	"github.com/ocelhq/ocel/cli/internal/run"
	"github.com/ocelhq/ocel/cli/internal/runtrace"
	planv1 "github.com/ocelhq/ocel/pkg/proto/common/plan/v1"
	progressv1 "github.com/ocelhq/ocel/pkg/proto/common/progress/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/proto/provider/contract/v1/contractv1connect"
	"github.com/ocelhq/ocel/pkg/proto/provider/cost/v1/costv1connect"
	"github.com/ocelhq/ocel/pkg/proto/provider/variablestore/v1/variablestorev1connect"
	"github.com/ocelhq/ocel/pkg/provider"
)

type Provider struct {
	span      *run.Span
	questions Questions
	name      string
	transport transport
	release   func()
}

type Launch struct {
	Pinning     executables.Pinning
	WebIdentity *contractv1.WebIdentity
	Namespace   string
	Release     func()
}

func Start(ctx context.Context, cfg *project.Project, span *run.Span, questions Questions, launch Launch) (*Provider, error) {
	spec, err := newLaunchSpec(ctx, cfg, launch.Pinning)
	if err != nil {
		return nil, err
	}
	spec.ProviderConfig.WebIdentity = launch.WebIdentity
	if launch.Namespace != "" {
		spec.Env = append(os.Environ(), provider.NamespaceEnvVar+"="+launch.Namespace)
	}
	provider, err := start(ctx, span, questions, spec)
	if err != nil {
		return nil, err
	}
	provider.release = launch.Release
	return provider, nil
}

func start(ctx context.Context, span *run.Span, questions Questions, spec LaunchSpec) (*Provider, error) {
	spec.Stdout = processLines{span: span, subject: spec.ProviderName, stream: progressv1.Stream_STREAM_STDOUT}
	spec.Stderr = processLines{span: span, subject: spec.ProviderName, stream: progressv1.Stream_STREAM_STDERR}
	questions.run = span.Run()
	process, err := Spawn(ctx, spec)
	if err != nil {
		return nil, fmt.Errorf("spawn provider: %w", err)
	}
	flushed := runtrace.KeepOpen(ctx)
	go func() {
		<-process.done
		flushed()
	}()
	if err := process.Ready(ctx); err != nil {
		process.Close()
		return nil, err
	}
	return &Provider{span: span, questions: questions, name: spec.ProviderName, transport: process}, nil
}

func (p *Provider) Name() string { return p.name }

func (p *Provider) Facts() *contractv1.ProviderFacts { return p.transport.Facts() }

func (p *Provider) Close() {
	p.transport.Close()
	if p.release != nil {
		p.release()
	}
}

func (p *Provider) callAnswering(ctx context.Context, call func(transport) error) error {
	err := call(p.transport)
	if confirmed, err := p.questions.answer(ctx, p.transport, err); !confirmed {
		return err
	}
	return call(p.transport)
}

func (p *Provider) Call(ctx context.Context, call func(contractv1connect.ProviderServiceClient) error) error {
	return p.callAnswering(ctx, func(t transport) error {
		client, err := t.Client()
		if err != nil {
			return err
		}
		return call(client)
	})
}

func Stream[Req any](ctx context.Context, p *Provider, rpc string, req *Req, call streamCall[Req]) (*progressv1.OperationResult, error) {
	return forward(ctx, p, rpc, req, call, p.span.Forward)
}

func ReadLogs(ctx context.Context, p *Provider, req *contractv1.ReadLogsRequest, onResponse func(*contractv1.ReadLogsResponse) error) error {
	delivered := false
	return p.callAnswering(ctx, func(t transport) error {
		err := readLogs(ctx, t, req, func(resp *contractv1.ReadLogsResponse) error {
			delivered = true
			return onResponse(resp)
		})
		if question, asked := questionIn(err); asked && delivered {
			return fmt.Errorf("provider: ReadLogs asked a question after sending logs, and reading again would print them twice: %s", question.GetFinding())
		}
		return err
	})
}

func Preflight(ctx context.Context, p *Provider, req *contractv1.PreflightRequest) (*contractv1.PreflightResponse, error) {
	var resp *contractv1.PreflightResponse
	err := p.callAnswering(ctx, func(t transport) (err error) {
		resp, err = preflight(ctx, t, req, p.span.Forward)
		return err
	})
	return resp, err
}

func Plan[Req any](ctx context.Context, p *Provider, rpc string, req *Req, call streamCall[Req]) (*planv1.ChangePlan, error) {
	var plan *planv1.ChangePlan
	_, err := forward(ctx, p, rpc, req, call, func(rpcCall runtrace.Call, event *progressv1.OperationEvent) {
		if shown := event.GetPlan(); shown != nil {
			plan = shown
			return
		}
		p.span.Forward(rpcCall, event)
	})
	return plan, err
}

func forward[Req any](ctx context.Context, p *Provider, rpc string, req *Req, call streamCall[Req], each onCallEvent) (*progressv1.OperationResult, error) {
	var result *progressv1.OperationResult
	err := p.callAnswering(ctx, func(t transport) error {
		var err error
		result, err = stream(ctx, t, rpc, req, call, each)
		return err
	})
	return result, err
}

func (p *Provider) VariableStore() (variablestorev1connect.VariableStoreServiceClient, error) {
	return p.transport.VariableStore()
}

func (p *Provider) Cost() (costv1connect.CostServiceClient, error) {
	return p.transport.Cost()
}

type processLines struct {
	span    *run.Span
	subject string
	stream  progressv1.Stream
}

func (w processLines) Write(line []byte) (int, error) {
	w.span.Forward(runtrace.Call{}, &progressv1.OperationEvent{
		Level:   progressv1.Level_LEVEL_DEBUG,
		Phase:   w.span.Phase(),
		Subject: w.subject,
		Message: strings.TrimSuffix(string(line), "\n"),
		Body:    &progressv1.OperationEvent_Output{Output: &progressv1.Output{Stream: w.stream}},
	})
	return len(line), nil
}
