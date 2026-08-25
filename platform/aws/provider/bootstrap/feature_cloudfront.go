package bootstrap

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/platform/aws/provider/edges/cloudfront/resolver"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

const (
	KindCloudFront = "cloudfront"

	OutputEdgeRoutesStoreARN = "EdgeRoutesStoreArn"
	OutputEdgeResolverARN    = "EdgeResolverArn"
	OutputEdgeCachePolicy    = "EdgeCachePolicyId"
	OutputEdgeHeadersPolicy  = "EdgeHeadersPolicyId"
	OutputEdgeAssetAccess    = "EdgeAssetAccessId"

	EdgeCacheTagHeader = "cache-tag"

	edgeCacheKeyHeader = "x-ocel-cache-key"

	edgeRSCQueryParameter = "_rsc"

	edgeCacheMaxTTL = int64(365 * 24 * time.Hour / time.Second)

	edgeNamespace = "ocel"
)

var cloudFrontEdgeFeature = feature{
	name:       FeatureCloudFrontEdge,
	summary:    "CloudFront as the front — routes store, resolver function, cache and headers policies, asset access",
	needs:      []string{needsEdgePrefix + KindCloudFront},
	template:   cloudFrontEdgeTemplate,
	payloads:   noPayloads,
	placements: noPlacements,
}

func EdgeRoutesStoreName(class edge.Class) string { return edgeSetName("routes", class) }

func EdgeResolverName(class edge.Class) string { return edgeSetName("resolver", class) }

func edgeCachePolicyName(class edge.Class) string { return edgeSetName("cache", class) }

func edgeHeadersPolicyName(class edge.Class) string { return edgeSetName("headers", class) }

func edgeAssetAccessName(class edge.Class) string { return edgeSetName("assets", class) }

func edgeSetName(what string, class edge.Class) string {
	if class == edge.ClassPreview {
		return naming.Join(naming.WordSeparator, edgeNamespace, what, string(edge.ClassPreview))
	}
	return naming.Join(naming.WordSeparator, edgeNamespace, what)
}

func cloudFrontEdgeTemplate(in featureInputs) featureStack {
	held := edge.Class(in.class)
	return featureStack{
		body: fmt.Sprintf(`AWSTemplateFormatVersion: '2010-09-09'
Description: "Ocel bootstrap feature (%s, %s) - what a CloudFront front needs in this account before any deployment is fronted with it: the key value store one entry per hostname is written into, the resolver function every distribution runs, the cache and response-headers policies they answer by, and the origin access control they read the asset bucket through."
Resources:
%s%s%s%s%sOutputs:
%s`,
			FeatureCloudFrontEdge, in.class,
			routesStoreResource(held),
			resolverResource(held),
			cachePolicyResource(held),
			headersPolicyResource(held),
			assetAccessResource(held),
			cloudFrontEdgeOutputs()),
	}
}

func routesStoreResource(class edge.Class) string {
	return fmt.Sprintf(`  EdgeRoutes:
    Type: AWS::CloudFront::KeyValueStore
    Metadata:
      Description: "One entry per hostname naming the release that answers on it. Written at promote, read by the resolver on every request."
    Properties:
      Name: %q
      Comment: "Ocel: one entry per hostname naming the release that answers on it. Written at promote, read by the resolver on every request."
`, EdgeRoutesStoreName(class))
}

func resolverResource(class edge.Class) string {
	return fmt.Sprintf(`  EdgeResolver:
    Type: AWS::CloudFront::Function
    Metadata:
      Description: "Runs on every request this account fronts: reads the hostname's release out of the key value store and points the request at that release's assets or entry function."
    Properties:
      Name: %q
      AutoPublish: true
      FunctionConfig:
        Comment: "Ocel: reads the hostname's release out of the key value store and points the request at the release's assets or entry function."
        Runtime: cloudfront-js-2.0
        KeyValueStoreAssociations:
          - KeyValueStoreARN: !GetAtt EdgeRoutes.Arn
      FunctionCode: |
%s`, EdgeResolverName(class), indent(string(resolver.Code()), 8))
}

func cachePolicyResource(class edge.Class) string {
	return fmt.Sprintf(`  EdgeCachePolicy:
    Type: AWS::CloudFront::CachePolicy
    Metadata:
      Description: "Keys every distribution's cache on the hostname and the resolver's variant; the origin's Cache-Control governs how long anything is held."
    Properties:
      CachePolicyConfig:
        Name: %q
        Comment: "Ocel: keys the cache on the hostname and the resolver's variant; the origin's Cache-Control governs how long anything is held."
        MinTTL: 0
        DefaultTTL: 0
        MaxTTL: %d
        ParametersInCacheKeyAndForwardedToOrigin:
          EnableAcceptEncodingGzip: true
          EnableAcceptEncodingBrotli: true
          HeadersConfig:
            HeaderBehavior: whitelist
            Headers:
              - Host
              - %s
          CookiesConfig:
            CookieBehavior: none
          QueryStringsConfig:
            QueryStringBehavior: allExcept
            QueryStrings:
              - %s
`, edgeCachePolicyName(class), edgeCacheMaxTTL, edgeCacheKeyHeader, edgeRSCQueryParameter)
}

func headersPolicyResource(class edge.Class) string {
	return fmt.Sprintf(`  EdgeHeadersPolicy:
    Type: AWS::CloudFront::ResponseHeadersPolicy
    Metadata:
      Description: "Marks every response this edge served, so a probe can tell which front answered, and drops cache tags before they reach a browser."
    Properties:
      ResponseHeadersPolicyConfig:
        Name: %q
        Comment: %q
        CustomHeadersConfig:
          Items:
            - Header: %q
              Value: %q
              Override: true
        RemoveHeadersConfig:
          Items:
            - Header: %q
`, edgeHeadersPolicyName(class),
		fmt.Sprintf("Ocel: marks every response the %q edge served, so a probe can tell which front answered, and drops cache tags.", KindCloudFront),
		edge.HeaderEdge, KindCloudFront, EdgeCacheTagHeader)
}

func assetAccessResource(class edge.Class) string {
	return fmt.Sprintf(`  EdgeAssetAccess:
    Type: AWS::CloudFront::OriginAccessControl
    Metadata:
      Description: "Signs every distribution's reads of the asset bucket, so the bucket stays closed to everyone else."
    Properties:
      OriginAccessControlConfig:
        Name: %q
        Description: %q
        OriginAccessControlOriginType: s3
        SigningBehavior: always
        SigningProtocol: sigv4
`, edgeAssetAccessName(class),
		fmt.Sprintf("Ocel: signs the %q edge's reads of the asset bucket, so the bucket stays closed to everyone else.", KindCloudFront))
}

func cloudFrontEdgeOutputs() string {
	return fmt.Sprintf(`  %s:
    Description: "Key value store the resolver reads a hostname's release out of. Every promote writes into it."
    Value: !GetAtt EdgeRoutes.Arn
  %s:
    Description: "Published resolver function every distribution this account fronts runs on viewer request."
    Value: !GetAtt EdgeResolver.FunctionARN
  %s:
    Description: "Cache policy every distribution this account fronts caches by."
    Value: !Ref EdgeCachePolicy
  %s:
    Description: "Response headers policy every distribution this account fronts answers with."
    Value: !Ref EdgeHeadersPolicy
  %s:
    Description: "Origin access control every distribution this account fronts signs its asset-bucket reads with."
    Value: !Ref EdgeAssetAccess
`, OutputEdgeRoutesStoreARN, OutputEdgeResolverARN, OutputEdgeCachePolicy, OutputEdgeHeadersPolicy, OutputEdgeAssetAccess)
}

func noPayloads(context.Context, ObjectStore, string) (stackPayloads, error) {
	return stackPayloads{}, nil
}

func noPlacements(string) stackPayloads { return stackPayloads{} }

func indent(body string, by int) string {
	pad := strings.Repeat(" ", by)
	lines := strings.Split(strings.TrimRight(body, "\n"), "\n")
	var b strings.Builder
	for _, line := range lines {
		if line != "" {
			b.WriteString(pad)
			b.WriteString(line)
		}
		b.WriteString("\n")
	}
	return b.String()
}
