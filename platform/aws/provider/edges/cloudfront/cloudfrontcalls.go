package cloudfront

import (
	"context"

	"github.com/aws/aws-sdk-go-v2/service/cloudfront"
)

type cloudFrontCalls struct {
	describeKeyValueStore      func(context.Context, *cloudfront.DescribeKeyValueStoreInput, ...func(*cloudfront.Options)) (*cloudfront.DescribeKeyValueStoreOutput, error)
	listDistributions          func(context.Context, *cloudfront.ListDistributionsInput, ...func(*cloudfront.Options)) (*cloudfront.ListDistributionsOutput, error)
	createDistributionWithTags func(context.Context, *cloudfront.CreateDistributionWithTagsInput, ...func(*cloudfront.Options)) (*cloudfront.CreateDistributionWithTagsOutput, error)
	getDistribution            func(context.Context, *cloudfront.GetDistributionInput, ...func(*cloudfront.Options)) (*cloudfront.GetDistributionOutput, error)
	getDistributionConfig      func(context.Context, *cloudfront.GetDistributionConfigInput, ...func(*cloudfront.Options)) (*cloudfront.GetDistributionConfigOutput, error)
	updateDistribution         func(context.Context, *cloudfront.UpdateDistributionInput, ...func(*cloudfront.Options)) (*cloudfront.UpdateDistributionOutput, error)
	deleteDistribution         func(context.Context, *cloudfront.DeleteDistributionInput, ...func(*cloudfront.Options)) (*cloudfront.DeleteDistributionOutput, error)
}

func newCloudFrontCalls(client *cloudfront.Client) cloudFrontCalls {
	return cloudFrontCalls{
		describeKeyValueStore:      client.DescribeKeyValueStore,
		listDistributions:          client.ListDistributions,
		createDistributionWithTags: client.CreateDistributionWithTags,
		getDistribution:            client.GetDistribution,
		getDistributionConfig:      client.GetDistributionConfig,
		updateDistribution:         client.UpdateDistribution,
		deleteDistribution:         client.DeleteDistribution,
	}
}

func (c cloudFrontCalls) DescribeKeyValueStore(ctx context.Context, in *cloudfront.DescribeKeyValueStoreInput, optFns ...func(*cloudfront.Options)) (*cloudfront.DescribeKeyValueStoreOutput, error) {
	return c.describeKeyValueStore(ctx, in, optFns...)
}

func (c cloudFrontCalls) ListDistributions(ctx context.Context, in *cloudfront.ListDistributionsInput, optFns ...func(*cloudfront.Options)) (*cloudfront.ListDistributionsOutput, error) {
	return c.listDistributions(ctx, in, optFns...)
}

func (c cloudFrontCalls) CreateDistributionWithTags(ctx context.Context, in *cloudfront.CreateDistributionWithTagsInput, optFns ...func(*cloudfront.Options)) (*cloudfront.CreateDistributionWithTagsOutput, error) {
	return c.createDistributionWithTags(ctx, in, optFns...)
}

func (c cloudFrontCalls) GetDistribution(ctx context.Context, in *cloudfront.GetDistributionInput, optFns ...func(*cloudfront.Options)) (*cloudfront.GetDistributionOutput, error) {
	return c.getDistribution(ctx, in, optFns...)
}

func (c cloudFrontCalls) GetDistributionConfig(ctx context.Context, in *cloudfront.GetDistributionConfigInput, optFns ...func(*cloudfront.Options)) (*cloudfront.GetDistributionConfigOutput, error) {
	return c.getDistributionConfig(ctx, in, optFns...)
}

func (c cloudFrontCalls) UpdateDistribution(ctx context.Context, in *cloudfront.UpdateDistributionInput, optFns ...func(*cloudfront.Options)) (*cloudfront.UpdateDistributionOutput, error) {
	return c.updateDistribution(ctx, in, optFns...)
}

func (c cloudFrontCalls) DeleteDistribution(ctx context.Context, in *cloudfront.DeleteDistributionInput, optFns ...func(*cloudfront.Options)) (*cloudfront.DeleteDistributionOutput, error) {
	return c.deleteDistribution(ctx, in, optFns...)
}
