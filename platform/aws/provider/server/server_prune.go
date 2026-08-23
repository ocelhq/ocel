package server

import (
	"context"
	"errors"
	"fmt"
	"strings"

	connect "connectrpc.com/connect"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/cloudformation"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/ssm"

	"github.com/ocelhq/ocel/pkg/naming"
	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"
	progressv1 "github.com/ocelhq/ocel/pkg/proto/common/progress/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/platform/aws/provider/bootstrap"
	"github.com/ocelhq/ocel/platform/aws/provider/deploy"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

func (s *Server) RemoveStalePromotions(ctx context.Context, req *contractv1.RemoveStalePromotionsRequest, stream *connect.ServerStream[progressv1.OperationEvent]) (err error) {
	sender := newEventSender(ctx, stream.Send)
	defer func() { err = errors.Join(err, sender.close()) }()
	tracer := newEventTracer(sender)
	stageReport, logf := newTeardownReporter(sender)

	result, perr := s.runPrune(ctx, req, tracer, stageReport, logf)
	if perr != nil {
		return sender.fail(perr)
	}
	for _, line := range pruneSummaryLines(result) {
		sender.send(progressEvent(line))
	}
	sender.send(okResult())
	return nil
}

func newPruneStages() deploy.PruneStages {
	return deploy.PruneStages{
		Diff:    deploy.NewRootStage("Diffing deployments to reclaim"),
		Reclaim: deploy.NewRootStage("Reclaiming deployments"),
	}
}

func (s *Server) runPrune(ctx context.Context, req *contractv1.RemoveStalePromotionsRequest, tracer deploy.Tracer, stageReport func(deploy.StageID) func(string), logf func(string)) (edge.PruneResult, error) {
	stages := newPruneStages()
	deploy.DeclareStages(tracer, false, stages.Diff, stages.Reclaim)
	finish := func(err error) (edge.PruneResult, error) {
		closeStages(tracer)
		return edge.PruneResult{}, err
	}

	opts := s.config.get()

	if env := req.GetEnvironment(); env.GetTier() == environmentv1.Tier_TIER_PREVIEW {
		pointer, err := deploy.EnvName(env)
		if err != nil {
			return finish(connect.NewError(connect.CodeInvalidArgument, err))
		}
		deps, stack, err := s.previewTeardownDeps(ctx, requestedEdge(req), opts, req.GetSlug(), env)
		if err != nil {
			return finish(err)
		}
		if stack.State().Empty() {
			return finish(nil)
		}
		return deploy.Prune(ctx, stack, deps.reclamation(reportingWith(tracer, stageReport)), int(req.GetKeepN()), pointer, stages, logf)
	}

	awscfg, params, err := productionTeardownParams(ctx, opts, req.GetSlug())
	if err != nil {
		return finish(err)
	}
	record, err := s.stackRecord(ctx, opts.Region, edge.ClassProduction, req.GetSlug())
	if err != nil {
		return finish(err)
	}
	stack, err := s.openStackFor(requestedEdge(req), record, awscfg.Region)
	if err != nil {
		if errors.Is(err, errNoProductionDeploy) {
			return finish(nil)
		}
		return finish(err)
	}

	deps, err := s.productionTeardownDeps(ctx, opts, awscfg, params, req.GetSlug())
	if err != nil {
		return finish(err)
	}

	return deploy.Prune(ctx, stack, deps.reclamation(reportingWith(tracer, stageReport)), int(req.GetKeepN()), "", stages, logf)
}

func reportingWith(tracer deploy.Tracer, stageReport func(deploy.StageID) func(string)) deploy.Reporting {
	return deploy.Reporting{Tracer: tracer, StageReport: stageReport}
}

func pruneSummaryLines(result edge.PruneResult) []string {
	if len(result.RemovedPromotionIDs) == 0 {
		return []string{"Nothing to prune."}
	}
	return []string{
		fmt.Sprintf("Reclaimed %d promotion(s): %s", len(result.RemovedPromotionIDs), strings.Join(result.RemovedPromotionIDs, ", ")),
		fmt.Sprintf("Kept %d promotion(s).", len(result.KeptPromotionIDs)),
	}
}

func productionTeardownParams(ctx context.Context, opts providerConfig, slug string) (aws.Config, bootstrap.TeardownParams, error) {
	awscfg, err := loadAWS(ctx, opts.Region)
	if err != nil {
		return aws.Config{}, bootstrap.TeardownParams{}, err
	}
	params, err := bootstrap.ReadTeardownParams(ctx, ssm.NewFromConfig(awscfg), bootstrap.ClassProduction, slug)
	if err != nil {
		return aws.Config{}, bootstrap.TeardownParams{}, err
	}
	return awscfg, params, nil
}

type teardownContext struct {
	teardown  deploy.Teardown
	isrWriter deploy.ISRWriterAccess
	values    deploy.ValueStore
}

func (d teardownContext) reclamation(rep deploy.Reporting) deploy.Reclamation {
	d.teardown.Report = rep
	return deploy.Reclamation{Teardown: d.teardown, ISRWriter: d.isrWriter}
}

func (d teardownContext) projectTeardown(rep deploy.Reporting, writer edge.DNSWriter) deploy.ProjectTeardown {
	d.teardown.Report = rep
	return deploy.ProjectTeardown{Teardown: d.teardown, Values: d.values, DNS: writer}
}

func (s *Server) productionTeardownDeps(ctx context.Context, opts providerConfig, awscfg aws.Config, params bootstrap.TeardownParams, slug string) (teardownContext, error) {
	cfn := cloudformation.NewFromConfig(awscfg)

	deployed, err := s.deployed(ctx, cfn, opts.Region, false)
	if err != nil {
		return teardownContext{}, err
	}
	if deployed.StateBucket == "" {
		return teardownContext{}, fmt.Errorf("account bootstrap is present but its state bucket is missing (a partial rollback?); re-run `ocel bootstrap`")
	}
	if deployed.AssetBucket == "" {
		return teardownContext{}, fmt.Errorf("account bootstrap is present but its asset bucket is missing (a partial rollback?); re-run `ocel bootstrap`")
	}
	stacks, err := stackIndexFor(awscfg, deployed, bootstrapCommand(false))
	if err != nil {
		return teardownContext{}, err
	}

	if params.PassphraseErr != nil {
		return teardownContext{}, params.PassphraseErr
	}
	return teardownContext{
		teardown: deploy.Teardown{
			Pulumi: deploy.PulumiAccess{
				Region:        awscfg.Region,
				BackendURL:    naming.StateBackendURL(deployed.StateBucket, slug),
				Passphrase:    params.Passphrase,
				PulumiProject: naming.PulumiProject(slug),
			},
			Slug:   slug,
			Env:    deployEnv,
			Stacks: stacks,
			Stores: deploy.ObjectStores{
				Uploader:           s3.NewFromConfig(awscfg),
				ArtifactBucket:     deployed.ArtifactBucket,
				AssetBucket:        deployed.AssetBucket,
				CacheStoreBucket:   params.CacheStore.Bucket,
				CacheStoreUploader: cacheStoreUploader(params.CacheStore),
			},
		},
		isrWriter: deploy.ISRWriterAccess{
			Endpoint:      params.ISRWriter.Endpoint,
			BootstrapCred: params.ISRWriter.BootstrapCred,
		},
		values: teardownValues(awscfg, deployed, bootstrap.ClassProduction),
	}, nil
}
