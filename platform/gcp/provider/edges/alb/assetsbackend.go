package alb

import (
	"fmt"
	"regexp"

	"github.com/pulumi/pulumi-gcp/sdk/v9/go/gcp/compute"
	"github.com/pulumi/pulumi-gcp/sdk/v9/go/gcp/projects"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"

	"github.com/ocelhq/ocel/pkg/provider"
)

const (
	objectViewerRole    = "roles/storage.objectViewer"
	releaseAssetsObject = `^projects/_/buckets/%s/objects/%s/[^/]+/[^/]+/[^/]+/[^/]+/assets/.+$`
)

type AssetsBucket struct {
	Name  string
	Agent string
}

func declareAssetsBackend(ctx *pulumi.Context, spec loadBalancerSpec, project pulumi.String) (*compute.BackendBucket, error) {
	bucket, err := compute.NewBackendBucket(ctx, spec.Names.AssetsBackend, &compute.BackendBucketArgs{
		Name:       pulumi.String(spec.Names.AssetsBackend),
		Project:    project,
		BucketName: pulumi.String(spec.Assets.Name),
		EnableCdn:  pulumi.Bool(true),
		CdnPolicy: &compute.BackendBucketCdnPolicyArgs{
			CacheMode:       pulumi.String(cacheOnOrigin),
			NegativeCaching: pulumi.Bool(false),
		},
	})
	if err != nil {
		return nil, err
	}
	_, err = projects.NewIAMMember(ctx, spec.Names.AssetsReader, &projects.IAMMemberArgs{
		Project: project,
		Role:    pulumi.String(objectViewerRole),
		Member:  pulumi.String(spec.Assets.Agent),
		Condition: &projects.IAMMemberConditionArgs{
			Title:       pulumi.String(spec.Names.AssetsReader),
			Description: pulumi.String("the static files of releases in the tier's bucket, and nothing else in it"),
			Expression: pulumi.String(fmt.Sprintf(`resource.name.matches(%q)`,
				fmt.Sprintf(releaseAssetsObject, regexp.QuoteMeta(spec.Assets.Name), provider.StoreAssets))),
		},
	}, pulumi.DependsOn([]pulumi.Resource{bucket}))
	return bucket, err
}
