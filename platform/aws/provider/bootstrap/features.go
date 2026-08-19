package bootstrap

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	cfntypes "github.com/aws/aws-sdk-go-v2/service/cloudformation/types"
)

const (
	FeatureISR               = "isr"
	FeatureImageOptimization = "image-optimization"
	FeatureCloudflareEdge    = "cloudflare-edge"
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
	version   int
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

	template func(featureInputs) featureStack
	payloads func(context.Context, ObjectStore, string) (stackPayloads, error)
	before   func(context.Context, stepDeps) error
	after    func(context.Context, stepDeps) error
	drop     func(context.Context, stepDeps) error
}

func (f feature) stackName(class string) string {
	if class == ClassPreview {
		return StackName + "-" + f.name + "-preview"
	}
	return StackName + "-" + f.name
}

var featureRegistry = []feature{isrFeature, imageOptimizationFeature, cloudflareEdgeFeature}

type FeatureInfo struct {
	Name      string
	Summary   string
	DependsOn []string
}

func Catalogue() []FeatureInfo {
	out := make([]FeatureInfo, 0, len(featureRegistry))
	for _, f := range featureRegistry {
		out = append(out, FeatureInfo{Name: f.name, Summary: f.summary, DependsOn: slices.Clone(f.dependsOn)})
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

func featureNames() []string {
	out := make([]string, 0, len(featureRegistry))
	for _, f := range featureRegistry {
		out = append(out, f.name)
	}
	return out
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

func (s FeatureSet) Missing(required []string) []string {
	var out []string
	for _, name := range required {
		if !s[name] && !slices.Contains(out, name) {
			out = append(out, name)
		}
	}
	slices.Sort(out)
	return out
}

func resolveFeatures(names []string) ([]string, error) {
	wanted := map[string]bool{}
	var pull func(name string, from []string) error
	pull = func(name string, from []string) error {
		f, ok := featureNamed(name)
		if !ok {
			return unknownFeature(name, from)
		}
		if wanted[name] {
			return nil
		}
		wanted[name] = true
		for _, dep := range f.dependsOn {
			if err := pull(dep, []string{name}); err != nil {
				return err
			}
		}
		return nil
	}
	for _, name := range names {
		if err := pull(name, nil); err != nil {
			return nil, err
		}
	}
	return FeatureSet(wanted).Names(), nil
}

func unknownFeature(name string, from []string) error {
	if len(from) > 0 {
		return fmt.Errorf("%s depends on %q, which this provider does not offer; it offers %s", from[0], name, strings.Join(featureNames(), ", "))
	}
	return fmt.Errorf("this provider has no bootstrap feature named %q; it offers %s", name, strings.Join(featureNames(), ", "))
}

func featureLevels(names []string) ([][]string, error) {
	pending := map[string]bool{}
	for _, name := range names {
		pending[name] = true
	}
	var levels [][]string
	placed := map[string]bool{}
	for len(pending) > 0 {
		var level []string
		for _, f := range featureRegistry {
			if !pending[f.name] {
				continue
			}
			ready := true
			for _, dep := range f.dependsOn {
				if pending[dep] && !placed[dep] {
					ready = false
				}
			}
			if ready {
				level = append(level, f.name)
			}
		}
		if len(level) == 0 {
			return nil, fmt.Errorf("no order stands %s up: each waits on another in the set", strings.Join(FeatureSet(pending).Names(), ", "))
		}
		for _, name := range level {
			delete(pending, name)
			placed[name] = true
		}
		levels = append(levels, level)
	}
	return levels, nil
}

func featureDiff(deployed FeatureSet, requested []string) (add, drop []string) {
	for _, name := range featureNames() {
		switch {
		case slices.Contains(requested, name) && !deployed.Has(name):
			add = append(add, name)
		case !slices.Contains(requested, name) && deployed.Has(name):
			drop = append(drop, name)
		}
	}
	return add, drop
}

func dropClosure(drop []string, present FeatureSet) []string {
	doomed := map[string]bool{}
	for _, name := range drop {
		doomed[name] = true
	}
	for grew := true; grew; {
		grew = false
		for _, f := range featureRegistry {
			if doomed[f.name] || !present.Has(f.name) {
				continue
			}
			for _, dep := range f.dependsOn {
				if doomed[dep] {
					doomed[f.name], grew = true, true
				}
			}
		}
	}
	return FeatureSet(doomed).Names()
}

func ProjectsNeeding(recorded map[string][]string, feature string) []string {
	return projectsDependingOn(recorded, []string{feature})
}

func projectsDependingOn(recorded map[string][]string, dropped []string) []string {
	var out []string
	for project, features := range recorded {
		for _, name := range features {
			if slices.Contains(dropped, name) {
				out = append(out, project)
				break
			}
		}
	}
	slices.Sort(out)
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

func bootstrapVersionOutput(version int) string {
	return fmt.Sprintf(`  %s:
    Description: "Schema version of the bootstrap this stack belongs to. The CLI refuses to act while its required version and this one disagree, and points at the side that has to move."
    Value: '%d'
`, outputVersion, version)
}
