package cloudfront

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	cftypes "github.com/aws/aws-sdk-go-v2/service/cloudfront/types"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/pricing"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/router"
	"github.com/ocelhq/ocel/platform/aws/provider/account"
	"github.com/ocelhq/ocel/platform/aws/provider/edges/cloudfront/resolver"
)

const (
	Part    = "cloudfront"
	Summary = "CloudFront as the front: the routes store, resolver and empty-body functions, cache and headers policies and asset access every distribution shares"

	Kind = "cloudfront"

	CacheTagHeader = "cache-tag"

	cacheKeyHeader      = "x-ocel-cache-key"
	previewRobotsHeader = "x-robots-tag"
	previewRobotsValue  = "noindex"
	rscQueryParameter   = "_rsc"

	cacheMaxTTL = int64(365 * 24 * time.Hour / time.Second)

	functionRuntime = cftypes.FunctionRuntimeCloudfrontJs20

	KindRoutesStore   = "AWS::CloudFront::KeyValueStore"
	KindFunction      = "AWS::CloudFront::Function"
	KindCachePolicy   = "AWS::CloudFront::CachePolicy"
	KindHeadersPolicy = "AWS::CloudFront::ResponseHeadersPolicy"
	KindAssetAccess   = "AWS::CloudFront::OriginAccessControl"
)

type Spec struct {
	Namespace account.Namespace
	Tier      environment.Tier
}

type Names struct {
	RoutesStore   string
	Resolver      string
	EmptyBody     string
	CachePolicy   string
	HeadersPolicy string
	AssetAccess   string
}

func NamesFor(ns account.Namespace, tier environment.Tier) Names {
	return Names{
		RoutesStore:   ns.EdgeRoutesStoreName(tier),
		Resolver:      ns.EdgeResolverName(tier),
		EmptyBody:     ns.EdgeEmptyBodyName(tier),
		CachePolicy:   ns.EdgeCachePolicyName(tier),
		HeadersPolicy: ns.EdgeHeadersPolicyName(tier),
		AssetAccess:   ns.EdgeAssetAccessName(tier),
	}
}

type Set struct {
	RoutesStore   string
	Resolver      string
	EmptyBody     string
	CachePolicy   string
	HeadersPolicy string
	AssetAccess   string
}

func (s Set) Complete() bool {
	return s.RoutesStore != "" && s.Resolver != "" && s.EmptyBody != "" && s.CachePolicy != "" && s.HeadersPolicy != "" && s.AssetAccess != ""
}

func SetOf(names Names, resources []provider.PartResource) Set {
	var set Set
	for _, resource := range resources {
		switch resource.Kind {
		case KindRoutesStore:
			set.RoutesStore = resource.ID
		case KindFunction:
			switch {
			case strings.HasSuffix(resource.ID, "/"+names.Resolver):
				set.Resolver = resource.ID
			case strings.HasSuffix(resource.ID, "/"+names.EmptyBody):
				set.EmptyBody = resource.ID
			}
		case KindCachePolicy:
			set.CachePolicy = resource.ID
		case KindHeadersPolicy:
			set.HeadersPolicy = resource.ID
		case KindAssetAccess:
			set.AssetAccess = resource.ID
		}
	}
	return set
}

func (s Set) resources() []provider.PartResource {
	return []provider.PartResource{
		{Kind: KindRoutesStore, ID: s.RoutesStore},
		{Kind: KindFunction, ID: s.Resolver},
		{Kind: KindFunction, ID: s.EmptyBody},
		{Kind: KindCachePolicy, ID: s.CachePolicy},
		{Kind: KindHeadersPolicy, ID: s.HeadersPolicy},
		{Kind: KindAssetAccess, ID: s.AssetAccess},
	}
}

func Digest(spec Spec) string {
	names := NamesFor(spec.Namespace, spec.Tier)
	sum := sha256.Sum256([]byte(strings.Join([]string{
		string(spec.Tier), names.RoutesStore, names.Resolver, names.EmptyBody, names.CachePolicy, names.HeadersPolicy, names.AssetAccess,
		string(resolver.Code()), string(resolver.EmptyBodyCode()), resolver.GateDigest(),
		cacheKeyHeader, rscQueryParameter, strconv.FormatInt(cacheMaxTTL, 10), router.HeaderRouter, CacheTagHeader, edge.HeaderEmptyBody,
	}, "\n")))
	return hex.EncodeToString(sum[:])
}

func Shape(ns account.Namespace, tier environment.Tier) []pricing.Shaped {
	names := NamesFor(ns, tier)
	return []pricing.Shaped{
		{Name: names.RoutesStore, Type: "aws_cloudfront_key_value_store", Properties: map[string]any{}},
		{Name: names.Resolver, Type: "aws_cloudfront_function", Properties: map[string]any{}},
		{Name: names.EmptyBody, Type: "aws_cloudfront_function", Properties: map[string]any{}},
	}
}

func Ensure(ctx context.Context, c API, spec Spec, prior []provider.PartResource) ([]provider.PartResource, error) {
	names := NamesFor(spec.Namespace, spec.Tier)
	recorded := SetOf(names, prior)
	var set Set
	var err error
	if set.RoutesStore, err = ensureRoutesStore(ctx, c, names.RoutesStore); err != nil {
		return nil, err
	}
	routes := &cftypes.KeyValueStoreAssociations{Quantity: aws.Int32(1), Items: []cftypes.KeyValueStoreAssociation{{KeyValueStoreARN: aws.String(set.RoutesStore)}}}
	if set.Resolver, err = ensureFunction(ctx, c, names.Resolver, &cftypes.FunctionConfig{
		Comment:                   aws.String("Ocel: reads the hostname's release out of the key value store and points the request at the release's assets or root function."),
		Runtime:                   functionRuntime,
		KeyValueStoreAssociations: routes,
	}, resolver.Code()); err != nil {
		return nil, err
	}
	if set.EmptyBody, err = ensureFunction(ctx, c, names.EmptyBody, &cftypes.FunctionConfig{
		Comment: aws.String("Ocel: empties the sentinel byte an origin marks a bodiless response with, and drops the mark."),
		Runtime: functionRuntime,
	}, resolver.EmptyBodyCode()); err != nil {
		return nil, err
	}
	if set.CachePolicy, err = ensureCachePolicy(ctx, c, recorded.CachePolicy, cachePolicyConfig(names)); err != nil {
		return nil, err
	}
	if set.HeadersPolicy, err = ensureHeadersPolicy(ctx, c, recorded.HeadersPolicy, headersPolicyConfig(names, spec.Tier)); err != nil {
		return nil, err
	}
	if set.AssetAccess, err = ensureAssetAccess(ctx, c, recorded.AssetAccess, assetAccessConfig(names)); err != nil {
		return nil, err
	}
	return set.resources(), nil
}

func cachePolicyConfig(names Names) *cftypes.CachePolicyConfig {
	return &cftypes.CachePolicyConfig{
		Name:       aws.String(names.CachePolicy),
		Comment:    aws.String("Ocel: keys the cache on the hostname and the resolver's variant; the origin's Cache-Control sets how long anything is cached."),
		MinTTL:     aws.Int64(0),
		DefaultTTL: aws.Int64(0),
		MaxTTL:     aws.Int64(cacheMaxTTL),
		ParametersInCacheKeyAndForwardedToOrigin: &cftypes.ParametersInCacheKeyAndForwardedToOrigin{
			EnableAcceptEncodingGzip:   aws.Bool(true),
			EnableAcceptEncodingBrotli: aws.Bool(true),
			HeadersConfig: &cftypes.CachePolicyHeadersConfig{
				HeaderBehavior: cftypes.CachePolicyHeaderBehaviorWhitelist,
				Headers:        &cftypes.Headers{Quantity: aws.Int32(2), Items: []string{"Host", cacheKeyHeader}},
			},
			CookiesConfig: &cftypes.CachePolicyCookiesConfig{CookieBehavior: cftypes.CachePolicyCookieBehaviorNone},
			QueryStringsConfig: &cftypes.CachePolicyQueryStringsConfig{
				QueryStringBehavior: cftypes.CachePolicyQueryStringBehaviorAllExcept,
				QueryStrings:        &cftypes.QueryStringNames{Quantity: aws.Int32(1), Items: []string{rscQueryParameter}},
			},
		},
	}
}

func headersPolicyConfig(names Names, tier environment.Tier) *cftypes.ResponseHeadersPolicyConfig {
	custom := []cftypes.ResponseHeadersPolicyCustomHeader{{Header: aws.String(router.HeaderRouter), Value: aws.String(Kind), Override: aws.Bool(true)}}
	if tier == environment.TierPreview {
		custom = append(custom, cftypes.ResponseHeadersPolicyCustomHeader{Header: aws.String(previewRobotsHeader), Value: aws.String(previewRobotsValue), Override: aws.Bool(true)})
	}
	removed := []cftypes.ResponseHeadersPolicyRemoveHeader{{Header: aws.String(CacheTagHeader)}, {Header: aws.String(edge.HeaderEmptyBody)}}
	return &cftypes.ResponseHeadersPolicyConfig{
		Name:                aws.String(names.HeadersPolicy),
		Comment:             aws.String(fmt.Sprintf("Ocel: names the %q router on each response it served, so a probe knows which router answered, and drops cache tags.", Kind)),
		CustomHeadersConfig: &cftypes.ResponseHeadersPolicyCustomHeadersConfig{Quantity: aws.Int32(int32(len(custom))), Items: custom},
		RemoveHeadersConfig: &cftypes.ResponseHeadersPolicyRemoveHeadersConfig{Quantity: aws.Int32(int32(len(removed))), Items: removed},
	}
}

func assetAccessConfig(names Names) *cftypes.OriginAccessControlConfig {
	return &cftypes.OriginAccessControlConfig{
		Name:                          aws.String(names.AssetAccess),
		Description:                   aws.String(fmt.Sprintf("Ocel: signs the %q edge's reads of the asset bucket, so the bucket stays closed to everyone else.", Kind)),
		OriginAccessControlOriginType: cftypes.OriginAccessControlOriginTypesS3,
		SigningBehavior:               cftypes.OriginAccessControlSigningBehaviorsAlways,
		SigningProtocol:               cftypes.OriginAccessControlSigningProtocolsSigv4,
	}
}
