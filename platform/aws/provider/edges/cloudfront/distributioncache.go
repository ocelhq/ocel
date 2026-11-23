package cloudfront

import (
	"context"
	"sync"

	"github.com/aws/aws-sdk-go-v2/service/cloudfront"
)

type distributionCache struct {
	CloudFrontAPI

	mu        sync.Mutex
	summaries []distributionSummary
	listed    bool
}

func (d *distributionCache) listDistributions(ctx context.Context) ([]distributionSummary, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.listed {
		return d.summaries, nil
	}
	summaries, err := listDistributions(ctx, Clients{CloudFront: d.CloudFrontAPI})
	if err != nil {
		return nil, err
	}
	d.summaries, d.listed = summaries, true
	return summaries, nil
}

func (d *distributionCache) forget() {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.summaries, d.listed = nil, false
}

func (d *distributionCache) CreateDistributionWithTags(ctx context.Context, in *cloudfront.CreateDistributionWithTagsInput, optFns ...func(*cloudfront.Options)) (*cloudfront.CreateDistributionWithTagsOutput, error) {
	defer d.forget()
	return d.CloudFrontAPI.CreateDistributionWithTags(ctx, in, optFns...)
}

func (d *distributionCache) UpdateDistribution(ctx context.Context, in *cloudfront.UpdateDistributionInput, optFns ...func(*cloudfront.Options)) (*cloudfront.UpdateDistributionOutput, error) {
	defer d.forget()
	return d.CloudFrontAPI.UpdateDistribution(ctx, in, optFns...)
}

func (d *distributionCache) DeleteDistribution(ctx context.Context, in *cloudfront.DeleteDistributionInput, optFns ...func(*cloudfront.Options)) (*cloudfront.DeleteDistributionOutput, error) {
	defer d.forget()
	return d.CloudFrontAPI.DeleteDistribution(ctx, in, optFns...)
}
