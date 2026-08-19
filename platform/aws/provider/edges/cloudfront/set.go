package cloudfront

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/cloudfront"
	cftypes "github.com/aws/aws-sdk-go-v2/service/cloudfront/types"

	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

const (
	cacheMaxTTL = int64(365 * 24 * time.Hour / time.Second)

	rscQueryParameter = "_rsc"

	listPageCeiling = 200
)

type edgeSet struct {
	keyValueStoreARN    string
	functionARN         string
	cachePolicy         string
	headersPolicy       string
	originAccessControl string
}

func ensureEdgeSet(ctx context.Context, c Clients, class edge.Class) (edgeSet, error) {
	kvsARN, err := ensureKeyValueStore(ctx, c, class)
	if err != nil {
		return edgeSet{}, err
	}
	functionARN, err := ensureResolver(ctx, c, class, kvsARN)
	if err != nil {
		return edgeSet{}, err
	}
	cachePolicy, err := ensureCachePolicy(ctx, c, class)
	if err != nil {
		return edgeSet{}, err
	}
	headersPolicy, err := ensureHeadersPolicy(ctx, c, class)
	if err != nil {
		return edgeSet{}, err
	}
	oac, err := ensureOriginAccessControl(ctx, c, class)
	if err != nil {
		return edgeSet{}, err
	}
	return edgeSet{
		keyValueStoreARN:    kvsARN,
		functionARN:         functionARN,
		cachePolicy:         cachePolicy,
		headersPolicy:       headersPolicy,
		originAccessControl: oac,
	}, nil
}

func findEdgeSet(ctx context.Context, c Clients, class edge.Class, held edgeSet) (edgeSet, error) {
	set := edgeSet{
		cachePolicy:         held.cachePolicy,
		headersPolicy:       held.headersPolicy,
		originAccessControl: held.originAccessControl,
	}

	store, err := c.CloudFront.DescribeKeyValueStore(ctx, &cloudfront.DescribeKeyValueStoreInput{
		Name: aws.String(keyValueStoreName(class)),
	})
	if err != nil {
		if isNotFound(err) {
			return edgeSet{}, unbootstrapped(class)
		}
		return edgeSet{}, fmt.Errorf("read the key value store the %q edge routes with: %w", Kind, err)
	}
	set.keyValueStoreARN = aws.ToString(store.KeyValueStore.ARN)

	described, err := c.CloudFront.DescribeFunction(ctx, &cloudfront.DescribeFunctionInput{
		Name:  aws.String(functionName(class)),
		Stage: cftypes.FunctionStageLive,
	})
	if err != nil {
		if isNotFound(err) {
			return edgeSet{}, unbootstrapped(class)
		}
		return edgeSet{}, fmt.Errorf("read the resolver function the %q edge routes with: %w", Kind, err)
	}
	set.functionARN = aws.ToString(described.FunctionSummary.FunctionMetadata.FunctionARN)

	if set.cachePolicy == "" {
		if set.cachePolicy, err = findCachePolicy(ctx, c, cachePolicyName(class)); err != nil {
			return edgeSet{}, err
		}
	}
	if set.headersPolicy == "" {
		if set.headersPolicy, err = findHeadersPolicy(ctx, c, headersPolicyName(class)); err != nil {
			return edgeSet{}, err
		}
	}
	if set.originAccessControl == "" {
		if set.originAccessControl, err = findOriginAccessControl(ctx, c, originAccessControlName(class)); err != nil {
			return edgeSet{}, err
		}
	}
	if set.cachePolicy == "" || set.headersPolicy == "" || set.originAccessControl == "" {
		return edgeSet{}, unbootstrapped(class)
	}
	return set, nil
}

func unbootstrapped(class edge.Class) error {
	return fmt.Errorf("the %q edge has nothing to front %s deployments with in this account: its CloudFront function, key value store and cache policies are missing. Run `ocel bootstrap` against this account, then deploy again", Kind, class)
}

func ensureKeyValueStore(ctx context.Context, c Clients, class edge.Class) (string, error) {
	name := keyValueStoreName(class)
	out, err := c.CloudFront.DescribeKeyValueStore(ctx, &cloudfront.DescribeKeyValueStoreInput{Name: aws.String(name)})
	if err == nil {
		return aws.ToString(out.KeyValueStore.ARN), nil
	}
	if !isNotFound(err) {
		return "", fmt.Errorf("read the key value store %q: %w", name, err)
	}
	created, err := c.CloudFront.CreateKeyValueStore(ctx, &cloudfront.CreateKeyValueStoreInput{
		Name:    aws.String(name),
		Comment: aws.String("Ocel: one entry per hostname naming the release that answers on it. Written at promote, read by the resolver function on every request."),
	})
	if err != nil {
		return "", createError("key value store", name, err)
	}
	return aws.ToString(created.KeyValueStore.ARN), nil
}

func ensureResolver(ctx context.Context, c Clients, class edge.Class, kvsARN string) (string, error) {
	name := functionName(class)
	config := &cftypes.FunctionConfig{
		Comment: aws.String("Ocel: reads the hostname's release out of the key value store and points the request at the release's assets or its entry function."),
		Runtime: cftypes.FunctionRuntimeCloudfrontJs20,
		KeyValueStoreAssociations: &cftypes.KeyValueStoreAssociations{
			Quantity: ptr(int32(1)),
			Items:    []cftypes.KeyValueStoreAssociation{{KeyValueStoreARN: aws.String(kvsARN)}},
		},
	}

	held, err := c.CloudFront.GetFunction(ctx, &cloudfront.GetFunctionInput{
		Name:  aws.String(name),
		Stage: cftypes.FunctionStageDevelopment,
	})
	switch {
	case err != nil && !isNotFound(err):
		return "", fmt.Errorf("read the resolver function %q: %w", name, err)
	case err != nil:
		created, err := c.CloudFront.CreateFunction(ctx, &cloudfront.CreateFunctionInput{
			Name:           aws.String(name),
			FunctionCode:   ResolverCode(),
			FunctionConfig: config,
		})
		if err != nil {
			return "", createError("function", name, err)
		}
		return publishResolver(ctx, c, name, aws.ToString(created.ETag))
	case bytes.Equal(held.FunctionCode, ResolverCode()):
		described, err := c.CloudFront.DescribeFunction(ctx, &cloudfront.DescribeFunctionInput{
			Name:  aws.String(name),
			Stage: cftypes.FunctionStageLive,
		})
		if err == nil {
			return aws.ToString(described.FunctionSummary.FunctionMetadata.FunctionARN), nil
		}
		if !isNotFound(err) {
			return "", fmt.Errorf("read the published resolver function %q: %w", name, err)
		}
		return publishResolver(ctx, c, name, aws.ToString(held.ETag))
	}

	updated, err := c.CloudFront.UpdateFunction(ctx, &cloudfront.UpdateFunctionInput{
		Name:           aws.String(name),
		IfMatch:        held.ETag,
		FunctionCode:   ResolverCode(),
		FunctionConfig: config,
	})
	if err != nil {
		return "", fmt.Errorf("update the resolver function %q: %w", name, err)
	}
	return publishResolver(ctx, c, name, aws.ToString(updated.ETag))
}

func publishResolver(ctx context.Context, c Clients, name, etag string) (string, error) {
	out, err := c.CloudFront.PublishFunction(ctx, &cloudfront.PublishFunctionInput{
		Name:    aws.String(name),
		IfMatch: aws.String(etag),
	})
	if err != nil {
		return "", fmt.Errorf("publish the resolver function %q: %w", name, err)
	}
	return aws.ToString(out.FunctionSummary.FunctionMetadata.FunctionARN), nil
}

func ensureCachePolicy(ctx context.Context, c Clients, class edge.Class) (string, error) {
	name := cachePolicyName(class)
	id, err := findCachePolicy(ctx, c, name)
	if err != nil || id != "" {
		return id, err
	}
	out, err := c.CloudFront.CreateCachePolicy(ctx, &cloudfront.CreateCachePolicyInput{
		CachePolicyConfig: &cftypes.CachePolicyConfig{
			Name:       aws.String(name),
			Comment:    aws.String("Ocel: keys the cache on the hostname and the variant the resolver function computed, and lets the origin's Cache-Control govern how long anything is held."),
			MinTTL:     ptr(int64(0)),
			DefaultTTL: ptr(int64(0)),
			MaxTTL:     ptr(cacheMaxTTL),
			ParametersInCacheKeyAndForwardedToOrigin: &cftypes.ParametersInCacheKeyAndForwardedToOrigin{
				EnableAcceptEncodingGzip:   ptr(true),
				EnableAcceptEncodingBrotli: ptr(true),
				HeadersConfig: &cftypes.CachePolicyHeadersConfig{
					HeaderBehavior: cftypes.CachePolicyHeaderBehaviorWhitelist,
					Headers: &cftypes.Headers{
						Quantity: ptr(int32(2)),
						Items:    []string{"Host", cacheKeyHeader},
					},
				},
				CookiesConfig: &cftypes.CachePolicyCookiesConfig{
					CookieBehavior: cftypes.CachePolicyCookieBehaviorNone,
				},
				QueryStringsConfig: &cftypes.CachePolicyQueryStringsConfig{
					QueryStringBehavior: cftypes.CachePolicyQueryStringBehaviorAllExcept,
					QueryStrings: &cftypes.QueryStringNames{
						Quantity: ptr(int32(1)),
						Items:    []string{rscQueryParameter},
					},
				},
			},
		},
	})
	if err != nil {
		return "", createError("cache policy", name, err)
	}
	return aws.ToString(out.CachePolicy.Id), nil
}

func ensureHeadersPolicy(ctx context.Context, c Clients, class edge.Class) (string, error) {
	name := headersPolicyName(class)
	id, err := findHeadersPolicy(ctx, c, name)
	if err != nil || id != "" {
		return id, err
	}
	out, err := c.CloudFront.CreateResponseHeadersPolicy(ctx, &cloudfront.CreateResponseHeadersPolicyInput{
		ResponseHeadersPolicyConfig: &cftypes.ResponseHeadersPolicyConfig{
			Name:    aws.String(name),
			Comment: aws.String(fmt.Sprintf("Ocel: marks every response the %q edge served, so a liveness probe can tell which front answered, and keeps the origin's cache tags off the wire to the viewer.", Kind)),
			CustomHeadersConfig: &cftypes.ResponseHeadersPolicyCustomHeadersConfig{
				Quantity: ptr(int32(1)),
				Items: []cftypes.ResponseHeadersPolicyCustomHeader{{
					Header:   aws.String(edge.HeaderEdge),
					Value:    aws.String(edgeHeaderValue),
					Override: ptr(true),
				}},
			},
			RemoveHeadersConfig: &cftypes.ResponseHeadersPolicyRemoveHeadersConfig{
				Quantity: ptr(int32(1)),
				Items: []cftypes.ResponseHeadersPolicyRemoveHeader{{
					Header: aws.String(cacheTagHeader),
				}},
			},
		},
	})
	if err != nil {
		return "", createError("response headers policy", name, err)
	}
	return aws.ToString(out.ResponseHeadersPolicy.Id), nil
}

func ensureOriginAccessControl(ctx context.Context, c Clients, class edge.Class) (string, error) {
	name := originAccessControlName(class)
	id, err := findOriginAccessControl(ctx, c, name)
	if err != nil || id != "" {
		return id, err
	}
	out, err := c.CloudFront.CreateOriginAccessControl(ctx, &cloudfront.CreateOriginAccessControlInput{
		OriginAccessControlConfig: &cftypes.OriginAccessControlConfig{
			Name:                          aws.String(name),
			Description:                   aws.String(fmt.Sprintf("Ocel: signs the %q edge's reads of the asset bucket, so the bucket stays closed to everyone else.", Kind)),
			OriginAccessControlOriginType: cftypes.OriginAccessControlOriginTypesS3,
			SigningBehavior:               cftypes.OriginAccessControlSigningBehaviorsAlways,
			SigningProtocol:               cftypes.OriginAccessControlSigningProtocolsSigv4,
		},
	})
	if err != nil {
		return "", createError("origin access control", name, err)
	}
	return aws.ToString(out.OriginAccessControl.Id), nil
}

func findCachePolicy(ctx context.Context, c Clients, name string) (string, error) {
	var marker *string
	for page := 0; page < listPageCeiling; page++ {
		out, err := c.CloudFront.ListCachePolicies(ctx, &cloudfront.ListCachePoliciesInput{
			Type:   cftypes.CachePolicyTypeCustom,
			Marker: marker,
		})
		if err != nil {
			return "", fmt.Errorf("read the cache policies this account already holds: %w", err)
		}
		for _, item := range out.CachePolicyList.Items {
			if item.CachePolicy == nil || item.CachePolicy.CachePolicyConfig == nil {
				continue
			}
			if aws.ToString(item.CachePolicy.CachePolicyConfig.Name) == name {
				return aws.ToString(item.CachePolicy.Id), nil
			}
		}
		if marker = out.CachePolicyList.NextMarker; aws.ToString(marker) == "" {
			return "", nil
		}
	}
	return "", pagedForever("cache policies")
}

func findHeadersPolicy(ctx context.Context, c Clients, name string) (string, error) {
	var marker *string
	for page := 0; page < listPageCeiling; page++ {
		out, err := c.CloudFront.ListResponseHeadersPolicies(ctx, &cloudfront.ListResponseHeadersPoliciesInput{
			Type:   cftypes.ResponseHeadersPolicyTypeCustom,
			Marker: marker,
		})
		if err != nil {
			return "", fmt.Errorf("read the response headers policies this account already holds: %w", err)
		}
		for _, item := range out.ResponseHeadersPolicyList.Items {
			if item.ResponseHeadersPolicy == nil || item.ResponseHeadersPolicy.ResponseHeadersPolicyConfig == nil {
				continue
			}
			if aws.ToString(item.ResponseHeadersPolicy.ResponseHeadersPolicyConfig.Name) == name {
				return aws.ToString(item.ResponseHeadersPolicy.Id), nil
			}
		}
		if marker = out.ResponseHeadersPolicyList.NextMarker; aws.ToString(marker) == "" {
			return "", nil
		}
	}
	return "", pagedForever("response headers policies")
}

func findOriginAccessControl(ctx context.Context, c Clients, name string) (string, error) {
	var marker *string
	for page := 0; page < listPageCeiling; page++ {
		out, err := c.CloudFront.ListOriginAccessControls(ctx, &cloudfront.ListOriginAccessControlsInput{Marker: marker})
		if err != nil {
			return "", fmt.Errorf("read the origin access controls this account already holds: %w", err)
		}
		for _, item := range out.OriginAccessControlList.Items {
			if aws.ToString(item.Name) == name {
				return aws.ToString(item.Id), nil
			}
		}
		if marker = out.OriginAccessControlList.NextMarker; aws.ToString(marker) == "" {
			return "", nil
		}
	}
	return "", pagedForever("origin access controls")
}

func pagedForever(what string) error {
	return fmt.Errorf("read the %s this account already holds: CloudFront handed back a %d-th page and kept asking for more, which it does not do for an account of any size. Wait a minute and run the same command again; if it keeps happening, this is an AWS-side fault and nothing in your account needs changing", what, listPageCeiling)
}

func teardownEdgeSet(ctx context.Context, c Clients, class edge.Class) error {
	var errs []error

	name := functionName(class)
	described, err := c.CloudFront.DescribeFunction(ctx, &cloudfront.DescribeFunctionInput{
		Name:  aws.String(name),
		Stage: cftypes.FunctionStageDevelopment,
	})
	switch {
	case err != nil && !isNotFound(err):
		errs = append(errs, fmt.Errorf("read the resolver function %q: %w", name, err))
	case err == nil:
		if _, err := c.CloudFront.DeleteFunction(ctx, &cloudfront.DeleteFunctionInput{
			Name:    aws.String(name),
			IfMatch: described.ETag,
		}); err != nil && !isNotFound(err) {
			errs = append(errs, fmt.Errorf("delete the resolver function %q: %w", name, err))
		}
	}

	store := keyValueStoreName(class)
	held, err := c.CloudFront.DescribeKeyValueStore(ctx, &cloudfront.DescribeKeyValueStoreInput{Name: aws.String(store)})
	switch {
	case err != nil && !isNotFound(err):
		errs = append(errs, fmt.Errorf("read the key value store %q: %w", store, err))
	case err == nil:
		if _, err := c.CloudFront.DeleteKeyValueStore(ctx, &cloudfront.DeleteKeyValueStoreInput{
			Name:    aws.String(store),
			IfMatch: held.ETag,
		}); err != nil && !isNotFound(err) {
			errs = append(errs, fmt.Errorf("delete the key value store %q: %w", store, err))
		}
	}

	if id, err := findCachePolicy(ctx, c, cachePolicyName(class)); err != nil {
		errs = append(errs, err)
	} else if id != "" {
		if _, err := c.CloudFront.DeleteCachePolicy(ctx, &cloudfront.DeleteCachePolicyInput{Id: aws.String(id)}); err != nil && !isNotFound(err) {
			errs = append(errs, fmt.Errorf("delete the cache policy %q: %w", cachePolicyName(class), err))
		}
	}
	if id, err := findHeadersPolicy(ctx, c, headersPolicyName(class)); err != nil {
		errs = append(errs, err)
	} else if id != "" {
		if _, err := c.CloudFront.DeleteResponseHeadersPolicy(ctx, &cloudfront.DeleteResponseHeadersPolicyInput{Id: aws.String(id)}); err != nil && !isNotFound(err) {
			errs = append(errs, fmt.Errorf("delete the response headers policy %q: %w", headersPolicyName(class), err))
		}
	}
	if id, err := findOriginAccessControl(ctx, c, originAccessControlName(class)); err != nil {
		errs = append(errs, err)
	} else if id != "" {
		if _, err := c.CloudFront.DeleteOriginAccessControl(ctx, &cloudfront.DeleteOriginAccessControlInput{Id: aws.String(id)}); err != nil && !isNotFound(err) {
			errs = append(errs, fmt.Errorf("delete the origin access control %q: %w", originAccessControlName(class), err))
		}
	}
	return errors.Join(errs...)
}
