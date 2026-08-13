package server

import (
	"context"
	"errors"

	connect "connectrpc.com/connect"

	"github.com/aws/aws-sdk-go-v2/service/ssm"

	deploymentsv1 "github.com/ocelhq/ocel/pkg/proto/deployments/v1"
	"github.com/ocelhq/ocel/platform/aws/provider/bootstrap"
	"github.com/ocelhq/ocel/platform/aws/provider/deploy"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

func (s *Server) PlanDestroyProject(ctx context.Context, req *deploymentsv1.PlanDestroyProjectRequest) (*deploymentsv1.PlanDestroyProjectResponse, error) {
	opts, err := parseOptions(req.GetOptions())
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
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

	rootStack, err := s.hasRootStack(params.RootStackState)
	if err != nil {
		return nil, err
	}

	appStacks := make([]string, 0, len(plan.AppStacks))
	for _, stack := range plan.AppStacks {
		appStacks = append(appStacks, stack.String())
	}
	var infraStack string
	if !plan.InfraStack.IsZero() {
		infraStack = plan.InfraStack.String()
	}

	return &deploymentsv1.PlanDestroyProjectResponse{
		AppStacks:  appStacks,
		InfraStack: infraStack,
		RootStack:  rootStack,
	}, nil
}

func (s *Server) hasRootStack(state edge.RootStackState) (bool, error) {
	_, state, err := s.rootStackFor(state)
	if err != nil {
		if errors.Is(err, errNoProductionDeploy) {
			return false, nil
		}
		return false, err
	}
	return len(state) > 0, nil
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
		Edge:        deploy.NewRootStage("Destroying edge workers and the deployments store"),
		AppStacks:   deploy.NewRootStage("Destroying app stacks"),
		InfraStacks: deploy.NewRootStage("Destroying infra stacks"),
		Values:      deploy.NewRootStage("Purging stored variable values"),
		Assets:      deploy.NewRootStage("Purging project assets"),
		Forget:      deploy.NewRootStage("Forgetting the project"),
	}
}

func newDestroyPreviewProjectStages() deploy.ProjectTeardownStages {
	return deploy.ProjectTeardownStages{
		Planning:    deploy.NewRootStage("Planning the preview teardown"),
		Edge:        deploy.NewRootStage("Destroying preview root workers and the deployments store"),
		AppStacks:   deploy.NewRootStage("Destroying preview app stacks"),
		InfraStacks: deploy.NewRootStage("Destroying preview infra stacks"),
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
	stack, state, err := s.rootStackFor(params.RootStackState)
	if err != nil && !errors.Is(err, errNoProductionDeploy) {
		return finish(err)
	}
	cfg, err := s.pruneConfig(ctx, opts, awscfg, params, req.GetSlug())
	if err != nil {
		return finish(err)
	}
	cfg.Tracer = tracer
	cfg.StageReport = stageReport

	result, derr := deploy.DestroyProject(ctx, stack, state, cfg, req.GetSlug(), stages, logf)

	if result.RootTornDown && len(state) > 0 {
		if err := s.deleteRootStackState(ctx, opts, req.GetSlug()); err != nil {
			derr = errors.Join(derr, err)
		}
	}
	return derr
}

func (s *Server) deleteRootStackState(ctx context.Context, opts options, slug string) error {
	awscfg, err := loadAWS(ctx, opts.Region)
	if err != nil {
		return err
	}
	return bootstrap.DeleteRootStackState(ctx, ssm.NewFromConfig(awscfg), slug)
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
	cfg, stack, state, err := s.previewTeardownContext(ctx, opts, slug, env)
	if err != nil {
		return finish(err)
	}
	cfg.Tracer = tracer
	cfg.StageReport = stageReport

	result, derr := deploy.DestroyPreviewProject(ctx, stack, state, cfg, slug, stages, logf)

	if result.RootTornDown && len(state) > 0 {
		awscfg, awsErr := loadAWS(ctx, opts.Region)
		if awsErr != nil {
			return awsErr
		}
		if err := bootstrap.DeleteRootStackStateFor(ctx, ssm.NewFromConfig(awscfg), bootstrap.ClassPreview, slug); err != nil {
			derr = errors.Join(derr, err)
		}
	}
	return derr
}
