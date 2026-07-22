package server

import (
	"context"
	"errors"

	connect "connectrpc.com/connect"

	"github.com/aws/aws-sdk-go-v2/service/ssm"

	"github.com/ocelhq/ocel/cloud/aws/bootstrap"
	"github.com/ocelhq/ocel/cloud/aws/deploy"
	deploymentsv1 "github.com/ocelhq/ocel/pkg/proto/deployments/v1"
)

// PlanDestroyProject enumerates, without removing anything, what a
// DestroyProject would tear down: the project's app-deploy stacks, its infra
// stack, and whether it has a root stack. The CLI calls it to show the blast
// radius before prompting and to exit cleanly when there is nothing to destroy.
func (s *Server) PlanDestroyProject(ctx context.Context, req *deploymentsv1.PlanDestroyProjectRequest) (*deploymentsv1.PlanDestroyProjectResponse, error) {
	opts, err := parseOptions(req.GetOptions())
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}

	cfg, err := pruneConfig(ctx, opts)
	if err != nil {
		return nil, err
	}
	plan, err := deploy.PlanProjectTeardown(ctx, cfg, req.GetProjectId())
	if err != nil {
		return nil, err
	}

	rootStack, err := s.hasRootStack(ctx, opts, req.GetProjectId())
	if err != nil {
		return nil, err
	}

	return &deploymentsv1.PlanDestroyProjectResponse{
		AppStacks:  plan.AppStacks,
		InfraStack: plan.InfraStack,
		RootStack:  rootStack,
	}, nil
}

// hasRootStack reports whether a project has a reconciled root stack, treating
// "never deployed to production" as simply absent rather than an error.
func (s *Server) hasRootStack(ctx context.Context, opts options, projectID string) (bool, error) {
	_, state, err := s.rootStack(ctx, opts, projectID)
	if err != nil {
		if errors.Is(err, errNoProductionDeploy) {
			return false, nil
		}
		return false, err
	}
	return len(state) > 0, nil
}

// DestroyProject tears down an entire production project (ADR 0001): the
// imperative root stack, every app-deploy stack, and the stateful infra stack,
// plus the project's R2/S3 assets and — once the root stack is gone — its
// persisted root-stack state. It resolves the same account-global state Deploy
// and Prune do, then drives deploy.DestroyProject. Like them it streams
// progress/log events (a teardown runs for minutes) ending in a terminal
// ResultEvent, and it is best-effort: it reports failure but leaves nothing it
// could remove behind, so a re-run resumes.
func (s *Server) DestroyProject(ctx context.Context, req *deploymentsv1.DestroyProjectRequest, stream *connect.ServerStream[deploymentsv1.DeployEvent]) error {
	progress := func(m string) { _ = stream.Send(phaseProgressEvent(deploymentsv1.Phase_PHASE_PROVISIONING, m, 0, 0)) }
	logf := func(m string) { _ = stream.Send(logEvent(m)) }

	if err := s.runDestroyProject(ctx, req, progress, logf); err != nil {
		return stream.Send(resultEvent(false, err.Error(), nil, nil))
	}
	return stream.Send(resultEvent(true, "", nil, nil))
}

func (s *Server) runDestroyProject(ctx context.Context, req *deploymentsv1.DestroyProjectRequest, progress, logf func(string)) error {
	opts, err := parseOptions(req.GetOptions())
	if err != nil {
		return connect.NewError(connect.CodeInvalidArgument, err)
	}

	// A preview environment destroys the whole preview footprint on the preview
	// substrate; anything else destroys the production project.
	if env := req.GetEnvironment(); env.GetClass() == deploymentsv1.Environment_CLASS_PREVIEW {
		return s.runDestroyPreviewProject(ctx, opts, req.GetProjectId(), env, progress, logf)
	}

	// A project may have no root stack (a first deploy that aborted before
	// reconcile) yet still own orphaned infra/app stacks; that is not an error,
	// it just leaves nothing edge-side to remove.
	stack, state, err := s.rootStack(ctx, opts, req.GetProjectId())
	if err != nil && !errors.Is(err, errNoProductionDeploy) {
		return err
	}

	cfg, err := pruneConfig(ctx, opts)
	if err != nil {
		return err
	}

	result, derr := deploy.DestroyProject(ctx, stack, state, cfg, req.GetProjectId(), progress, logf)

	// Forget the persisted root-stack state only once the root stack is gone:
	// deleting it while workers still stand would strip the identities a re-run
	// needs to finish the teardown.
	if result.RootTornDown && len(state) > 0 {
		if err := s.deleteRootStackState(ctx, opts, req.GetProjectId()); err != nil {
			derr = errors.Join(derr, err)
		}
	}
	return derr
}

// deleteRootStackState removes a project's persisted root-stack state, the final
// step of a successful root-stack teardown.
func (s *Server) deleteRootStackState(ctx context.Context, opts options, projectID string) error {
	awscfg, err := loadAWS(ctx, opts.Region)
	if err != nil {
		return err
	}
	return bootstrap.DeleteRootStackState(ctx, ssm.NewFromConfig(awscfg), projectID)
}

// runDestroyPreviewProject tears down a whole project's preview footprint on the
// preview substrate: every preview pointer's app-deploy and per-name infra
// stacks, the preview store instance, the preview root worker(s), the R2 assets,
// and — once the store instance and workers are gone — the persisted preview
// root-stack state. The account-level preview bootstrap is left intact.
func (s *Server) runDestroyPreviewProject(ctx context.Context, opts options, projectID string, env *deploymentsv1.Environment, progress, logf func(string)) error {
	cfg, stack, state, err := s.previewTeardownContext(ctx, opts, projectID, env)
	if err != nil {
		return err
	}

	derr := deploy.DestroyPreviewProject(ctx, stack, state, cfg, projectID, progress, logf)

	// Forget the persisted preview root-stack state only once the store instance
	// and workers are gone, so a re-run of a partial teardown still holds the
	// identities it needs. A teardown that failed leaves the state in place.
	if derr == nil && len(state) > 0 {
		awscfg, awsErr := loadAWS(ctx, opts.Region)
		if awsErr != nil {
			return awsErr
		}
		if err := bootstrap.DeleteRootStackStateFor(ctx, ssm.NewFromConfig(awscfg), bootstrap.ClassPreview, projectID); err != nil {
			derr = errors.Join(derr, err)
		}
	}
	return derr
}
