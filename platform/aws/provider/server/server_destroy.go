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

func (s *Server) DestroyProject(ctx context.Context, req *deploymentsv1.DestroyProjectRequest, stream *connect.ServerStream[deploymentsv1.DeployEvent]) error {
	progress := func(m string) { _ = stream.Send(phaseProgressEvent(deploymentsv1.Phase_PHASE_PROVISIONING, m, 0, 0)) }
	logf := func(m string) { _ = stream.Send(logEvent(m)) }

	if err := s.runDestroyProject(ctx, req, progress, logf); err != nil {
		return stream.Send(failureResult(err))
	}
	return stream.Send(okResult())
}

func (s *Server) runDestroyProject(ctx context.Context, req *deploymentsv1.DestroyProjectRequest, progress, logf func(string)) error {
	opts, err := parseOptions(req.GetOptions())
	if err != nil {
		return connect.NewError(connect.CodeInvalidArgument, err)
	}

	if env := req.GetEnvironment(); env.GetClass() == deploymentsv1.Environment_CLASS_PREVIEW {
		return s.runDestroyPreviewProject(ctx, opts, req.GetSlug(), env, progress, logf)
	}

	awscfg, params, err := productionTeardownParams(ctx, opts, req.GetSlug())
	if err != nil {
		return err
	}
	stack, state, err := s.rootStackFor(params.RootStackState)
	if err != nil && !errors.Is(err, errNoProductionDeploy) {
		return err
	}

	cfg, err := s.pruneConfig(ctx, opts, awscfg, params, req.GetSlug())
	if err != nil {
		return err
	}

	result, derr := deploy.DestroyProject(ctx, stack, state, cfg, req.GetSlug(), progress, logf)

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

func (s *Server) runDestroyPreviewProject(ctx context.Context, opts options, slug string, env *deploymentsv1.Environment, progress, logf func(string)) error {
	cfg, stack, state, err := s.previewTeardownContext(ctx, opts, slug, env)
	if err != nil {
		return err
	}

	result, derr := deploy.DestroyPreviewProject(ctx, stack, state, cfg, slug, progress, logf)

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
