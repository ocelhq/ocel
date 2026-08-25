package cloudfront

import (
	"maps"
	"slices"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/ocelhq/ocel/platform/aws/provider/bootstrap"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

func renderedCore(t *testing.T, class edge.Class) map[string]any {
	t.Helper()
	fragment := newWorld().edge().CoreStack(string(class))
	body := "Resources:\n" + fragment.Resources + "Outputs:\n" + fragment.Outputs
	var parsed map[string]any
	if err := yaml.Unmarshal([]byte(strings.ReplaceAll(strings.ReplaceAll(body, "!GetAtt ", ""), "!Ref ", "")), &parsed); err != nil {
		t.Fatalf("the fragment the core stack splices in is not valid YAML, so the plan cannot read it: %v\n%s", err, body)
	}
	return parsed
}

func section(t *testing.T, parsed map[string]any, name string) map[string]any {
	t.Helper()
	held, ok := parsed[name].(map[string]any)
	if !ok {
		t.Fatalf("the fragment has no %s section", name)
	}
	return held
}

func TestCoreStackCarriesTheSetTheEdgeRoutesWith(t *testing.T) {
	t.Parallel()

	for _, class := range []edge.Class{edge.ClassProduction, edge.ClassPreview} {
		t.Run(string(class), func(t *testing.T) {
			t.Parallel()

			parsed := renderedCore(t, class)
			resources := section(t, parsed, "Resources")
			want := map[string]string{
				"EdgeRoutes":        "AWS::CloudFront::KeyValueStore",
				"EdgeResolver":      "AWS::CloudFront::Function",
				"EdgeCachePolicy":   "AWS::CloudFront::CachePolicy",
				"EdgeHeadersPolicy": "AWS::CloudFront::ResponseHeadersPolicy",
				"EdgeAssetAccess":   "AWS::CloudFront::OriginAccessControl",
			}
			assertSet(t, "the resources the fragment carries", slices.Collect(maps.Keys(resources)), slices.Sorted(maps.Keys(want)))
			for id, kind := range want {
				held, ok := resources[id].(map[string]any)
				if !ok {
					t.Fatalf("the fragment declares no %s, so the plan would never name it", id)
				}
				if held["Type"] != kind {
					t.Errorf("%s type = %v, want %s", id, held["Type"], kind)
				}
			}

			outputs := section(t, parsed, "Outputs")
			assertSet(t, "the outputs the fragment publishes", slices.Collect(maps.Keys(outputs)),
				[]string{outputRoutesStoreARN, outputResolverARN, outputCachePolicy, outputHeadersPolicy, outputAssetAccess})

			named := []struct{ id, under, want string }{
				{"EdgeRoutes", "", keyValueStoreName(class)},
				{"EdgeResolver", "", functionName(class)},
				{"EdgeCachePolicy", "CachePolicyConfig", cachePolicyName(class)},
				{"EdgeHeadersPolicy", "ResponseHeadersPolicyConfig", headersPolicyName(class)},
				{"EdgeAssetAccess", "OriginAccessControlConfig", originAccessControlName(class)},
			}
			for _, held := range named {
				if got := nameOf(resources, held.id, held.under); got != held.want {
					t.Errorf("%s is named %q, want %q", held.id, got, held.want)
				}
			}
		})
	}
}

func nameOf(resources map[string]any, id, under string) string {
	held, _ := resources[id].(map[string]any)["Properties"].(map[string]any)
	if under != "" {
		held, _ = held[under].(map[string]any)
	}
	name, _ := held["Name"].(string)
	return name
}

func TestTheResolverRunsTheCodeThisBuildCarriesAgainstTheStoreBesideIt(t *testing.T) {
	t.Parallel()

	resources := section(t, renderedCore(t, edge.ClassProduction), "Resources")
	properties, _ := resources["EdgeResolver"].(map[string]any)["Properties"].(map[string]any)

	if got, want := properties["FunctionCode"], string(ResolverCode()); got != want {
		t.Errorf("the resolver is written into the stack as %q, want the code this build carries", got)
	}
	if properties["Name"] != functionName(edge.ClassProduction) {
		t.Errorf("the resolver is named %v, want %q", properties["Name"], functionName(edge.ClassProduction))
	}
	config, _ := properties["FunctionConfig"].(map[string]any)
	if config["Runtime"] != "cloudfront-js-2.0" {
		t.Errorf("runtime = %v, want the only one that reads a key value store", config["Runtime"])
	}
	associations, _ := config["KeyValueStoreAssociations"].([]any)
	if len(associations) != 1 {
		t.Fatalf("the resolver is associated with %d key value stores, want only the one this stack makes", len(associations))
	}
	held, _ := associations[0].(map[string]any)
	if held["KeyValueStoreARN"] != "EdgeRoutes.Arn" {
		t.Errorf("the resolver reads %v, want the key value store this same stack makes", held["KeyValueStoreARN"])
	}
}

func TestTheFrontKeepsTheOriginsCacheTagsOffTheWire(t *testing.T) {
	t.Parallel()

	resources := section(t, renderedCore(t, edge.ClassProduction), "Resources")
	properties, _ := resources["EdgeHeadersPolicy"].(map[string]any)["Properties"].(map[string]any)
	config, _ := properties["ResponseHeadersPolicyConfig"].(map[string]any)

	removed, _ := config["RemoveHeadersConfig"].(map[string]any)["Items"].([]any)
	var dropped []string
	for _, item := range removed {
		held, _ := item.(map[string]any)
		dropped = append(dropped, held["Header"].(string))
	}
	if !slices.Contains(dropped, cacheTagHeader) {
		t.Errorf("the response headers policy removes %v, want %q among them, or every viewer is told the tags of the page it was served", dropped, cacheTagHeader)
	}

	custom, _ := config["CustomHeadersConfig"].(map[string]any)["Items"].([]any)
	var marked bool
	for _, item := range custom {
		held, _ := item.(map[string]any)
		if held["Header"] == edge.HeaderEdge && held["Value"] == string(Kind) {
			marked = true
		}
	}
	if !marked {
		t.Errorf("no response headers policy sets %s: %s, so a liveness probe cannot tell which front answered", edge.HeaderEdge, Kind)
	}
}

func TestAnUnknownClassCarriesNothingIntoTheCoreStack(t *testing.T) {
	t.Parallel()

	if fragment := newWorld().edge().CoreStack("staging"); fragment != (bootstrap.CoreFragment{}) {
		t.Errorf("CoreStack(staging) = %+v, want nothing: the edge knows no such class", fragment)
	}
}

func TestAHalfWrittenBootstrapIsRefusedRatherThanFrontedWithHalfASet(t *testing.T) {
	t.Parallel()

	full := bootstrap.Deployed{Present: true, CoreOutputs: fakeEdgeOutputs(edge.ClassProduction)}
	if _, err := edgeSetOf(full, edge.ClassProduction); err != nil {
		t.Fatalf("edgeSetOf a bootstrap that carries the whole set: %v", err)
	}
	for _, key := range []string{outputRoutesStoreARN, outputResolverARN, outputCachePolicy, outputHeadersPolicy, outputAssetAccess} {
		partial := bootstrap.Deployed{Present: true, CoreOutputs: fakeEdgeOutputs(edge.ClassProduction)}
		delete(partial.CoreOutputs, key)
		if _, err := edgeSetOf(partial, edge.ClassProduction); err == nil {
			t.Errorf("a bootstrap missing %s was accepted, want a refusal naming the command that writes it", key)
		}
	}
}
