package account

import (
	"context"
	"slices"

	"github.com/aws/aws-sdk-go-v2/aws"
	cfntypes "github.com/aws/aws-sdk-go-v2/service/cloudformation/types"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/progress"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/platform/aws/provider/cfn"
)

const CloudflareEdgePart = "cloudflare-edge"

type stackRefs struct {
	edgeBucketARN       string
	bucketTag           string
	stateTable          string
	stateTableARN       string
	stateTableStreamARN string
}

type partInputs struct {
	ns           Namespace
	tier         environment.Tier
	refs         stackRefs
	alongside    PartSet
	variablesKey string
}

type partStack struct {
	template cfn.Template
	body     string
	params   []cfntypes.Parameter
}

func newPartStack(template cfn.Template, params []cfntypes.Parameter) (partStack, error) {
	body, err := template.Body()
	if err != nil {
		return partStack{}, err
	}
	return partStack{template: template, body: body, params: params}, nil
}

type stepDeps struct {
	ns       Namespace
	tier     environment.Tier
	ssm      SSMAPI
	iam      IAMAPI
	progress progress.Log
}

type part struct {
	name    string
	summary string
	edges   []edge.Kind

	template  func(partInputs) (partStack, error)
	after     func(context.Context, stepDeps) error
	afterPlan func(context.Context, ParamAPIs, Namespace, environment.Tier, Request) ([]provider.Change, error)
	drop      func(context.Context, stepDeps) error
	dropPlan  func(context.Context, ParamAPIs, Namespace, environment.Tier, Request) ([]provider.Change, error)
}

func (f part) edgeKind() (edge.Kind, bool) {
	if len(f.edges) == 0 {
		return edge.None, false
	}
	return f.edges[0], true
}

func (f part) stackName(ns Namespace, tier environment.Tier) string {
	return ns.partStackName(f.name, tier)
}

var partRegistry = []part{
	cloudflareEdgePart,
	variablesKeyPart,
}

func Parts() []provider.Part {
	out := make([]provider.Part, 0, len(partRegistry))
	for _, f := range partRegistry {
		out = append(out, provider.Part{
			Name:    f.name,
			Summary: f.summary,
			Edges:   slices.Clone(f.edges),
		})
	}
	return out
}

func partNamed(name string) (part, bool) {
	for _, f := range partRegistry {
		if f.name == name {
			return f, true
		}
	}
	return part{}, false
}

func edgeKinds() []edge.Kind { return EdgeKindsFor(partNames()) }

func EdgeKindsFor(names []string) []edge.Kind {
	var kinds []edge.Kind
	for _, name := range names {
		f, ok := partNamed(name)
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

func Removing(parts, remove []string) []string {
	var out []string
	for _, name := range remove {
		if !slices.Contains(parts, name) {
			out = append(out, name)
		}
	}
	return out
}

func partNames() []string {
	out := make([]string, 0, len(partRegistry))
	for _, f := range partRegistry {
		out = append(out, f.name)
	}
	return out
}

type PartSet map[string]bool

func (s PartSet) Has(name string) bool { return s[name] }

func (s PartSet) Names() []string {
	var out []string
	for _, name := range partNames() {
		if s[name] {
			out = append(out, name)
		}
	}
	return out
}

const (
	paramEdgeBucketARN       = "EdgeBucketArn"
	paramStateTableName      = "StateTableName"
	paramStateTableARN       = "StateTableArn"
	paramStateTableStreamARN = "StateTableStreamArn"
)

type crossStackParam struct {
	name        string
	description string
	value       string
}

func crossStack(specs []crossStackParam) ([]cfn.Parameter, []cfntypes.Parameter) {
	declared := make([]cfn.Parameter, 0, len(specs))
	values := make([]cfntypes.Parameter, 0, len(specs))
	for _, spec := range specs {
		declared = append(declared, cfn.Parameter{Name: spec.name, Type: "String", Description: spec.description})
		values = append(values, cfntypes.Parameter{
			ParameterKey:   aws.String(spec.name),
			ParameterValue: aws.String(spec.value),
		})
	}
	return declared, values
}
