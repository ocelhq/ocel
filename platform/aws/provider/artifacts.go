package aws

import (
	"context"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"

	"github.com/ocelhq/ocel/platform/aws/provider/bootstrap"
	awsports "github.com/ocelhq/ocel/platform/aws/provider/ports"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

func (p *Provider) Buckets(ctx context.Context, class edge.Class) (awsports.Buckets, error) {
	deployed, err := p.bootstrapped(ctx, class)
	if err != nil {
		return awsports.Buckets{}, err
	}
	buckets := awsports.Buckets{Functions: deployed.ArtifactBucket, Assets: deployed.AssetBucket}
	for _, kind := range bootstrap.EdgeKindsFor(deployed.Features.Names()) {
		params, err := p.classParams(ctx, class, kind)
		if err != nil {
			return buckets, err
		}
		if params.CacheStore.Bucket != "" {
			buckets.Caches = append(buckets.Caches, awsports.CacheBucket{
				Name: params.CacheStore.Bucket,
				S3:   cacheStoreClient(params.CacheStore),
			})
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
