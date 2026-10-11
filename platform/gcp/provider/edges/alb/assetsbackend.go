package alb

import (
	"fmt"

	"github.com/pulumi/pulumi-gcp/sdk/v9/go/gcp/compute"
	"github.com/pulumi/pulumi-gcp/sdk/v9/go/gcp/projects"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"

	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/provider"
)

const objectViewerRole = "roles/storage.objectViewer"

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
			Description: pulumi.String("lists the tier's bucket and reads the static files of its releases, and nothing else in it"),
			Expression:  pulumi.String(releaseFilesCondition(spec.Assets.Name)),
		},
	}, pulumi.DependsOn([]pulumi.Resource{bucket}))
	return bucket, err
}

func releaseFilesCondition(bucket string) string {
	name := "projects/_/buckets/" + bucket
	objects := name + "/objects/" + provider.StoreAssets + "/"
	return fmt.Sprintf(`resource.name == %q || (resource.name.startsWith(%q) && resource.name.extract(%q) != "")`,
		name, objects, objects+"{release}/"+naming.AssetsSegment+"/")
}
