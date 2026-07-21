package server

import (
	"context"
	"errors"
	"fmt"

	connect "connectrpc.com/connect"

	"github.com/aws/aws-sdk-go-v2/service/cloudformation"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/ssm"

	"github.com/ocelhq/ocel/cloud/aws/bootstrap"
	"github.com/ocelhq/ocel/cloud/aws/deploy"
	"github.com/ocelhq/ocel/cloud/aws/pulumirt"
	deploymentsv1 "github.com/ocelhq/ocel/pkg/proto/deployments/v1"
)

// Prune reclaims a production project's old Deployments (ADR 0001): it
// resolves the same account-global state Deploy does, then drives
// deploy.Prune, which enforces the keepN-deep retention window through the
// project's already-reconciled root tier and reclaims what it collects. It
// backs `ocel deployments prune` and never runs inline on a deploy.
func (s *Server) Prune(ctx context.Context, req *deploymentsv1.PruneRequest) (*deploymentsv1.PruneResponse, error) {
	opts, err := parseOptions(req.GetOptions())
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}

	tier, state, err := s.rootTier(ctx, opts, req.GetProjectId())
	if err != nil {
		if errors.Is(err, errNoProductionDeploy) {
			return &deploymentsv1.PruneResponse{}, nil
		}
		return nil, err
	}

	cfg, err := pruneConfig(ctx, opts)
	if err != nil {
		return nil, err
	}

	result, err := deploy.Prune(ctx, tier, state, cfg, req.GetProjectId(), int(req.GetKeepN()), nil, nil)
	if err != nil {
		return nil, err
	}
	return &deploymentsv1.PruneResponse{
		KeptPromotionIds:    result.KeptPromotionIDs,
		RemovedPromotionIds: result.RemovedPromotionIDs,
	}, nil
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
