package alb

import (
	"context"
	"fmt"
	"strings"

	"github.com/pulumi/pulumi-gcp/sdk/v9/go/gcp/compute"
	"github.com/pulumi/pulumi-gcp/sdk/v9/go/gcp/projects"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"

	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/progress"
	"github.com/ocelhq/ocel/pkg/provider"
)

const objectViewerRole = "roles/storage.objectViewer"

const agentAppearsAttempts = 20

type AssetsBucket struct {
	Name  string
	Agent string
}

func declareAssetsBackend(ctx *pulumi.Context, names names, assets AssetsBucket, project pulumi.String) (*compute.BackendBucket, error) {
	bucket, err := compute.NewBackendBucket(ctx, names.AssetsBackend, &compute.BackendBucketArgs{
		Name:       pulumi.String(names.AssetsBackend),
		Project:    project,
		BucketName: pulumi.String(assets.Name),
		EnableCdn:  pulumi.Bool(true),
		CdnPolicy: &compute.BackendBucketCdnPolicyArgs{
			CacheMode:       pulumi.String(cacheOnOrigin),
			NegativeCaching: pulumi.Bool(false),
		},
	})
	if err != nil {
		return nil, err
	}
	_, err = projects.NewIAMMember(ctx, names.AssetsReader, &projects.IAMMemberArgs{
		Project: project,
		Role:    pulumi.String(objectViewerRole),
		Member:  pulumi.String(assets.Agent),
		Condition: &projects.IAMMemberConditionArgs{
			Title:       pulumi.String(names.AssetsReader),
			Description: pulumi.String("lists the tier's bucket and reads the static files of its releases, and nothing else in it"),
			Expression:  pulumi.String(releaseFilesCondition(assets.Name)),
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

func (e *Edge) raiseFront(ctx context.Context, spec loadBalancerSpec, progress progress.Log) (map[string]string, error) {
	for attempt := 1; ; attempt++ {
		outputs, err := e.deps.Stacks.Up(ctx, e.loadBalancerTarget(spec.Tier), loadBalancerProgram(spec), progress)
		if err == nil || !isUnseenAgent(err, spec.Assets) || attempt == agentAppearsAttempts {
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
