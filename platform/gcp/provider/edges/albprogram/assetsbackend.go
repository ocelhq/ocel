package albprogram

import (
	"github.com/pulumi/pulumi-gcp/sdk/v9/go/gcp/compute"
	"github.com/pulumi/pulumi-gcp/sdk/v9/go/gcp/projects"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"

	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/platform/gcp/provider/edges/albresources"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/gcloud"
)

func declareAssetsBackend(ctx *pulumi.Context, names albresources.Names, headers pulumi.StringArray, assets albresources.AssetsBucket, project pulumi.String) (*compute.BackendBucket, error) {
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
		Role:    pulumi.String(albresources.ObjectViewerRole),
		Member:  pulumi.String(assets.Agent),
		Condition: &projects.IAMMemberConditionArgs{
			Title:       pulumi.String(names.AssetsReader),
			Description: pulumi.String("lists the tier's edge bucket and reads the static files in it"),
			Expression:  pulumi.String(gcloud.ListedKindCondition(names.AssetsReader, assets.Name, naming.ObjectAssets).Expression),
		},
	}, pulumi.DependsOn([]pulumi.Resource{bucket}))
	return bucket, err
}
