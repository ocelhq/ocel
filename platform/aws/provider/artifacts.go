package aws

import (
	"context"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/platform/aws/provider/bootstrap"
	awsports "github.com/ocelhq/ocel/platform/aws/provider/ports"
	cloudflare "github.com/ocelhq/ocel/platform/edge/cloudflare/deploy"
)

func (p *Provider) Buckets(ctx context.Context, tier environment.Tier) (awsports.Buckets, error) {
	deployed, err := p.bootstrapped(ctx, tier)
	if err != nil {
		return awsports.Buckets{}, err
	}
	buckets := awsports.Buckets{Functions: deployed.ArtifactBucket, Assets: deployed.AssetBucket}
	for _, kind := range bootstrap.EdgeKindsFor(deployed.Features.Names()) {
		params, err := p.tierParams(ctx, tier, kind)
		if err != nil {
			return buckets, err
		}
		if params.CacheStore.Bucket != "" {
			cache := awsports.CacheBucket{
				Name: params.CacheStore.Bucket,
				S3:   cacheStoreClient(params.CacheStore),
			}
			if writer := params.ISRWriter; writer.Endpoint != "" && writer.BootstrapCredential != "" {
				cache.Writer = cloudflare.ISRWriter{Endpoint: writer.Endpoint, BootstrapCredential: writer.BootstrapCredential}
			}
			buckets.Caches = append(buckets.Caches, cache)
		}
	}
	return buckets, nil
}

func cacheStoreClient(store bootstrap.CacheStore) awsports.S3API {
	if store.Endpoint == "" {
		return nil
	}
	return s3.New(s3.Options{
		Region:       store.Region,
		BaseEndpoint: aws.String(store.Endpoint),
		UsePathStyle: true,
		Credentials:  credentials.NewStaticCredentialsProvider(store.AccessKeyID, store.SecretAccessKey, ""),
	})
}
