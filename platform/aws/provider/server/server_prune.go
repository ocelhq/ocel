package server

import (
	"context"
	"errors"
	"fmt"
	"strings"

	connect "connectrpc.com/connect"

	"github.com/aws/aws-sdk-go-v2/service/cloudformation"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/ssm"

	deploymentsv1 "github.com/ocelhq/ocel/pkg/proto/deployments/v1"
	"github.com/ocelhq/ocel/platform/aws/provider/bootstrap"
	"github.com/ocelhq/ocel/platform/aws/provider/deploy"
	"github.com/ocelhq/ocel/platform/aws/provider/pulumiruntime"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

func (s *Server) Prune(ctx context.Context, req *deploymentsv1.PruneRequest, stream *connect.ServerStream[deploymentsv1.DeployEvent]) error {
	progress := func(m string) { _ = stream.Send(phaseProgressEvent(deploymentsv1.Phase_PHASE_PROVISIONING, m, 0, 0)) }
	logf := func(m string) { _ = stream.Send(logEvent(m)) }

	result, err := s.runPrune(ctx, req, progress, logf)
	if err != nil {
		return stream.Send(failureResult(err))
	}
	for _, line := range pruneSummaryLines(result) {
		if err := stream.Send(progressEvent(line)); err != nil {
			return err
		}
	}
	return stream.Send(okResult())
}

func (s *Server) runPrune(ctx context.Context, req *deploymentsv1.PruneRequest, progress, logf func(string)) (edge.PruneResult, error) {
	opts, err := parseOptions(req.GetOptions())
	if err != nil {
		return edge.PruneResult{}, connect.NewError(connect.CodeInvalidArgument, err)
	}

	if env := req.GetEnvironment(); env.GetClass() == deploymentsv1.Environment_CLASS_PREVIEW {
		cfg, stack, state, err := s.previewTeardownContext(ctx, opts, req.GetSlug(), env)
		if err != nil {
			return edge.PruneResult{}, err
		}
		if len(state) == 0 {
			return edge.PruneResult{}, nil
		}
		return deploy.Prune(ctx, stack, state, cfg, req.GetSlug(), int(req.GetKeepN()), env.GetIdentity(), progress, logf)
	}

	stack, state, err := s.rootStack(ctx, opts, req.GetSlug())
	if err != nil {
		if errors.Is(err, errNoProductionDeploy) {
			return edge.PruneResult{}, nil
		}
		return edge.PruneResult{}, err
	}

	cfg, err := pruneConfig(ctx, opts, req.GetSlug())
	if err != nil {
		return edge.PruneResult{}, err
	}

	return deploy.Prune(ctx, stack, state, cfg, req.GetSlug(), int(req.GetKeepN()), "", progress, logf)
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

func pruneConfig(ctx context.Context, opts options, slug string) (deploy.Config, error) {
	awscfg, err := loadAWS(ctx, opts.Region)
	if err != nil {
		return deploy.Config{}, err
	}
	cfn := cloudformation.NewFromConfig(awscfg)
	ssmClient := ssm.NewFromConfig(awscfg)

	deployed, err := checkBootstrap(ctx, cfn, false)
	if err != nil {
		return deploy.Config{}, err
	}
	if deployed.StateBucket == "" {
		return deploy.Config{}, fmt.Errorf("account bootstrap is present but its state bucket is missing (a partial rollback?); re-run `ocel bootstrap`")
	}
	if deployed.AssetBucket == "" {
		return deploy.Config{}, fmt.Errorf("account bootstrap is present but its asset bucket is missing (a partial rollback?); re-run `ocel bootstrap`")
	}

	passphrase, err := bootstrap.ReadPassphrase(ctx, ssmClient)
	if err != nil {
		return deploy.Config{}, err
	}
	pulumiCmd, err := pulumiruntime.Ensure(ctx, nil)
	if err != nil {
		return deploy.Config{}, err
	}

	cacheStore, err := bootstrap.ReadCacheStore(ctx, ssmClient, bootstrap.ClassProduction)
	if err != nil {
		cacheStore = bootstrap.CacheStore{}
	}

	isrWriter, err := bootstrap.ReadISRWriterFor(ctx, ssmClient, bootstrap.ClassProduction)
	if err != nil {
		isrWriter = bootstrap.ISRWriter{}
	}

	return deploy.Config{
		Region:             awscfg.Region,
		BackendURL:         deploy.StateBackendURL(deployed.StateBucket, slug),
		Passphrase:         passphrase,
		ProjectName:        pulumiProjectName,
		Pulumi:             pulumiCmd,
		AssetBucket:        deployed.AssetBucket,
		ArtifactBucket:     deployed.ArtifactBucket,
		Uploader:           s3.NewFromConfig(awscfg),
		CacheStoreBucket:   cacheStore.Bucket,
		CacheStoreUploader: cacheStoreUploader(cacheStore),
		Stacks:             stackIndexFor(awscfg, deployed),
		Env:                deployEnv,
		Values:             teardownValues(awscfg, deployed, bootstrap.ClassProduction),

		ISRWriterEndpoint:      isrWriter.Endpoint,
		ISRWriterBootstrapCred: isrWriter.BootstrapCred,
	}, nil
}
