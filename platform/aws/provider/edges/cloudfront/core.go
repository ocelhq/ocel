package cloudfront

import (
	"fmt"
	"time"

	"github.com/ocelhq/ocel/pkg/providerkit"
	"github.com/ocelhq/ocel/platform/aws/provider/bootstrap"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

const (
	cacheMaxTTL = int64(365 * 24 * time.Hour / time.Second)

	rscQueryParameter = "_rsc"

	outputRoutesStoreARN = "EdgeRoutesStoreArn"
	outputResolverARN    = "EdgeResolverArn"
	outputCachePolicy    = "EdgeCachePolicyId"
	outputHeadersPolicy  = "EdgeHeadersPolicyId"
	outputAssetAccess    = "EdgeAssetAccessId"
)

type edgeSet struct {
	keyValueStoreARN    string
	functionARN         string
	cachePolicy         string
	headersPolicy       string
	originAccessControl string
}

var _ bootstrap.CoreFront = (*provider)(nil)

func (p *provider) CoreStack(class string) bootstrap.CoreFragment {
	held := edge.Class(class)
	if knownClass(held) != nil {
		return bootstrap.CoreFragment{}
	}
	return bootstrap.CoreFragment{
		Resources: routesStoreResource(held) +
			resolverResource(held) +
			cachePolicyResource(held) +
			headersPolicyResource(held) +
			assetAccessResource(held),
		Outputs: edgeSetOutputs(),
	}
}

func edgeSetOf(deployed bootstrap.Deployed, class edge.Class) (edgeSet, error) {
	set := edgeSet{
		keyValueStoreARN:    deployed.CoreOutputs[outputRoutesStoreARN],
		functionARN:         deployed.CoreOutputs[outputResolverARN],
		cachePolicy:         deployed.CoreOutputs[outputCachePolicy],
		headersPolicy:       deployed.CoreOutputs[outputHeadersPolicy],
		originAccessControl: deployed.CoreOutputs[outputAssetAccess],
	}
	if set.keyValueStoreARN == "" || set.functionARN == "" || set.cachePolicy == "" || set.headersPolicy == "" || set.originAccessControl == "" {
		return edgeSet{}, unbootstrapped(class)
	}
	return set, nil
}

func unbootstrapped(class edge.Class) error {
	return fmt.Errorf("the %s bootstrap in this account carries nothing the %q edge fronts deployments with: its resolver function, key value store and cache policies belong to the bootstrap stack, and this account's was written for a different edge. Run `%s` against this account, then deploy again", class, Kind, providerkit.BootstrapCommand(class))
}

func routesStoreResource(class edge.Class) string {
	return fmt.Sprintf(`  EdgeRoutes:
    Type: AWS::CloudFront::KeyValueStore
    Metadata:
      Description: "One entry per hostname naming the release that answers on it. Written at promote, read by the resolver on every request."
    Properties:
      Name: %q
      Comment: "Ocel: one entry per hostname naming the release that answers on it. Written at promote, read by the resolver on every request."
`, keyValueStoreName(class))
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
%s`, functionName(class), bootstrap.Indent(string(ResolverCode()), 8))
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
`, cachePolicyName(class), cacheMaxTTL, cacheKeyHeader, rscQueryParameter)
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
`, headersPolicyName(class), fmt.Sprintf("Ocel: marks every response the %q edge served, so a probe can tell which front answered, and drops cache tags.", Kind), edge.HeaderEdge, edgeHeaderValue, cacheTagHeader)
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
`, originAccessControlName(class), fmt.Sprintf("Ocel: signs the %q edge's reads of the asset bucket, so the bucket stays closed to everyone else.", Kind))
}

func edgeSetOutputs() string {
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
`, outputRoutesStoreARN, outputResolverARN, outputCachePolicy, outputHeadersPolicy, outputAssetAccess)
}
