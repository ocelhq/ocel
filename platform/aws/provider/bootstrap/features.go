package bootstrap

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	cfntypes "github.com/aws/aws-sdk-go-v2/service/cloudformation/types"

	"github.com/ocelhq/ocel/pkg/providerkit"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

const (
	FeatureISR               = "isr"
	FeatureImageOptimization = "image-optimization"
	FeatureCloudflareEdge    = "cloudflare-edge"
	FeatureCloudFrontEdge    = "cloudfront-edge"
	FeatureAPIGatewayEdge    = "apigateway-edge"

	needsFrameworkPrefix = providerkit.NeedsFrameworkPrefix
	needsEdgePrefix      = providerkit.NeedsEdgePrefix
)

type stackRefs struct {
	assetBucket         string
	assetBucketARN      string
	stateTable          string
	stateTableARN       string
	stateTableStreamARN string
	revalidateQueueARN  string
	imageOptimizerARN   string
}

type featureInputs struct {
	class     string
	code      stackPayloads
	refs      stackRefs
	alongside FeatureSet
}

type featureStack struct {
	body   string
	params []cfntypes.Parameter
}

type stepDeps struct {
	class    string
	ssm      SSMAPI
	iam      IAMAPI
	progress func(string)
	log      func(string)
}

type feature struct {
	name      string
	summary   string
	dependsOn []string
	needs     []string

	template   func(featureInputs) featureStack
	payloads   func(context.Context, ObjectStore, string) (stackPayloads, error)
	placements func(string) stackPayloads
	after      func(context.Context, stepDeps) error
	afterPlan  func(context.Context, ParamAPIs, string) ([]providerkit.Change, error)
	drop       func(context.Context, stepDeps) error
	dropPlan   func(context.Context, ParamAPIs, string) ([]providerkit.Change, error)
}

func (f feature) render(class, artifactBucket string, refs stackRefs, alongside FeatureSet) featureStack {
	return f.template(featureInputs{
		class:     class,
		code:      f.placements(artifactBucket),
		refs:      refs,
		alongside: alongside,
	})
}

func (f feature) edgeKind() (edge.Kind, bool) {
	for _, need := range f.needs {
		if kind, ok := strings.CutPrefix(need, needsEdgePrefix); ok {
			return edge.Kind(kind), true
		}
	}
	return "", false
}

func (f feature) stackName(class string) string {
	if class == ClassPreview {
		return StackName + "-" + f.name + "-preview"
	}
	return StackName + "-" + f.name
}

var featureRegistry = []feature{
	isrFeature,
	imageOptimizationFeature,
	cloudflareEdgeFeature,
	cloudFrontEdgeFeature,
	apiGatewayEdgeFeature,
}

func Catalogue() []providerkit.Feature {
	out := make([]providerkit.Feature, 0, len(featureRegistry))
	for _, f := range featureRegistry {
		out = append(out, providerkit.Feature{
			Name:      f.name,
			Summary:   f.summary,
			DependsOn: slices.Clone(f.dependsOn),
			Needs:     slices.Clone(f.needs),
		})
	}
	return out
}

func featureNamed(name string) (feature, bool) {
	for _, f := range featureRegistry {
		if f.name == name {
			return f, true
		}
	}
	return feature{}, false
}

func edgeKinds() []edge.Kind { return EdgeKindsFor(featureNames()) }

func EdgeKindsFor(names []string) []edge.Kind {
	var kinds []edge.Kind
	for _, name := range names {
		f, ok := featureNamed(name)
		if !ok {
			continue
		}
		kind, needed := f.edgeKind()
		if needed && !slices.Contains(kinds, kind) {
			kinds = append(kinds, kind)
		}
	}
	return kinds
}

func Removing(features, remove []string) []string {
	var out []string
	for _, name := range remove {
		if !slices.Contains(features, name) {
			out = append(out, name)
		}
	}
	return out
}

func featureNames() []string {
	out := make([]string, 0, len(featureRegistry))
	for _, f := range featureRegistry {
		out = append(out, f.name)
	}
	return out
}

func featureLevels(names []string) ([][]string, error) {
	return providerkit.FeatureLevels(Catalogue(), names)
}

type FeatureSet map[string]bool

func (s FeatureSet) Has(name string) bool { return s[name] }

func (s FeatureSet) Names() []string {
	var out []string
	for _, name := range featureNames() {
		if s[name] {
			out = append(out, name)
		}
	}
	return out
}

const (
	paramAssetBucketName     = "AssetBucketName"
	paramAssetBucketARN      = "AssetBucketArn"
	paramStateTableName      = "StateTableName"
	paramStateTableARN       = "StateTableArn"
	paramStateTableStreamARN = "StateTableStreamArn"
	paramRevalidateQueueARN  = "RevalidateQueueArn"
	paramImageOptimizerARN   = "ImageOptimizerArn"
)

type crossStackParam struct {
	name        string
	description string
	value       string
}

func crossStack(specs []crossStackParam) (string, []cfntypes.Parameter) {
	var b strings.Builder
	b.WriteString("Parameters:\n")
	values := make([]cfntypes.Parameter, 0, len(specs))
	for _, spec := range specs {
		fmt.Fprintf(&b, "  %s:\n    Type: String\n    Description: %q\n", spec.name, spec.description)
		values = append(values, cfntypes.Parameter{
			ParameterKey:   aws.String(spec.name),
			ParameterValue: aws.String(spec.value),
		})
	}
	return b.String(), values
}
