package alb

import (
	"context"
	"strings"

	"github.com/pulumi/pulumi-gcp/sdk/v9/go/gcp/compute"
	"github.com/pulumi/pulumi-gcp/sdk/v9/go/gcp/projects"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"

	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/progress"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/gcloud"
)

const objectViewerRole = "roles/storage.objectViewer"

const agentAppearsAttempts = 20

type AssetsBucket struct {
	Name  string
	Agent string
}

func declareAssetsBackend(ctx *pulumi.Context, names names, headers pulumi.StringArray, assets AssetsBucket, project pulumi.String) (*compute.BackendBucket, error) {
	args := &compute.BackendBucketArgs{
		Name:       pulumi.String(names.AssetsBackend),
		Project:    project,
		BucketName: pulumi.String(assets.Name),
		EnableCdn:  pulumi.Bool(true),
		CdnPolicy: &compute.BackendBucketCdnPolicyArgs{
			CacheMode:       pulumi.String(cacheOnOrigin),
			NegativeCaching: pulumi.Bool(false),
		},
	}
	if len(headers) > 0 {
		args.CustomResponseHeaders = headers
	}
	bucket, err := compute.NewBackendBucket(ctx, names.AssetsBackend, args)
	if err != nil {
		return nil, err
	}
	_, err = projects.NewIAMMember(ctx, names.AssetsReader, &projects.IAMMemberArgs{
		Project: project,
		Role:    pulumi.String(objectViewerRole),
		Member:  pulumi.String(assets.Agent),
		Condition: &projects.IAMMemberConditionArgs{
			Title:       pulumi.String(names.AssetsReader),
			Description: pulumi.String("lists the tier's edge bucket and reads the static files in it"),
			Expression:  pulumi.String(gcloud.ListedKindCondition(names.AssetsReader, assets.Name, naming.ObjectAssets).Expression),
		},
	}, pulumi.DependsOn([]pulumi.Resource{bucket}))
	return bucket, err
}

func (e *Edge) raiseFront(ctx context.Context, spec loadBalancerSpec, progress progress.Log) (map[string]string, error) {
	program, err := loadBalancerProgram(spec)
	if err != nil {
		return nil, err
	}
	for attempt := 1; ; attempt++ {
		outputs, err := e.deps.Stacks.Up(ctx, e.loadBalancerTarget(spec.Tier), program, progress)
		if err == nil {
			return outputs, program.Patches.RefuseUnclaimed()
		}
		if !isUnseenAgent(err, spec.Assets) || attempt == agentAppearsAttempts {
			return outputs, err
		}
		if !e.deps.WaitBeforeRetry(ctx, attempt) {
			return nil, err
		}
	}
}

func isUnseenAgent(err error, assets *AssetsBucket) bool {
	if assets == nil {
		return false
	}
	message := err.Error()
	return strings.Contains(message, strings.TrimPrefix(assets.Agent, "serviceAccount:")) && strings.Contains(message, "does not exist")
}
