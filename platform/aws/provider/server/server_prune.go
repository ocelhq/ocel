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
	deploymentsv1 "github.com/ocelhq/ocel/pkg/proto/deployments/v1"
	"github.com/ocelhq/ocel/platform/aws/provider/bootstrap"
	"github.com/ocelhq/ocel/platform/aws/provider/deploy"
	"github.com/ocelhq/ocel/platform/aws/provider/pulumiruntime"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

func (s *Server) Prune(ctx context.Context, req *deploymentsv1.PruneRequest, stream *connect.ServerStream[deploymentsv1.DeployEvent]) (err error) {
	sender := newEventSender(ctx, stream.Send)
	defer func() { err = sender.close() }()
	tracer := newEventTracer(sender)
	stageReport, logf := newTeardownReporter(sender)

	result, perr := s.runPrune(ctx, req, tracer, stageReport, logf)
	if perr != nil {
		sender.send(failureResult(perr))
		return nil
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

func (s *Server) runPrune(ctx context.Context, req *deploymentsv1.PruneRequest, tracer deploy.Tracer, stageReport func(deploy.StageID) func(string), logf func(string)) (edge.PruneResult, error) {
	stages := newPruneStages()
	deploy.DeclareStages(tracer, false, stages.Diff, stages.Reclaim)
	finish := func(err error) (edge.PruneResult, error) {
		closeStages(tracer)
		return edge.PruneResult{}, err
	}

	opts, err := parseOptions(req.GetOptions())
	if err != nil {
		return finish(connect.NewError(connect.CodeInvalidArgument, err))
	}

	if env := req.GetEnvironment(); env.GetClass() == deploymentsv1.Environment_CLASS_PREVIEW {
		pointer, err := deploy.EnvName(env)
		if err != nil {
			return finish(connect.NewError(connect.CodeInvalidArgument, err))
		}
		cfg, stack, err := s.previewTeardownContext(ctx, edge.Kind(req.GetEdgeKind()), opts, req.GetSlug(), env)
		if err != nil {
			return finish(err)
		}
		if len(stack.State()) == 0 {
			return finish(nil)
		}
		cfg.Tracer = tracer
		cfg.StageReport = stageReport
		return deploy.Prune(ctx, stack, cfg, req.GetSlug(), int(req.GetKeepN()), pointer, stages, logf)
	}

	awscfg, params, err := productionTeardownParams(ctx, opts, req.GetSlug())
	if err != nil {
		return finish(err)
	}
	stack, err := s.openStackFor(edge.Kind(req.GetEdgeKind()), params.StackState, awscfg.Region)
	if err != nil {
		if errors.Is(err, errNoProductionDeploy) {
			return finish(nil)
		}
		return finish(err)
	}

	cfg, err := s.pruneConfig(ctx, opts, awscfg, params, req.GetSlug())
	if err != nil {
		return finish(err)
	}
	cfg.Tracer = tracer
	cfg.StageReport = stageReport

	return deploy.Prune(ctx, stack, cfg, req.GetSlug(), int(req.GetKeepN()), "", stages, logf)
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

func productionTeardownParams(ctx context.Context, opts options, slug string) (aws.Config, bootstrap.TeardownParams, error) {
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

func (s *Server) pruneConfig(ctx context.Context, opts options, awscfg aws.Config, params bootstrap.TeardownParams, slug string) (deploy.Config, error) {
	cfn := cloudformation.NewFromConfig(awscfg)

	deployed, err := s.deployed(ctx, cfn, opts.Region, false)
	if err != nil {
		return deploy.Config{}, err
	}
	if deployed.StateBucket == "" {
		return deploy.Config{}, fmt.Errorf("account bootstrap is present but its state bucket is missing (a partial rollback?); re-run `ocel bootstrap`")
	}
	if deployed.AssetBucket == "" {
		return deploy.Config{}, fmt.Errorf("account bootstrap is present but its asset bucket is missing (a partial rollback?); re-run `ocel bootstrap`")
	}
	stacks, err := stackIndexFor(awscfg, deployed, bootstrapCommand(false))
	if err != nil {
		return deploy.Config{}, err
	}

	if params.PassphraseErr != nil {
		return deploy.Config{}, params.PassphraseErr
	}
	pulumiCmd, err := pulumiruntime.Ensure(ctx, nil)
	if err != nil {
		return deploy.Config{}, err
	}

	return deploy.Config{
		Region:             awscfg.Region,
		BackendURL:         naming.StateBackendURL(deployed.StateBucket, slug),
		Passphrase:         params.Passphrase,
		PulumiProject:      naming.PulumiProject(slug),
		Pulumi:             pulumiCmd,
		AssetBucket:        deployed.AssetBucket,
		ArtifactBucket:     deployed.ArtifactBucket,
		Uploader:           s3.NewFromConfig(awscfg),
		CacheStoreBucket:   params.CacheStore.Bucket,
		CacheStoreUploader: cacheStoreUploader(params.CacheStore),
		Stacks:             stacks,
		Env:                deployEnv,
		Slug:               slug,
		Values:             teardownValues(awscfg, deployed, bootstrap.ClassProduction),

		ISRWriterEndpoint:      params.ISRWriter.Endpoint,
		ISRWriterBootstrapCred: params.ISRWriter.BootstrapCred,
	}, nil
}
