package cloudfront

import (
	"context"
	"errors"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/cloudfront"
	cftypes "github.com/aws/aws-sdk-go-v2/service/cloudfront/types"

	"github.com/ocelhq/ocel/pkg/provider"
)

func Remove(ctx context.Context, c API, spec Spec, recorded []provider.PhysicalResource) error {
	names := NamesFor(spec.Namespace, spec.Tier)
	set := SetOf(names, recorded)
	for _, name := range []string{names.Resolver, names.EmptyBody} {
		if err := removeFunction(ctx, c, name); err != nil {
			return err
		}
	}
	if err := removeRoutesStore(ctx, c, names.RoutesStore); err != nil {
		return err
	}
	if set.CachePolicy != "" {
		current, err := c.GetCachePolicy(ctx, &cloudfront.GetCachePolicyInput{Id: aws.String(set.CachePolicy)})
		var missing *cftypes.NoSuchCachePolicy
		switch {
		case errors.As(err, &missing):
		case err != nil:
			return fmt.Errorf("read cache policy %s: %w", names.CachePolicy, err)
		default:
			if _, err := c.DeleteCachePolicy(ctx, &cloudfront.DeleteCachePolicyInput{Id: aws.String(set.CachePolicy), IfMatch: current.ETag}); err != nil && !errors.As(err, &missing) {
				return fmt.Errorf("delete cache policy %s: %w", names.CachePolicy, err)
			}
		}
	}
	if set.HeadersPolicy != "" {
		current, err := c.GetResponseHeadersPolicy(ctx, &cloudfront.GetResponseHeadersPolicyInput{Id: aws.String(set.HeadersPolicy)})
		var missing *cftypes.NoSuchResponseHeadersPolicy
		switch {
		case errors.As(err, &missing):
		case err != nil:
			return fmt.Errorf("read response headers policy %s: %w", names.HeadersPolicy, err)
		default:
			if _, err := c.DeleteResponseHeadersPolicy(ctx, &cloudfront.DeleteResponseHeadersPolicyInput{Id: aws.String(set.HeadersPolicy), IfMatch: current.ETag}); err != nil && !errors.As(err, &missing) {
				return fmt.Errorf("delete response headers policy %s: %w", names.HeadersPolicy, err)
			}
		}
	}
	if set.AssetAccess != "" {
		current, err := c.GetOriginAccessControl(ctx, &cloudfront.GetOriginAccessControlInput{Id: aws.String(set.AssetAccess)})
		var missing *cftypes.NoSuchOriginAccessControl
		switch {
		case errors.As(err, &missing):
		case err != nil:
			return fmt.Errorf("read origin access control %s: %w", names.AssetAccess, err)
		default:
			if _, err := c.DeleteOriginAccessControl(ctx, &cloudfront.DeleteOriginAccessControlInput{Id: aws.String(set.AssetAccess), IfMatch: current.ETag}); err != nil && !errors.As(err, &missing) {
				return fmt.Errorf("delete origin access control %s: %w", names.AssetAccess, err)
			}
		}
	}
	return nil
}

func removeFunction(ctx context.Context, c API, name string) error {
	described, err := c.DescribeFunction(ctx, &cloudfront.DescribeFunctionInput{Name: aws.String(name), Stage: cftypes.FunctionStageDevelopment})
	var missing *cftypes.NoSuchFunctionExists
	if errors.As(err, &missing) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read CloudFront function %s: %w", name, err)
	}
	if _, err := c.DeleteFunction(ctx, &cloudfront.DeleteFunctionInput{Name: aws.String(name), IfMatch: described.ETag}); err != nil && !errors.As(err, &missing) {
		return fmt.Errorf("delete CloudFront function %s: %w", name, err)
	}
	return nil
}

func removeRoutesStore(ctx context.Context, c API, name string) error {
	described, err := c.DescribeKeyValueStore(ctx, &cloudfront.DescribeKeyValueStoreInput{Name: aws.String(name)})
	var missing *cftypes.EntityNotFound
	if errors.As(err, &missing) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read key value store %s: %w", name, err)
	}
	if _, err := c.DeleteKeyValueStore(ctx, &cloudfront.DeleteKeyValueStoreInput{Name: aws.String(name), IfMatch: described.ETag}); err != nil && !errors.As(err, &missing) {
		return fmt.Errorf("delete key value store %s: %w", name, err)
	}
	return nil
}
