package deploy

import (
	"context"
	"maps"
	"slices"

	sdk "github.com/pulumi/pulumi/sdk/v3/go/pulumi"

	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/providerkit"
	"github.com/ocelhq/ocel/platform/aws/provider/transform"
)

const lambdaVPCConfigField = "vpcConfig"

type resourceKey struct {
	Type string
	Name string
}

type transformCandidate struct {
	key   resourceKey
	names map[string]string
}

type transformPatches struct {
	patches  map[string]map[string]any
	tags     map[resourceKey]map[string]string
	vpcBound map[string]bool
}

func (t *transformPatches) tagsFor(kind, name string) map[string]string {
	if t == nil {
		return nil
	}
	return t.tags[resourceKey{Type: kind, Name: name}]
}

func (t *transformPatches) placesInVPC(name string) bool {
	return t != nil && t.vpcBound[name]
}

func (t *transformPatches) install(pctx *sdk.Context) error {
	if t == nil || len(t.patches) == 0 {
		return nil
	}
	return pctx.RegisterResourceTransform(func(_ context.Context, args *sdk.ResourceTransformArgs) *sdk.ResourceTransformResult {
		patch, claimed := t.patches[args.Name]
		if !claimed {
			return nil
		}
		return &sdk.ResourceTransformResult{Props: mergeProps(args.Props, patch), Opts: args.Opts}
	})
}

func mergeProps(props sdk.Map, patch map[string]any) sdk.Map {
	merged := sdk.Map{}
	maps.Copy(merged, props)
	for _, field := range slices.Sorted(maps.Keys(patch)) {
		merged[field] = mergeInput(merged[field], patch[field])
	}
	return merged
}

func mergeInput(held sdk.Input, over any) sdk.Input {
	nested, isMap := over.(map[string]any)
	if !isMap {
		return sdk.Any(over)
	}
	under, layered := held.(sdk.Map)
	if !layered {
		return sdk.Any(over)
	}
	return mergeProps(under, nested)
}

func indexPatches(candidates []transformCandidate, results []transform.Result) (*transformPatches, error) {
	held := &transformPatches{
		patches:  map[string]map[string]any{},
		tags:     map[resourceKey]map[string]string{},
		vpcBound: map[string]bool{},
	}
	for i, candidate := range candidates {
		result := results[i]
		if len(result.Tags) > 0 {
			held.tags[candidate.key] = result.Tags
		}
		for _, key := range slices.Sorted(maps.Keys(result.Patches)) {
			patch := result.Patches[key]
			name, constructed := candidate.names[key]
			if !constructed {
				return nil, providerkit.Refuse(providerkit.CodeInvalid,
					"a transform patches %s's %s, and this deploy constructs no such resource for it",
					candidate.key.Name, key)
			}
			held.patches[name] = patch
		}
		if candidate.key.Type == transformTypeFunction {
			if _, bound := result.Patches["lambda"][lambdaVPCConfigField]; bound {
				held.vpcBound[candidate.key.Name] = true
			}
		}
	}
	return held, nil
}

const (
	transformTypeFunction = "function"
	transformTypeBucket   = "bucket"
	transformTypePostgres = "postgres"
)

func functionResourceNames(project string, stack naming.StackName, logicalName string) map[string]string {
	coord := functionCoordinate(project, stack, logicalName)
	return map[string]string{
		"lambda":   coord.PhysicalName(maxLambdaBaseNameLen),
		"url":      naming.ResourceID(naming.KindFunction, coord.Name, "url"),
		"logGroup": naming.ResourceID(naming.KindFunction, coord.Name, "logs"),
	}
}

func bucketResourceNames(project, env, logicalName string, args bucketArgs) map[string]string {
	at := resourceCoordinate(project, env, logicalName, naming.KindBucket)
	names := map[string]string{
		"bucket":                  naming.ResourceID(at.Kind, at.Name),
		"publicAccessBlock":       naming.ResourceID(at.Kind, at.Name, "public-access-block"),
		"uploadCompleterRole":     naming.ResourceID(at.Kind, at.Name, uploadCompleterLocalName+"-role"),
		"uploadCompleterLogGroup": naming.ResourceID(at.Kind, at.Name, uploadCompleterLocalName+"-logs"),
		"uploadCompleter":         naming.ResourceID(at.Kind, at.Name, uploadCompleterLocalName),
		"notification":            naming.ResourceID(at.Kind, at.Name, "notification"),
	}
	if len(args.CORS.AllowedOrigins) > 0 {
		names["cors"] = naming.ResourceID(at.Kind, at.Name, "cors")
	}
	return names
}

func postgresResourceNames(project, env, logicalName string) map[string]string {
	at := resourceCoordinate(project, env, logicalName, naming.KindDatabase)
	return map[string]string{
		"securityGroup": naming.ResourceID(at.Kind, at.Name, "security-group"),
		"subnetGroup":   naming.ResourceID(at.Kind, at.Name, "subnet-group"),
		"cluster":       naming.ResourceID(at.Kind, at.Name),
		"instance":      naming.ResourceID(at.Kind, at.Name, "instance"),
	}
}
