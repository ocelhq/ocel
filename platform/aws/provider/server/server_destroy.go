package server

import (
	"context"
	"errors"

	connect "connectrpc.com/connect"

	"github.com/aws/aws-sdk-go-v2/service/cloudformation"
	"github.com/aws/aws-sdk-go-v2/service/ssm"

	"github.com/ocelhq/ocel/pkg/naming"

	deploymentsv1 "github.com/ocelhq/ocel/pkg/proto/deployments/v1"
	"github.com/ocelhq/ocel/platform/aws/provider/bootstrap"
	"github.com/ocelhq/ocel/platform/aws/provider/certs"
	"github.com/ocelhq/ocel/platform/aws/provider/deploy"
	"github.com/ocelhq/ocel/platform/aws/provider/dns"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

func (s *Server) PlanDestroyProject(ctx context.Context, req *deploymentsv1.PlanDestroyProjectRequest) (*deploymentsv1.PlanDestroyProjectResponse, error) {
	opts, err := parseOptions(req.GetOptions())
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}

	edgeFront, err := s.edge(requestedEdge(req), opts.Region)
	if err != nil {
		return nil, err
	}
	if req.GetEnvironment().GetClass() == deploymentsv1.Environment_CLASS_PREVIEW {
		return s.planDestroyPreviewProject(ctx, opts, edgeFront, req.GetSlug())
	}

	awscfg, params, err := productionTeardownParams(ctx, opts, req.GetSlug())
	if err != nil {
		return nil, err
	}
	cfg, err := s.pruneConfig(ctx, opts, awscfg, params, req.GetSlug())
	if err != nil {
		return nil, err
	}
	plan, err := deploy.PlanProjectTeardown(ctx, cfg, req.GetSlug())
	if err != nil {
		return nil, err
	}
	deployed, err := s.deployed(ctx, cloudformation.NewFromConfig(awscfg), opts.Region, false)
	if err != nil {
		return nil, err
	}

	var infraStacks []string
	if !plan.InfraStack.IsZero() {
		infraStacks = []string{plan.InfraStack.String()}
	}
	edgeStack, err := s.edgeStackPlan(edgeFront, projectPlanScope{
		kind:       edgeFront.Kind(),
		class:      bootstrap.ClassProduction,
		slug:       req.GetSlug(),
		stateTable: deployed.StateTable,
		stacks:     len(plan.AppStacks) + len(infraStacks),
		state:      params.StackState,
	})
	if err != nil {
		return nil, err
	}
	indexed, err := deploy.ProjectIndexed(ctx, cfg.Stacks, req.GetSlug())
	if err != nil {
		return nil, err
	}
	appStacks := stackNames(plan.AppStacks)
	return &deploymentsv1.PlanDestroyProjectResponse{
		AppStacks:        appStacks,
		InfraStacks:      infraStacks,
		EdgeStack:        edgeStack,
		NothingToDestroy: nothingToDestroy(indexed, edgeStack, appStacks, infraStacks),
	}, nil
}

func nothingToDestroy(indexed bool, edgeStack *deploymentsv1.EdgeStackPlan, appStacks, infraStacks []string) bool {
	return !indexed && len(edgeStack.GetItems()) == 0 && len(appStacks) == 0 && len(infraStacks) == 0
}

func (s *Server) planDestroyPreviewProject(ctx context.Context, opts options, edgeFront edge.Edge, slug string) (*deploymentsv1.PlanDestroyProjectResponse, error) {
	awscfg, err := loadAWS(ctx, opts.Region)
	if err != nil {
		return nil, err
	}
	deployed, err := s.deployed(ctx, cloudformation.NewFromConfig(awscfg), opts.Region, true)
	if err != nil {
		return nil, err
	}
	stacks, err := stackIndexFor(awscfg, deployed, bootstrapCommand(true))
	if err != nil {
		return nil, err
	}
	plan, err := deploy.PlanPreviewProjectTeardown(ctx, deploy.Config{Stacks: stacks}, slug)
	if err != nil {
		return nil, err
	}
	state, err := bootstrap.ReadStackStateFor(ctx, ssm.NewFromConfig(awscfg), bootstrap.ClassPreview, slug)
	if err != nil {
		return nil, err
	}

	edgeStack, err := s.edgeStackPlan(edgeFront, projectPlanScope{
		kind:       edgeFront.Kind(),
		class:      bootstrap.ClassPreview,
		slug:       slug,
		stateTable: deployed.StateTable,
		stacks:     len(plan.AppStacks) + len(plan.InfraStacks),
		state:      state,
	})
	if err != nil {
		return nil, err
	}
	indexed, err := deploy.ProjectIndexed(ctx, stacks, slug)
	if err != nil {
		return nil, err
	}
	appStacks, infraStacks := stackNames(plan.AppStacks), stackNames(plan.InfraStacks)
	return &deploymentsv1.PlanDestroyProjectResponse{
		AppStacks:        appStacks,
		InfraStacks:      infraStacks,
		EdgeStack:        edgeStack,
		NothingToDestroy: nothingToDestroy(indexed, edgeStack, appStacks, infraStacks),
	}, nil
}

func stackNames(stacks []naming.StackName) []string {
	names := make([]string, 0, len(stacks))
	for _, stack := range stacks {
		names = append(names, stack.String())
	}
	return names
}

func (s *Server) edgeStackPlan(edgeFront edge.Edge, scope projectPlanScope) (*deploymentsv1.EdgeStackPlan, error) {
	plan := &deploymentsv1.EdgeStackPlan{EdgeKind: string(edgeFront.Kind())}

	if _, err := openStackOn(edgeFront, scope.state); err != nil {
		if errors.Is(err, errNoProductionDeploy) {
			return plan, nil
		}
		return nil, err
	}

	items, err := destroyPlanItems(scope)
	if err != nil {
		return nil, err
	}
	plan.Items = items
	return plan, nil
}

func (s *Server) DestroyProject(ctx context.Context, req *deploymentsv1.DestroyProjectRequest, stream *connect.ServerStream[deploymentsv1.DeployEvent]) (err error) {
	sender := newEventSender(ctx, stream.Send)
	defer func() { err = sender.close() }()
	tracer := newEventTracer(sender)
	stageReport, logf := newTeardownReporter(sender)

	if derr := s.runDestroyProject(ctx, req, tracer, stageReport, logf); derr != nil {
		sender.send(failureResult(derr))
		return nil
	}
	sender.send(okResult())
	return nil
}

func newDestroyProjectStages() deploy.ProjectTeardownStages {
	return deploy.ProjectTeardownStages{
		Planning:    deploy.NewRootStage("Planning the teardown"),
		Unbind:      deploy.NewRootStage("Unbinding what routes to this project"),
		AppStacks:   deploy.NewRootStage("Destroying app stacks"),
		InfraStacks: deploy.NewRootStage("Destroying infra stacks"),
		Edge:        deploy.NewRootStage("Destroying the edge stack, its domain surfaces and the deployments ledger"),
		Values:      deploy.NewRootStage("Purging stored variable values"),
		Assets:      deploy.NewRootStage("Purging project assets"),
		Forget:      deploy.NewRootStage("Forgetting the project"),
	}
}

func newDestroyPreviewProjectStages() deploy.ProjectTeardownStages {
	return deploy.ProjectTeardownStages{
		Planning:    deploy.NewRootStage("Planning the preview teardown"),
		Unbind:      deploy.NewRootStage("Unbinding what routes to this project's previews"),
		AppStacks:   deploy.NewRootStage("Destroying preview app stacks"),
		InfraStacks: deploy.NewRootStage("Destroying preview infra stacks"),
		Edge:        deploy.NewRootStage("Destroying the preview edge stack and the deployments ledger"),
		Values:      deploy.NewRootStage("Purging stored variable values"),
		Assets:      deploy.NewRootStage("Purging preview assets"),
		Forget:      deploy.NewRootStage("Forgetting the project"),
	}
}

func (s *Server) runDestroyProject(ctx context.Context, req *deploymentsv1.DestroyProjectRequest, tracer deploy.Tracer, stageReport func(deploy.StageID) func(string), logf func(string)) error {
	if env := req.GetEnvironment(); env.GetClass() == deploymentsv1.Environment_CLASS_PREVIEW {
		return s.runDestroyPreviewProject(ctx, req, env, tracer, stageReport, logf)
	}

	stages := newDestroyProjectStages()
	deploy.DeclareStages(tracer, false, stages.Roots()...)
	finish := func(err error) error {
		if err != nil {
			closeStages(tracer)
		}
		return err
	}

	opts, err := parseOptions(req.GetOptions())
	if err != nil {
		return finish(connect.NewError(connect.CodeInvalidArgument, err))
	}
	awscfg, params, err := productionTeardownParams(ctx, opts, req.GetSlug())
	if err != nil {
		return finish(err)
	}
	stack, err := s.openStackFor(requestedEdge(req), params.StackState, awscfg.Region)
	if err != nil && !errors.Is(err, errNoProductionDeploy) {
		return finish(err)
	}
	cfg, err := s.pruneConfig(ctx, opts, awscfg, params, req.GetSlug())
	if err != nil {
		return finish(err)
	}
	cfg.Tracer = tracer
	cfg.StageReport = stageReport
	if cfg.DNS, err = dns.WriterFor(req.GetDns().GetKind(), req.GetDns().GetZone(), dns.Deps{AWS: awscfg}); err != nil {
		return finish(err)
	}

	result, derr := deploy.DestroyProject(ctx, stack, cfg, req.GetSlug(), stages, logf)

	if teardownFinished(result, params.StackState) {
		discarder := func(cert certs.Certificate) certs.Issuer {
			return certs.DiscardIssuerFor(cert, certs.Deps{AWS: awscfg})
		}
		if err := releaseProductionDomains(ctx, params.StackState, cfg.DNS, discarder, logf); err != nil {
			derr = errors.Join(derr, err)
		}
	}
	if err := forgetStackState(ctx, ssm.NewFromConfig(awscfg), bootstrap.ClassProduction, req.GetSlug(), result, params.StackState, derr); err != nil {
		derr = errors.Join(derr, err)
	}
	return derr
}

func teardownFinished(result deploy.DestroyProjectResult, recorded edge.StackState) bool {
	return result.EdgeTornDown && len(recorded) > 0
}

func forgetStackState(ctx context.Context, ssmClient bootstrap.SSMAPI, class, slug string, result deploy.DestroyProjectResult, recorded edge.StackState, failed error) error {
	if failed != nil || !teardownFinished(result, recorded) {
		return nil
	}
	return bootstrap.DeleteStackStateFor(ctx, ssmClient, class, slug)
}

func (s *Server) runDestroyPreviewProject(ctx context.Context, req *deploymentsv1.DestroyProjectRequest, env *deploymentsv1.Environment, tracer deploy.Tracer, stageReport func(deploy.StageID) func(string), logf func(string)) error {
	slug := req.GetSlug()

	stages := newDestroyPreviewProjectStages()
	deploy.DeclareStages(tracer, false, stages.Roots()...)
	finish := func(err error) error {
		if err != nil {
			closeStages(tracer)
		}
		return err
	}

	opts, err := parseOptions(req.GetOptions())
	if err != nil {
		return finish(connect.NewError(connect.CodeInvalidArgument, err))
	}
	cfg, stack, err := s.previewTeardownContext(ctx, requestedEdge(req), opts, slug, env)
	if err != nil {
		return finish(err)
	}
	awscfg, err := loadAWS(ctx, opts.Region)
	if err != nil {
		return finish(err)
	}
	cfg.Tracer = tracer
	cfg.StageReport = stageReport
	if cfg.DNS, err = dns.WriterFor(req.GetDns().GetKind(), req.GetDns().GetZone(), dns.Deps{AWS: awscfg}); err != nil {
		return finish(err)
	}

	result, derr := deploy.DestroyPreviewProject(ctx, stack, cfg, slug, stages, logf)

	if err := forgetStackState(ctx, ssm.NewFromConfig(awscfg), bootstrap.ClassPreview, slug, result, stack.State(), derr); err != nil {
		derr = errors.Join(derr, err)
	}
	return derr
}
