package bootstrap

import (
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/ocelhq/ocel/platform/aws/provider/edges/cloudfront/resolver"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

type cloudFrontEdgeShape struct {
	Resources map[string]struct {
		Type       string `yaml:"Type"`
		Properties struct {
			Name           string `yaml:"Name"`
			AutoPublish    bool   `yaml:"AutoPublish"`
			FunctionCode   string `yaml:"FunctionCode"`
			FunctionConfig struct {
				Runtime                   string `yaml:"Runtime"`
				KeyValueStoreAssociations []struct {
					KeyValueStoreARN string `yaml:"KeyValueStoreARN"`
				} `yaml:"KeyValueStoreAssociations"`
			} `yaml:"FunctionConfig"`
			CachePolicyConfig struct {
				Name string `yaml:"Name"`
			} `yaml:"CachePolicyConfig"`
			ResponseHeadersPolicyConfig struct {
				Name string `yaml:"Name"`
			} `yaml:"ResponseHeadersPolicyConfig"`
			OriginAccessControlConfig struct {
				Name                          string `yaml:"Name"`
				OriginAccessControlOriginType string `yaml:"OriginAccessControlOriginType"`
				SigningBehavior               string `yaml:"SigningBehavior"`
			} `yaml:"OriginAccessControlConfig"`
		} `yaml:"Properties"`
	} `yaml:"Resources"`
	Outputs map[string]struct {
		Value string `yaml:"Value"`
	} `yaml:"Outputs"`
}

func TestCloudFrontEdgeStandsUpWhatEveryDistributionInTheAccountShares(t *testing.T) {
	for _, class := range []string{ClassProduction, ClassPreview} {
		t.Run(class, func(t *testing.T) {
			held := edge.Class(class)
			var tmpl cloudFrontEdgeShape
			if err := yaml.Unmarshal([]byte(featureTemplate(FeatureCloudFrontEdge, class)), &tmpl); err != nil {
				t.Fatalf("template is not valid YAML: %v", err)
			}

			routes, ok := tmpl.Resources["EdgeRoutes"]
			if !ok {
				t.Fatal("template stands up no key value store; the resolver has nothing to read a hostname's release out of")
			}
			if routes.Type != "AWS::CloudFront::KeyValueStore" {
				t.Errorf("EdgeRoutes Type = %q, want AWS::CloudFront::KeyValueStore", routes.Type)
			}
			if got, want := routes.Properties.Name, EdgeRoutesStoreName(held); got != want {
				t.Errorf("key value store name = %q, want %q", got, want)
			}

			fn, ok := tmpl.Resources["EdgeResolver"]
			if !ok {
				t.Fatal("template stands up no resolver function; nothing points a request at the release that answers on its hostname")
			}
			if fn.Type != "AWS::CloudFront::Function" {
				t.Errorf("EdgeResolver Type = %q, want AWS::CloudFront::Function", fn.Type)
			}
			if got, want := fn.Properties.Name, EdgeResolverName(held); got != want {
				t.Errorf("resolver name = %q, want %q", got, want)
			}
			if !fn.Properties.AutoPublish {
				t.Error("AutoPublish = false; an unpublished resolver runs on no distribution")
			}
			if got, want := strings.TrimRight(fn.Properties.FunctionCode, "\n"), strings.TrimRight(string(resolver.Code()), "\n"); got != want {
				t.Error("the function carries something other than the resolver this build embeds")
			}
			if got := fn.Properties.FunctionConfig.Runtime; got != "cloudfront-js-2.0" {
				t.Errorf("resolver runtime = %q, want cloudfront-js-2.0: no earlier runtime reads a key value store", got)
			}
			assoc := fn.Properties.FunctionConfig.KeyValueStoreAssociations
			if len(assoc) != 1 || assoc[0].KeyValueStoreARN != "EdgeRoutes.Arn" {
				t.Errorf("key value store associations = %+v, want the store this same stack stands up", assoc)
			}

			cache, ok := tmpl.Resources["EdgeCachePolicy"]
			if !ok {
				t.Fatal("template stands up no cache policy; every distribution would have to carry one of its own")
			}
			if cache.Type != "AWS::CloudFront::CachePolicy" {
				t.Errorf("EdgeCachePolicy Type = %q, want AWS::CloudFront::CachePolicy", cache.Type)
			}
			if got, want := cache.Properties.CachePolicyConfig.Name, edgeCachePolicyName(held); got != want {
				t.Errorf("cache policy name = %q, want %q", got, want)
			}

			headers, ok := tmpl.Resources["EdgeHeadersPolicy"]
			if !ok {
				t.Fatal("template stands up no response-headers policy; no probe can tell which front answered")
			}
			if headers.Type != "AWS::CloudFront::ResponseHeadersPolicy" {
				t.Errorf("EdgeHeadersPolicy Type = %q, want AWS::CloudFront::ResponseHeadersPolicy", headers.Type)
			}
			if got, want := headers.Properties.ResponseHeadersPolicyConfig.Name, edgeHeadersPolicyName(held); got != want {
				t.Errorf("response headers policy name = %q, want %q", got, want)
			}

			access, ok := tmpl.Resources["EdgeAssetAccess"]
			if !ok {
				t.Fatal("template stands up no origin access control; no distribution can read the asset bucket the core keeps closed")
			}
			if access.Type != "AWS::CloudFront::OriginAccessControl" {
				t.Errorf("EdgeAssetAccess Type = %q, want AWS::CloudFront::OriginAccessControl", access.Type)
			}
			if got, want := access.Properties.OriginAccessControlConfig.Name, edgeAssetAccessName(held); got != want {
				t.Errorf("origin access control name = %q, want %q", got, want)
			}
			if got := access.Properties.OriginAccessControlConfig.OriginAccessControlOriginType; got != "s3" {
				t.Errorf("origin type = %q, want s3: the asset bucket is what a distribution reads through it", got)
			}
			if got := access.Properties.OriginAccessControlConfig.SigningBehavior; got != "always" {
				t.Errorf("SigningBehavior = %q, want always: an unsigned read is refused by the bucket", got)
			}

			for _, key := range []string{
				OutputEdgeRoutesStoreARN,
				OutputEdgeResolverARN,
				OutputEdgeCachePolicy,
				OutputEdgeHeadersPolicy,
				OutputEdgeAssetAccess,
			} {
				if _, ok := tmpl.Outputs[key]; !ok {
					t.Errorf("template does not output %s, which every CloudFront deploy reads off this stack", key)
				}
			}
		})
	}
}

func TestTheCoreCarriesNothingACloudFrontFrontNeeds(t *testing.T) {
	for _, class := range []string{ClassProduction, ClassPreview} {
		t.Run(class, func(t *testing.T) {
			body := coreStackTemplate(class)
			for _, resource := range templateResources(body) {
				if strings.HasPrefix(resource.kind, "AWS::CloudFront::") {
					t.Errorf("the core holds %s (%s); an edge stands in a feature stack of its own", resource.id, resource.kind)
				}
			}
			var tmpl cloudFrontEdgeShape
			if err := yaml.Unmarshal([]byte(body), &tmpl); err != nil {
				t.Fatalf("core template is not valid YAML: %v", err)
			}
			for _, key := range []string{
				OutputEdgeRoutesStoreARN,
				OutputEdgeResolverARN,
				OutputEdgeCachePolicy,
				OutputEdgeHeadersPolicy,
				OutputEdgeAssetAccess,
			} {
				if _, ok := tmpl.Outputs[key]; ok {
					t.Errorf("the core outputs %s; a deploy would read the edge off a stack no edge writes", key)
				}
			}
		})
	}
}
