package deploy

import (
	"maps"
	"slices"
	"strings"

	"github.com/ocelhq/ocel/pkg/provider/transform"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/platform/aws/provider/iam"
)

const (
	roleSurface            = "role"
	managedPolicyArnsField = "managedPolicyArns"
	arnPrefix              = "arn:"
)

func takeManagedPolicies(key resourceKey, patches transform.Patches, account string) (transform.Patches, []string, error) {
	if key.Type != transformTypeFunction && key.Type != transformTypeContainer {
		return patches, nil, nil
	}
	role := patches[roleSurface]
	listed, attaches := role[managedPolicyArnsField]
	if !attaches {
		return patches, nil, nil
	}
	arns, err := readPolicyARNs(key, listed)
	if err != nil {
		return nil, nil, err
	}
	for _, arn := range arns {
		if !iam.IsAServiceRoleOrInAccount(arn, account) {
			return nil, nil, refusal.Refuse(refusal.CodeInvalid,
				"a transform attaches %s to %s %s's role, and an app's role takes only a Lambda or ECS service-role policy or a policy of this account: attach one under %s",
				arn, key.Type, key.Name, iam.AccountPolicyARNPrefix(account))
		}
	}
	rest := maps.Clone(patches)
	rest[roleSurface] = maps.Clone(role)
	delete(rest[roleSurface], managedPolicyArnsField)
	return rest, arns, nil
}

func readPolicyARNs(key resourceKey, listed any) ([]string, error) {
	values, isList := listed.([]any)
	if !isList {
		return nil, refuseManagedPolicy(key, listed)
	}
	arns := make([]string, 0, len(values))
	for _, value := range values {
		arn, isString := value.(string)
		if !isString || !strings.HasPrefix(arn, arnPrefix) {
			return nil, refuseManagedPolicy(key, value)
		}
		arns = append(arns, arn)
	}
	return arns, nil
}

func refuseManagedPolicy(key resourceKey, value any) error {
	return refusal.Refuse(refusal.CodeInvalid,
		"a transform gives %s %s's role.%s %v, and it takes a list of policy ARNs",
		key.Type, key.Name, managedPolicyArnsField, value)
}

func (t *transformPatches) managedPoliciesOf(kind string, names ...string) []string {
	if t == nil {
		return nil
	}
	var arns []string
	for _, name := range names {
		for _, arn := range t.managed[resourceKey{Type: kind, Name: name}] {
			if !slices.Contains(arns, arn) {
				arns = append(arns, arn)
			}
		}
	}
	return arns
}
