package server

import (
	"context"
	"errors"

	connect "connectrpc.com/connect"

	"github.com/aws/aws-sdk-go-v2/service/cloudformation"
	"github.com/aws/aws-sdk-go-v2/service/ssm"

	"github.com/ocelhq/ocel/pkg/naming"

	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"
	progressv1 "github.com/ocelhq/ocel/pkg/proto/common/progress/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/platform/aws/provider/bootstrap"
	"github.com/ocelhq/ocel/platform/aws/provider/certs"
	"github.com/ocelhq/ocel/platform/aws/provider/deploy"
	"github.com/ocelhq/ocel/platform/aws/provider/dns"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

func (s *Server) PlanRemoveProject(ctx context.Context, req *contractv1.ProjectRequest) (*contractv1.RemovalPlan, error) {
	opts := s.config.get()

	edgeFront, err := s.edge(requestedEdge(req), opts.Region)
	if err != nil {
		return nil, err
	}
	if req.GetEnvironment().GetTier() == environmentv1.Tier_TIER_PREVIEW {
		return s.planDestroyPreviewProject(ctx, opts, edgeFront, req.GetSlug())
	}

	awscfg, params, err := productionTeardownParams(ctx, opts, req.GetSlug())
	if err != nil {
		return nil, err
	}
	deps, err := s.productionTeardownDeps(ctx, opts, awscfg, params, req.GetSlug())
	if err != nil {
		return nil, err
	}
	plan, err := deploy.PlanProjectTeardown(ctx, deps.teardown.Stacks, req.GetSlug())
	if err != nil {
		return nil, err
	}
	deployed, err := s.deployed(ctx, cloudformation.NewFromConfig(awscfg), opts.Region, false)
	if err != nil {
		return nil, err
	}

	var infraStacks []naming.StackName
	if !plan.InfraStack.IsZero() {
		infraStacks = []naming.StackName{plan.InfraStack}
	}
	removal, err := s.projectRemovalPlan(edgeFront, projectPlanScope{
		class:      bootstrap.ClassProduction,
		slug:       req.GetSlug(),
		stateTable: deployed.StateTable,
		stacks:     len(plan.AppStacks) + len(infraStacks),
		record:     params.Stack,
	})
	if err != nil {
		return nil, err
	}
	indexed, err := deploy.ProjectIndexed(ctx, deps.teardown.Stacks, req.GetSlug())
	if err != nil {
		return nil, err
	}
	removal.Items = append(removal.Items, stackItems(infraStacks, plan.AppStacks)...)
	return withProjectIndex(removal, indexed, req.GetSlug()), nil
}

func (s *Server) planDestroyPreviewProject(ctx context.Context, opts providerConfig, edgeFront edge.Edge, slug string) (*contractv1.RemovalPlan, error) {
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
	plan, err := deploy.PlanPreviewProjectTeardown(ctx, stacks, slug)
	if err != nil {
		return nil, err
	}
	record, err := bootstrap.ReadStackRecordFor(ctx, ssm.NewFromConfig(awscfg), bootstrap.ClassPreview, slug)
	if err != nil {
		return nil, err
	}

	removal, err := s.projectRemovalPlan(edgeFront, projectPlanScope{
		class:      bootstrap.ClassPreview,
		slug:       slug,
		stateTable: deployed.StateTable,
		stacks:     len(plan.AppStacks) + len(plan.InfraStacks),
		record:     record,
	})
	if err != nil {
		return nil, err
	}
	indexed, err := deploy.ProjectIndexed(ctx, stacks, slug)
	if err != nil {
		return nil, err
	}
	removal.Items = append(removal.Items, stackItems(plan.InfraStacks, plan.AppStacks)...)
	return withProjectIndex(removal, indexed, slug), nil
}

func withProjectIndex(plan *contractv1.RemovalPlan, indexed bool, slug string) *contractv1.RemovalPlan {
	if indexed {
		plan.Items = append(plan.Items, projectIndexItem(slug))
	}
	return plan
}

func (s *Server) projectRemovalPlan(edgeFront edge.Edge, scope projectPlanScope) (*contractv1.RemovalPlan, error) {
	plan := &contractv1.RemovalPlan{EdgeKind: string(edgeFront.Kind()), Subject: scope.slug}

	if _, err := openStackOn(edgeFront, scope.record); err != nil {
		if errors.Is(err, errNoProductionDeploy) {
			return plan, nil
		}
		return nil, err
	}

	plan.Items = destroyPlanItems(edgeFront, scope)
	return plan, nil
}

func (s *Server) RemoveProject(ctx context.Context, req *contractv1.ProjectRequest, stream *connect.ServerStream[progressv1.OperationEvent]) (err error) {
	sender := newEventSender(ctx, stream.Send)
	defer func() { err = errors.Join(err, sender.close()) }()
	tracer := newEventTracer(sender)
	stageReport, logf := newTeardownReporter(sender)

	if derr := s.runDestroyProject(ctx, req, tracer, stageReport, logf); derr != nil {
		return sender.fail(derr)
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

func (s *Server) runDestroyProject(ctx context.Context, req *contractv1.ProjectRequest, tracer deploy.Tracer, stageReport func(deploy.StageID) func(string), logf func(string)) error {
	if env := req.GetEnvironment(); env.GetTier() == environmentv1.Tier_TIER_PREVIEW {
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

	opts := s.config.get()
	awscfg, params, err := productionTeardownParams(ctx, opts, req.GetSlug())
	if err != nil {
		return finish(err)
	}
	stack, err := s.openStackFor(requestedEdge(req), params.Stack, awscfg.Region)
	if err != nil && !errors.Is(err, errNoProductionDeploy) {
		return finish(err)
	}
	deps, err := s.productionTeardownDeps(ctx, opts, awscfg, params, req.GetSlug())
	if err != nil {
		return finish(err)
	}
	writer, err := dns.WriterFor(requestedDNS(req).GetKind(), requestedDNS(req).GetZone(), dns.Deps{AWS: awscfg})
	if err != nil {
		return finish(err)
	}

	result, derr := deploy.DestroyProject(ctx, stack, deps.projectTeardown(reportingWith(tracer, stageReport), writer), stages, logf)

	if teardownFinished(result, params.Stack) {
		discarder := func(cert certs.Certificate) certs.Issuer {
			return certs.DiscardIssuerFor(cert, certs.Deps{AWS: awscfg})
		}
		if err := releaseProductionDomains(ctx, params.Stack.Production, writer, discarder, logf); err != nil {
			derr = errors.Join(derr, err)
		}
	}
	if err := forgetStackRecord(ctx, ssm.NewFromConfig(awscfg), bootstrap.ClassProduction, req.GetSlug(), result, params.Stack, derr); err != nil {
		derr = errors.Join(derr, err)
	}
	return derr
}

func teardownFinished(result deploy.DestroyProjectResult, recorded bootstrap.StackRecord) bool {
	return result.EdgeTornDown && !recorded.Empty()
}

func forgetStackRecord(ctx context.Context, ssmClient bootstrap.SSMAPI, class, slug string, result deploy.DestroyProjectResult, recorded bootstrap.StackRecord, failed error) error {
	if failed != nil || !teardownFinished(result, recorded) {
		return nil
	}
	return bootstrap.DeleteStackRecordFor(ctx, ssmClient, class, slug)
}

func (s *Server) runDestroyPreviewProject(ctx context.Context, req *contractv1.ProjectRequest, env *environmentv1.Environment, tracer deploy.Tracer, stageReport func(deploy.StageID) func(string), logf func(string)) error {
	slug := req.GetSlug()

	stages := newDestroyPreviewProjectStages()
	deploy.DeclareStages(tracer, false, stages.Roots()...)
	finish := func(err error) error {
		if err != nil {
			closeStages(tracer)
		}
		return err
	}

	opts := s.config.get()
	deps, stack, err := s.previewTeardownDeps(ctx, requestedEdge(req), opts, slug, env)
	if err != nil {
		return finish(err)
	}
	awscfg, err := loadAWS(ctx, opts.Region)
	if err != nil {
		return finish(err)
	}
	writer, err := dns.WriterFor(requestedDNS(req).GetKind(), requestedDNS(req).GetZone(), dns.Deps{AWS: awscfg})
	if err != nil {
		return finish(err)
	}

	result, derr := deploy.DestroyPreviewProject(ctx, stack, deps.projectTeardown(reportingWith(tracer, stageReport), writer), stages, logf)

	if err := forgetStackRecord(ctx, ssm.NewFromConfig(awscfg), bootstrap.ClassPreview, slug, result, bootstrap.StackRecord{Edge: stack.State()}, derr); err != nil {
		derr = errors.Join(derr, err)
	}
	return derr
}
