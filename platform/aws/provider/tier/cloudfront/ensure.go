package cloudfront

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/cloudfront"
	cftypes "github.com/aws/aws-sdk-go-v2/service/cloudfront/types"
)

type API interface {
	CreateKeyValueStore(ctx context.Context, in *cloudfront.CreateKeyValueStoreInput, optFns ...func(*cloudfront.Options)) (*cloudfront.CreateKeyValueStoreOutput, error)
	DescribeKeyValueStore(ctx context.Context, in *cloudfront.DescribeKeyValueStoreInput, optFns ...func(*cloudfront.Options)) (*cloudfront.DescribeKeyValueStoreOutput, error)
	DeleteKeyValueStore(ctx context.Context, in *cloudfront.DeleteKeyValueStoreInput, optFns ...func(*cloudfront.Options)) (*cloudfront.DeleteKeyValueStoreOutput, error)

	CreateFunction(ctx context.Context, in *cloudfront.CreateFunctionInput, optFns ...func(*cloudfront.Options)) (*cloudfront.CreateFunctionOutput, error)
	DescribeFunction(ctx context.Context, in *cloudfront.DescribeFunctionInput, optFns ...func(*cloudfront.Options)) (*cloudfront.DescribeFunctionOutput, error)
	UpdateFunction(ctx context.Context, in *cloudfront.UpdateFunctionInput, optFns ...func(*cloudfront.Options)) (*cloudfront.UpdateFunctionOutput, error)
	PublishFunction(ctx context.Context, in *cloudfront.PublishFunctionInput, optFns ...func(*cloudfront.Options)) (*cloudfront.PublishFunctionOutput, error)
	DeleteFunction(ctx context.Context, in *cloudfront.DeleteFunctionInput, optFns ...func(*cloudfront.Options)) (*cloudfront.DeleteFunctionOutput, error)

	CreateCachePolicy(ctx context.Context, in *cloudfront.CreateCachePolicyInput, optFns ...func(*cloudfront.Options)) (*cloudfront.CreateCachePolicyOutput, error)
	GetCachePolicy(ctx context.Context, in *cloudfront.GetCachePolicyInput, optFns ...func(*cloudfront.Options)) (*cloudfront.GetCachePolicyOutput, error)
	UpdateCachePolicy(ctx context.Context, in *cloudfront.UpdateCachePolicyInput, optFns ...func(*cloudfront.Options)) (*cloudfront.UpdateCachePolicyOutput, error)
	ListCachePolicies(ctx context.Context, in *cloudfront.ListCachePoliciesInput, optFns ...func(*cloudfront.Options)) (*cloudfront.ListCachePoliciesOutput, error)
	DeleteCachePolicy(ctx context.Context, in *cloudfront.DeleteCachePolicyInput, optFns ...func(*cloudfront.Options)) (*cloudfront.DeleteCachePolicyOutput, error)

	CreateResponseHeadersPolicy(ctx context.Context, in *cloudfront.CreateResponseHeadersPolicyInput, optFns ...func(*cloudfront.Options)) (*cloudfront.CreateResponseHeadersPolicyOutput, error)
	GetResponseHeadersPolicy(ctx context.Context, in *cloudfront.GetResponseHeadersPolicyInput, optFns ...func(*cloudfront.Options)) (*cloudfront.GetResponseHeadersPolicyOutput, error)
	UpdateResponseHeadersPolicy(ctx context.Context, in *cloudfront.UpdateResponseHeadersPolicyInput, optFns ...func(*cloudfront.Options)) (*cloudfront.UpdateResponseHeadersPolicyOutput, error)
	ListResponseHeadersPolicies(ctx context.Context, in *cloudfront.ListResponseHeadersPoliciesInput, optFns ...func(*cloudfront.Options)) (*cloudfront.ListResponseHeadersPoliciesOutput, error)
	DeleteResponseHeadersPolicy(ctx context.Context, in *cloudfront.DeleteResponseHeadersPolicyInput, optFns ...func(*cloudfront.Options)) (*cloudfront.DeleteResponseHeadersPolicyOutput, error)

	CreateOriginAccessControl(ctx context.Context, in *cloudfront.CreateOriginAccessControlInput, optFns ...func(*cloudfront.Options)) (*cloudfront.CreateOriginAccessControlOutput, error)
	GetOriginAccessControl(ctx context.Context, in *cloudfront.GetOriginAccessControlInput, optFns ...func(*cloudfront.Options)) (*cloudfront.GetOriginAccessControlOutput, error)
	UpdateOriginAccessControl(ctx context.Context, in *cloudfront.UpdateOriginAccessControlInput, optFns ...func(*cloudfront.Options)) (*cloudfront.UpdateOriginAccessControlOutput, error)
	ListOriginAccessControls(ctx context.Context, in *cloudfront.ListOriginAccessControlsInput, optFns ...func(*cloudfront.Options)) (*cloudfront.ListOriginAccessControlsOutput, error)
	DeleteOriginAccessControl(ctx context.Context, in *cloudfront.DeleteOriginAccessControlInput, optFns ...func(*cloudfront.Options)) (*cloudfront.DeleteOriginAccessControlOutput, error)
}

const listPageCeiling = 20

const (
	routesStoreReadyPoll   = time.Second
	routesStoreReadyWithin = 2 * time.Minute

	keyValueStoreReady        = "READY"
	keyValueStoreProvisioning = "PROVISIONING"
)

func ensureRoutesStore(ctx context.Context, c API, name string) (string, error) {
	_, err := c.CreateKeyValueStore(ctx, &cloudfront.CreateKeyValueStoreInput{
		Name:    aws.String(name),
		Comment: aws.String("Ocel: one entry per hostname naming the release that answers on it. Written at promote, read by the resolver on every request."),
	})
	var exists *cftypes.EntityAlreadyExists
	if err != nil && !errors.As(err, &exists) {
		return "", fmt.Errorf("create key value store %s: %w", name, err)
	}
	return routesStoreOnceReady(ctx, c, name)
}

func routesStoreOnceReady(ctx context.Context, c API, name string) (string, error) {
	deadline := time.Now().Add(routesStoreReadyWithin)
	for {
		described, err := c.DescribeKeyValueStore(ctx, &cloudfront.DescribeKeyValueStoreInput{Name: aws.String(name)})
		if err != nil {
			return "", fmt.Errorf("read key value store %s: %w", name, err)
		}
		switch status := aws.ToString(described.KeyValueStore.Status); status {
		case keyValueStoreReady:
			return aws.ToString(described.KeyValueStore.ARN), nil
		case keyValueStoreProvisioning:
		default:
			return "", fmt.Errorf("key value store %s is %s, not %s", name, status, keyValueStoreReady)
		}
		if time.Now().After(deadline) {
			return "", fmt.Errorf("key value store %s was still provisioning after %s", name, routesStoreReadyWithin)
		}
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(routesStoreReadyPoll):
		}
	}
}

func ensureFunction(ctx context.Context, c API, name string, config *cftypes.FunctionConfig, code []byte) (string, error) {
	described, err := c.DescribeFunction(ctx, &cloudfront.DescribeFunctionInput{Name: aws.String(name), Stage: cftypes.FunctionStageDevelopment})
	var etag *string
	var missing *cftypes.NoSuchFunctionExists
	switch {
	case errors.As(err, &missing):
		created, err := c.CreateFunction(ctx, &cloudfront.CreateFunctionInput{Name: aws.String(name), FunctionConfig: config, FunctionCode: code})
		if err != nil {
			return "", fmt.Errorf("create CloudFront function %s: %w", name, err)
		}
		etag = created.ETag
	case err != nil:
		return "", fmt.Errorf("read CloudFront function %s: %w", name, err)
	default:
		updated, err := c.UpdateFunction(ctx, &cloudfront.UpdateFunctionInput{Name: aws.String(name), IfMatch: described.ETag, FunctionConfig: config, FunctionCode: code})
		if err != nil {
			return "", fmt.Errorf("update CloudFront function %s: %w", name, err)
		}
		etag = updated.ETag
	}
	published, err := c.PublishFunction(ctx, &cloudfront.PublishFunctionInput{Name: aws.String(name), IfMatch: etag})
	if err != nil {
		return "", fmt.Errorf("publish CloudFront function %s: %w", name, err)
	}
	return aws.ToString(published.FunctionSummary.FunctionMetadata.FunctionARN), nil
}

func ensureCachePolicy(ctx context.Context, c API, recorded string, config *cftypes.CachePolicyConfig) (string, error) {
	name := aws.ToString(config.Name)
	id := recorded
	if id == "" {
		created, err := c.CreateCachePolicy(ctx, &cloudfront.CreateCachePolicyInput{CachePolicyConfig: config})
		if err == nil {
			return aws.ToString(created.CachePolicy.Id), nil
		}
		var exists *cftypes.CachePolicyAlreadyExists
		if !errors.As(err, &exists) {
			return "", fmt.Errorf("create cache policy %s: %w", name, err)
		}
		if id, err = cachePolicyNamed(ctx, c, name); err != nil {
			return "", err
		}
	}
	current, err := c.GetCachePolicy(ctx, &cloudfront.GetCachePolicyInput{Id: aws.String(id)})
	var missing *cftypes.NoSuchCachePolicy
	if errors.As(err, &missing) && recorded != "" {
		return ensureCachePolicy(ctx, c, "", config)
	}
	if err != nil {
		return "", fmt.Errorf("read cache policy %s: %w", name, err)
	}
	if _, err := c.UpdateCachePolicy(ctx, &cloudfront.UpdateCachePolicyInput{Id: aws.String(id), IfMatch: current.ETag, CachePolicyConfig: config}); err != nil {
		return "", fmt.Errorf("update cache policy %s: %w", name, err)
	}
	return id, nil
}

func cachePolicyNamed(ctx context.Context, c API, name string) (string, error) {
	var marker *string
	for range listPageCeiling {
		page, err := c.ListCachePolicies(ctx, &cloudfront.ListCachePoliciesInput{Type: cftypes.CachePolicyTypeCustom, Marker: marker})
		if err != nil {
			return "", fmt.Errorf("find cache policy %s: %w", name, err)
		}
		for _, item := range page.CachePolicyList.Items {
			if item.CachePolicy != nil && aws.ToString(item.CachePolicy.CachePolicyConfig.Name) == name {
				return aws.ToString(item.CachePolicy.Id), nil
			}
		}
		if marker = page.CachePolicyList.NextMarker; marker == nil {
			break
		}
	}
	return "", fmt.Errorf("cache policy %s exists, CloudFront says, but no custom cache policy of this account bears the name", name)
}

func ensureHeadersPolicy(ctx context.Context, c API, recorded string, config *cftypes.ResponseHeadersPolicyConfig) (string, error) {
	name := aws.ToString(config.Name)
	id := recorded
	if id == "" {
		created, err := c.CreateResponseHeadersPolicy(ctx, &cloudfront.CreateResponseHeadersPolicyInput{ResponseHeadersPolicyConfig: config})
		if err == nil {
			return aws.ToString(created.ResponseHeadersPolicy.Id), nil
		}
		var exists *cftypes.ResponseHeadersPolicyAlreadyExists
		if !errors.As(err, &exists) {
			return "", fmt.Errorf("create response headers policy %s: %w", name, err)
		}
		if id, err = headersPolicyNamed(ctx, c, name); err != nil {
			return "", err
		}
	}
	current, err := c.GetResponseHeadersPolicy(ctx, &cloudfront.GetResponseHeadersPolicyInput{Id: aws.String(id)})
	var missing *cftypes.NoSuchResponseHeadersPolicy
	if errors.As(err, &missing) && recorded != "" {
		return ensureHeadersPolicy(ctx, c, "", config)
	}
	if err != nil {
		return "", fmt.Errorf("read response headers policy %s: %w", name, err)
	}
	if _, err := c.UpdateResponseHeadersPolicy(ctx, &cloudfront.UpdateResponseHeadersPolicyInput{Id: aws.String(id), IfMatch: current.ETag, ResponseHeadersPolicyConfig: config}); err != nil {
		return "", fmt.Errorf("update response headers policy %s: %w", name, err)
	}
	return id, nil
}

func headersPolicyNamed(ctx context.Context, c API, name string) (string, error) {
	var marker *string
	for range listPageCeiling {
		page, err := c.ListResponseHeadersPolicies(ctx, &cloudfront.ListResponseHeadersPoliciesInput{Type: cftypes.ResponseHeadersPolicyTypeCustom, Marker: marker})
		if err != nil {
			return "", fmt.Errorf("find response headers policy %s: %w", name, err)
		}
		for _, item := range page.ResponseHeadersPolicyList.Items {
			if item.ResponseHeadersPolicy != nil && aws.ToString(item.ResponseHeadersPolicy.ResponseHeadersPolicyConfig.Name) == name {
				return aws.ToString(item.ResponseHeadersPolicy.Id), nil
			}
		}
		if marker = page.ResponseHeadersPolicyList.NextMarker; marker == nil {
			break
		}
	}
	return "", fmt.Errorf("response headers policy %s exists, CloudFront says, but no custom response headers policy of this account bears the name", name)
}

func ensureAssetAccess(ctx context.Context, c API, recorded string, config *cftypes.OriginAccessControlConfig) (string, error) {
	name := aws.ToString(config.Name)
	id := recorded
	if id == "" {
		created, err := c.CreateOriginAccessControl(ctx, &cloudfront.CreateOriginAccessControlInput{OriginAccessControlConfig: config})
		if err == nil {
			return aws.ToString(created.OriginAccessControl.Id), nil
		}
		var exists *cftypes.OriginAccessControlAlreadyExists
		if !errors.As(err, &exists) {
			return "", fmt.Errorf("create origin access control %s: %w", name, err)
		}
		if id, err = assetAccessNamed(ctx, c, name); err != nil {
			return "", err
		}
	}
	current, err := c.GetOriginAccessControl(ctx, &cloudfront.GetOriginAccessControlInput{Id: aws.String(id)})
	var missing *cftypes.NoSuchOriginAccessControl
	if errors.As(err, &missing) && recorded != "" {
		return ensureAssetAccess(ctx, c, "", config)
	}
	if err != nil {
		return "", fmt.Errorf("read origin access control %s: %w", name, err)
	}
	if _, err := c.UpdateOriginAccessControl(ctx, &cloudfront.UpdateOriginAccessControlInput{Id: aws.String(id), IfMatch: current.ETag, OriginAccessControlConfig: config}); err != nil {
		return "", fmt.Errorf("update origin access control %s: %w", name, err)
	}
	return id, nil
}

func assetAccessNamed(ctx context.Context, c API, name string) (string, error) {
	var marker *string
	for range listPageCeiling {
		page, err := c.ListOriginAccessControls(ctx, &cloudfront.ListOriginAccessControlsInput{Marker: marker})
		if err != nil {
			return "", fmt.Errorf("find origin access control %s: %w", name, err)
		}
		for _, item := range page.OriginAccessControlList.Items {
			if aws.ToString(item.Name) == name {
				return aws.ToString(item.Id), nil
			}
		}
		if marker = page.OriginAccessControlList.NextMarker; marker == nil {
			break
		}
	}
	return "", fmt.Errorf("origin access control %s exists, CloudFront says, but no origin access control of this account bears the name", name)
}
