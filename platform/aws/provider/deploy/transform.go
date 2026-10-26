package deploy

import (
	"maps"
	"slices"
	"strings"

	sdk "github.com/pulumi/pulumi/sdk/v3/go/pulumi"

	"github.com/ocelhq/ocel/pkg/provider/pulumi"
	"github.com/ocelhq/ocel/pkg/provider/transform"
	"github.com/ocelhq/ocel/pkg/refusal"
)

const (
	lambdaVPCConfigField    = "vpcConfig"
	lambdaVPCSubnetsField   = "subnetIds"
	lambdaVPCSecurityGroups = "securityGroupIds"
)

type resourceKey struct {
	Type string
	Name string
}

type transformCandidate struct {
	key   resourceKey
	names map[string][]pulumi.ResourceRef
	check func(name string, patches map[string]map[string]any) error
}

type transformPatches struct {
	patched  *pulumi.Patches
	tags     map[resourceKey]map[string]string
	vpcBound map[string]bool
	surfaces map[resourceKey]map[string]bool
	managed  map[resourceKey][]string
}

func (t *transformPatches) TagsFor(kind, name string) map[string]string {
	if t == nil {
		return nil
	}
	return t.tags[resourceKey{Type: kind, Name: name}]
}

func (t *transformPatches) placesInVPC(name string) bool {
	return t != nil && t.vpcBound[name]
}

func (t *transformPatches) Patches(kind, name, surface string) bool {
	return t != nil && t.surfaces[resourceKey{Type: kind, Name: name}][surface]
}

func (t *transformPatches) install(pctx *sdk.Context) error {
	if t == nil {
		return nil
	}
	return t.patched.Install(pctx)
}

func (t *transformPatches) refuseUnclaimed() error {
	if t == nil {
		return nil
	}
	return t.patched.RefuseUnclaimed()
}

func indexPatches(candidates []transformCandidate, results []transform.Result) (*transformPatches, error) {
	indexed := &transformPatches{
		patched:  pulumi.NewPatches(),
		tags:     map[resourceKey]map[string]string{},
		vpcBound: map[string]bool{},
		surfaces: map[resourceKey]map[string]bool{},
	}
	for i, candidate := range candidates {
		result := results[i]
		if len(result.Tags) > 0 {
			indexed.tags[candidate.key] = result.Tags
		}
		for _, key := range slices.Sorted(maps.Keys(result.Patches)) {
			patch := result.Patches[key]
			refs, constructed := candidate.names[key]
			if !constructed {
				return nil, refusal.Refuse(refusal.CodeInvalid,
					"a transform patches %s's %s, and this deploy constructs no such resource for it",
					candidate.key.Name, key)
			}
			if len(patch) == 0 {
				continue
			}
			if indexed.surfaces[candidate.key] == nil {
				indexed.surfaces[candidate.key] = map[string]bool{}
			}
			indexed.surfaces[candidate.key][key] = true
			site := candidate.key.Type + " " + candidate.key.Name + "'s " + key
			for _, ref := range refs {
				if err := indexed.patched.Add(ref, site, patch); err != nil {
					return nil, err
				}
			}
		}
		if candidate.check != nil {
			if err := candidate.check(candidate.key.Name, result.Patches); err != nil {
				return nil, err
			}
		}
		if candidate.key.Type == transformTypeFunction {
			if _, bound := result.Patches["lambda"][lambdaVPCConfigField]; bound {
				indexed.vpcBound[candidate.key.Name] = true
			}
		}
	}
	return indexed, nil
}

func checkVPCConfig(logicalName string, patch map[string]any) error {
	placed, bound := patch[lambdaVPCConfigField].(map[string]any)
	if !bound {
		return nil
	}
	subnets, groups := namedCount(placed[lambdaVPCSubnetsField]), namedCount(placed[lambdaVPCSecurityGroups])
	if subnets == 0 {
		return refusal.Refuse(refusal.CodeInvalid,
			"a transform places %s in a VPC with %d security groups and no subnets; a Lambda reaches a VPC through the subnets it is given, so name at least one",
			logicalName, groups)
	}
	if groups == 0 {
		return refusal.Refuse(refusal.CodeInvalid,
			"a transform places %s in %d subnets with no security group; a Lambda in a VPC is refused without one, so name at least one",
			logicalName, subnets)
	}
	return nil
}

func namedCount(value any) int {
	switch typed := value.(type) {
	case []any:
		return len(typed)
	case string:
		if strings.TrimSpace(typed) == "" {
			return 0
		}
		return 1
	}
	return 0
}

const (
	transformTypeFunction  = "function"
	transformTypeContainer = "container"
)
