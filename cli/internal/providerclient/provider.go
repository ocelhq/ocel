package providerclient

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"github.com/ocelhq/ocel/cli/internal/events"
	"github.com/ocelhq/ocel/cli/internal/projectconfig"
	planv1 "github.com/ocelhq/ocel/pkg/proto/common/plan/v1"
	progressv1 "github.com/ocelhq/ocel/pkg/proto/common/progress/v1"
	"github.com/ocelhq/ocel/pkg/proto/provider/contract/v1/contractv1connect"
	"github.com/ocelhq/ocel/pkg/proto/provider/cost/v1/costv1connect"
	"github.com/ocelhq/ocel/pkg/proto/provider/envvars/v1/envvarsv1connect"
)

type Provider struct {
	ctx    context.Context
	scope  *events.Scope
	trust  Trust
	config Config

	mu     sync.Mutex
	runner *Runner
}

func Start(ctx context.Context, cfg *projectconfig.Config, scope *events.Scope, trust Trust, pins Pinning) (*Provider, error) {
	config, err := prepareLaunch(ctx, cfg, pins)
	if err != nil {
		return nil, err
	}
	return start(ctx, scope, trust, config)
}

func start(ctx context.Context, scope *events.Scope, trust Trust, config Config) (*Provider, error) {
	config.Stdout = processLines{scope: scope, subject: config.ProviderName, stream: progressv1.Stream_STREAM_STDOUT}
	config.Stderr = processLines{scope: scope, subject: config.ProviderName, stream: progressv1.Stream_STREAM_STDERR}
	trust.Hold = scope.Hold
	p := &Provider{ctx: ctx, scope: scope, trust: trust, config: config}
	runner, err := p.spawn()
	if err != nil {
		return nil, err
	}
	p.runner = runner
	return p, nil
}

func (p *Provider) spawn() (*Runner, error) {
	runner, err := Spawn(p.ctx, p.config)
	if err != nil {
		return nil, fmt.Errorf("spawn provider: %w", err)
	}
	if err := runner.Ready(p.ctx); err != nil {
		runner.Close()
		return nil, err
	}
	return runner, nil
}

func (p *Provider) current() *Runner {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.runner
}

func (p *Provider) Name() string { return p.config.ProviderName }

func (p *Provider) Close() { p.current().Close() }

func (p *Provider) callTrusting(ctx context.Context, call func(*Runner) error) error {
	err := call(p.current())
	if trusted, err := p.trust.acceptKey(ctx, err); !trusted {
		return err
	}
	runner, err := p.restart()
	if err != nil {
		return err
	}
	return call(runner)
}

func (p *Provider) restart() (*Runner, error) {
	p.Close()
	runner, err := p.spawn()
	if err != nil {
		return nil, err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.runner = runner
	return runner, nil
}

func (p *Provider) Call(ctx context.Context, call func(contractv1connect.ProviderServiceClient) error) error {
	return p.callTrusting(ctx, func(r *Runner) error {
		client, err := r.Client()
		if err != nil {
			return err
		}
		return call(client)
	})
}

func Stream[Req any](ctx context.Context, p *Provider, rpc string, req *Req, call streamCall[Req]) (*progressv1.ResultEvent, error) {
	return forward(ctx, p, rpc, req, call, p.scope.Forward)
}

func Plan[Req any](ctx context.Context, p *Provider, rpc string, req *Req, call streamCall[Req]) (*planv1.ChangePlan, error) {
	var plan *planv1.ChangePlan
	_, err := forward(ctx, p, rpc, req, call, func(ev *progressv1.OperationEvent) {
		if shown := ev.GetPlan(); shown != nil {
			plan = shown
			return
		}
		p.scope.Forward(ev)
	})
	return plan, err
}

func forward[Req any](ctx context.Context, p *Provider, rpc string, req *Req, call streamCall[Req], each func(*progressv1.OperationEvent)) (*progressv1.ResultEvent, error) {
	var result *progressv1.ResultEvent
	err := p.callTrusting(ctx, func(r *Runner) error {
		var err error
		result, err = stream(ctx, r, rpc, req, call, each)
		return err
	})
	return result, err
}

func (p *Provider) Vars() (envvarsv1connect.EnvVarsServiceClient, error) {
	return p.current().Vars()
}

func (p *Provider) Cost() (costv1connect.CostServiceClient, error) {
	return p.current().Cost()
}

type processLines struct {
	scope   *events.Scope
	subject string
	stream  progressv1.Stream
}

func (w processLines) Write(line []byte) (int, error) {
	w.scope.Forward(&progressv1.OperationEvent{
		Level:   progressv1.Level_LEVEL_DEBUG,
		Subject: w.subject,
		Message: strings.TrimSuffix(string(line), "\n"),
		Body:    &progressv1.OperationEvent_Output{Output: &progressv1.Output{Stream: w.stream}},
	})
	return len(line), nil
}
