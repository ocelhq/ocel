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

	"github.com/ocelhq/ocel/cloud/aws/bootstrap"
	"github.com/ocelhq/ocel/cloud/aws/deploy"
	"github.com/ocelhq/ocel/cloud/aws/pulumirt"
	"github.com/ocelhq/ocel/cloud/edge"
	deploymentsv1 "github.com/ocelhq/ocel/pkg/proto/deployments/v1"
)

// Prune reclaims a production project's old Deployments (ADR 0001): it
// resolves the same account-global state Deploy does, then drives
// deploy.Prune, which enforces the keepN-deep retention window through the
// project's already-reconciled root stack and reclaims what it collects. It
// backs `ocel deployments prune` and never runs inline on a deploy.
//
// Like Deploy and Bootstrap it streams progress/log events — a reclaim's
// per-stack destroys run for minutes — ending in a terminal ResultEvent. The
// kept-vs-reclaimed summary rides the stream as the final progress lines.
func (s *Server) Prune(ctx context.Context, req *deploymentsv1.PruneRequest, stream *connect.ServerStream[deploymentsv1.DeployEvent]) error {
	progress := func(m string) { _ = stream.Send(phaseProgressEvent(deploymentsv1.Phase_PHASE_PROVISIONING, m, 0, 0)) }
	logf := func(m string) { _ = stream.Send(logEvent(m)) }

	result, err := s.runPrune(ctx, req, progress, logf)
	if err != nil {
		return stream.Send(resultEvent(false, err.Error(), nil, nil))
	}
	for _, line := range pruneSummaryLines(result) {
		if err := stream.Send(progressEvent(line)); err != nil {
			return err
		}
	}
	return stream.Send(resultEvent(true, "", nil, nil))
}

// runPrune resolves state and drives deploy.Prune, returning what it reclaimed.
// A project that has never had a production deploy is not an error: it simply
// has nothing to prune, reported as an empty result.
func (s *Server) runPrune(ctx context.Context, req *deploymentsv1.PruneRequest, progress, logf func(string)) (edge.PruneResult, error) {
	opts, err := parseOptions(req.GetOptions())
	if err != nil {
		return edge.PruneResult{}, connect.NewError(connect.CodeInvalidArgument, err)
	}

	stack, state, err := s.rootStack(ctx, opts, req.GetProjectId())
	if err != nil {
		if errors.Is(err, errNoProductionDeploy) {
			return edge.PruneResult{}, nil
		}
		return edge.PruneResult{}, err
	}

	cfg, err := pruneConfig(ctx, opts)
	if err != nil {
		return edge.PruneResult{}, err
	}

	return deploy.Prune(ctx, stack, state, cfg, req.GetProjectId(), int(req.GetKeepN()), "", progress, logf)
}

// pruneSummaryLines renders the kept-vs-reclaimed outcome as the human-readable
// lines Prune streams as its final progress events, mirroring what the CLI used
// to print from the (now removed) PruneResponse.
func pruneSummaryLines(result edge.PruneResult) []string {
	if len(result.RemovedPromotionIDs) == 0 {
		return []string{"Nothing to prune."}
	}
	return []string{
		fmt.Sprintf("Reclaimed %d promotion(s): %s", len(result.RemovedPromotionIDs), strings.Join(result.RemovedPromotionIDs, ", ")),
		fmt.Sprintf("Kept %d promotion(s).", len(result.KeptPromotionIDs)),
	}
}

// pruneConfig resolves the account-global state a prune needs to reclaim
// storage: the Pulumi backend a stack destroy selects against, and the S3/R2
// buckets+clients a build's static-asset and ISR/prerender prefixes are
// deleted from. It reuses the same bootstrap reads runDeploy's production
// path does, narrowed to only what Reclaim touches — no artifact bucket, no
// edge credentials/values, no per-app manifest state.
func pruneConfig(ctx context.Context, opts options) (deploy.Config, error) {
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
	pulumiCmd, err := pulumirt.Ensure(ctx, nil)
	if err != nil {
		return deploy.Config{}, err
	}

	// Best-effort, like Deploy's own read: a project whose edge never adopted
	// a cache store simply reclaims nothing from CacheStoreBucket (deletePrefix
	// is a no-op on an empty bucket) — every asset it wrote instead lives in
	// AssetBucket, which is still reclaimed.
	cacheStore, err := bootstrap.ReadCacheStore(ctx, ssmClient, bootstrap.ClassProduction)
	if err != nil {
		cacheStore = bootstrap.CacheStore{}
	}

	return deploy.Config{
		Region:             awscfg.Region,
		BackendURL:         "s3://" + deployed.StateBucket,
		Passphrase:         passphrase,
		ProjectName:        pulumiProjectName,
		Pulumi:             pulumiCmd,
		AssetBucket:        deployed.AssetBucket,
		Uploader:           s3.NewFromConfig(awscfg),
		CacheStoreBucket:   cacheStore.Bucket,
		CacheStoreUploader: cacheStoreUploader(cacheStore),
		Env:                deployEnv,
	}, nil
}
