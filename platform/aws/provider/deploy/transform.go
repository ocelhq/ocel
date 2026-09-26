package deploy

import (
	"context"
	"maps"
	"slices"
	"strings"
	"sync"

	sdk "github.com/pulumi/pulumi/sdk/v3/go/pulumi"

	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/providerkit/refusal"
	"github.com/ocelhq/ocel/pkg/transformkit"
)

const (
	lambdaVPCConfigField    = "vpcConfig"
	lambdaVPCSubnetsField   = "subnetIds"
	lambdaVPCSecurityGroups = "securityGroupIds"

	tokenLambdaFunction    = "aws:lambda/function:Function"
	tokenLambdaFunctionURL = "aws:lambda/functionUrl:FunctionUrl"
	tokenLambdaPermission  = "aws:lambda/permission:Permission"
	tokenLogGroup          = "aws:cloudwatch/logGroup:LogGroup"
	tokenIAMRole           = "aws:iam/role:Role"
	tokenIAMRolePolicy     = "aws:iam/rolePolicy:RolePolicy"
	tokenIAMRoleAttachment = "aws:iam/rolePolicyAttachment:RolePolicyAttachment"
	tokenS3Bucket          = "aws:s3/bucketV2:BucketV2"
	tokenS3PublicAccess    = "aws:s3/bucketPublicAccessBlock:BucketPublicAccessBlock"
	tokenS3CORS            = "aws:s3/bucketCorsConfigurationV2:BucketCorsConfigurationV2"
	tokenS3Notification    = "aws:s3/bucketNotification:BucketNotification"
	tokenEC2SecurityGroup  = "aws:ec2/securityGroup:SecurityGroup"
	tokenRDSSubnetGroup    = "aws:rds/subnetGroup:SubnetGroup"
	tokenRDSCluster        = "aws:rds/cluster:Cluster"
	tokenRDSClusterMember  = "aws:rds/clusterInstance:ClusterInstance"
	tokenECSTaskDefinition = "aws:ecs/taskDefinition:TaskDefinition"
	tokenECSService        = "aws:ecs/service:Service"
	tokenLBTargetGroup     = "aws:lb/targetGroup:TargetGroup"
	tokenLBListenerRule    = "aws:lb/listenerRule:ListenerRule"
)

type resourceKey struct {
	Type string
	Name string
}

type resourceRef struct {
	Token string
	Name  string
}

type transformCandidate struct {
	key   resourceKey
	names map[string]resourceRef
}

type transformPatches struct {
	patches  map[resourceRef]map[string]any
	sites    map[resourceRef]string
	tags     map[resourceKey]map[string]string
	vpcBound map[string]bool
	corsBind map[string]bool

	mu      sync.Mutex
	claimed map[resourceRef]bool
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

func (t *transformPatches) opensCORS(name string) bool {
	return t != nil && t.corsBind[name]
}

func (t *transformPatches) install(pctx *sdk.Context) error {
	if t == nil || len(t.patches) == 0 {
		return nil
	}
	t.mu.Lock()
	t.claimed = make(map[resourceRef]bool, len(t.patches))
	t.mu.Unlock()
	return pctx.RegisterResourceTransform(func(_ context.Context, args *sdk.ResourceTransformArgs) *sdk.ResourceTransformResult {
		merged, claimed := t.claim(resourceRef{Token: args.Type, Name: args.Name}, args.Props)
		if !claimed {
			return nil
		}
		return &sdk.ResourceTransformResult{Props: merged, Opts: args.Opts}
	})
}

func (t *transformPatches) claim(ref resourceRef, props sdk.Map) (sdk.Map, bool) {
	t.mu.Lock()
	patch, patched := t.patches[ref]
	if patched {
		t.claimed[ref] = true
	}
	t.mu.Unlock()
	if !patched {
		return nil, false
	}
	return mergeProps(props, patch), true
}

func (t *transformPatches) refuseUnclaimed() error {
	if t == nil || len(t.patches) == 0 {
		return nil
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	var missed []string
	for ref := range t.patches {
		if !t.claimed[ref] {
			missed = append(missed, t.sites[ref])
		}
	}
	if len(missed) == 0 {
		return nil
	}
	slices.Sort(missed)
	return refusal.Refuse(refusal.CodeInvalid,
		"a transform patches %s, and this deploy provisioned nothing to apply it to; a patch that reaches no resource is a patch this deploy never honoured",
		strings.Join(missed, ", "))
}

func mergeProps(props sdk.Map, patch map[string]any) sdk.Map {
	merged := sdk.Map{}
	maps.Copy(merged, props)
	for _, field := range slices.Sorted(maps.Keys(patch)) {
		merged[field] = mergeInput(merged[field], patch[field])
	}
	return merged
}

func mergeInput(current sdk.Input, over any) sdk.Input {
	nested, isMap := over.(map[string]any)
	if !isMap {
		return sdk.Any(over)
	}
	under, layered := current.(sdk.Map)
	if !layered {
		return sdk.Any(over)
	}
	return mergeProps(under, nested)
}

func indexPatches(candidates []transformCandidate, results []transformkit.Result) (*transformPatches, error) {
	indexed := &transformPatches{
		patches:  map[resourceRef]map[string]any{},
		sites:    map[resourceRef]string{},
		tags:     map[resourceKey]map[string]string{},
		vpcBound: map[string]bool{},
		corsBind: map[string]bool{},
	}
	for i, candidate := range candidates {
		result := results[i]
		if len(result.Tags) > 0 {
			indexed.tags[candidate.key] = result.Tags
		}
		for _, key := range slices.Sorted(maps.Keys(result.Patches)) {
			patch := result.Patches[key]
			ref, constructed := candidate.names[key]
			if !constructed {
				return nil, refusal.Refuse(refusal.CodeInvalid,
					"a transform patches %s's %s, and this deploy constructs no such resource for it",
					candidate.key.Name, key)
			}
			if len(patch) == 0 {
				continue
			}
			site := candidate.key.Type + " " + candidate.key.Name + "'s " + key
			if earlier, taken := indexed.patches[ref]; taken {
				merged, err := mergeClaims(earlier, patch, indexed.sites[ref], site)
				if err != nil {
					return nil, err
				}
				patch = merged
			}
			indexed.patches[ref] = patch
			indexed.sites[ref] = site
		}
		switch candidate.key.Type {
		case transformTypeFunction:
			if err := checkVPCConfig(candidate.key.Name, result.Patches["lambda"]); err != nil {
				return nil, err
			}
			if _, bound := result.Patches["lambda"][lambdaVPCConfigField]; bound {
				indexed.vpcBound[candidate.key.Name] = true
			}
		case transformTypeBucket:
			if len(result.Patches["cors"]) > 0 {
				indexed.corsBind[candidate.key.Name] = true
			}
		}
	}
	return indexed, nil
}

func mergeClaims(earlier, over map[string]any, at, also string) (map[string]any, error) {
	merged := map[string]any{}
	maps.Copy(merged, earlier)
	for _, field := range slices.Sorted(maps.Keys(over)) {
		value := over[field]
		if seen, taken := merged[field]; taken && !sameValue(seen, value) {
			return nil, refusal.Refuse(refusal.CodeInvalid,
				"%s and %s are the same underlying resource, and the transforms give its %s two different values; ocel shares it between them, so patch it once",
				at, also, field)
		}
		merged[field] = value
	}
	return merged, nil
}

func sameValue(a, b any) bool {
	switch left := a.(type) {
	case map[string]any:
		right, ok := b.(map[string]any)
		return ok && maps.EqualFunc(left, right, sameValue)
	case []any:
		right, ok := b.([]any)
		return ok && slices.EqualFunc(left, right, sameValue)
	}
	return a == b
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
	transformTypeBucket    = "bucket"
	transformTypePostgres  = "postgres"
)

func functionResourceNames(project string, stack naming.StackName, logicalName string) map[string]resourceRef {
	coord := functionCoordinate(project, stack, logicalName)
	return map[string]resourceRef{
		"lambda":        {Token: tokenLambdaFunction, Name: coord.PhysicalName(maxLambdaBaseNameLen)},
		"url":           {Token: tokenLambdaFunctionURL, Name: naming.ResourceID(naming.KindFunction, coord.Name, "url")},
		"urlPermission": {Token: tokenLambdaPermission, Name: naming.ResourceID(naming.KindFunction, coord.Name, "url", "invoke")},
		"logGroup":      {Token: tokenLogGroup, Name: naming.ResourceID(naming.KindFunction, coord.Name, "logs")},
		"role":          {Token: tokenIAMRole, Name: naming.ResourceID(naming.KindRole, roleLocalName)},
	}
}

func containerResourceNames() map[string]resourceRef {
	return map[string]resourceRef{
		"task":        {Token: tokenECSTaskDefinition, Name: naming.ResourceID(naming.KindService, containerLocalName, "task")},
		"service":     {Token: tokenECSService, Name: naming.ResourceID(naming.KindService, containerLocalName)},
		"targetGroup": {Token: tokenLBTargetGroup, Name: naming.ResourceID(naming.KindService, containerLocalName, "targets")},
		"rule":        {Token: tokenLBListenerRule, Name: naming.ResourceID(naming.KindService, containerLocalName, "rule")},
		"role":        {Token: tokenIAMRole, Name: naming.ResourceID(naming.KindRole, roleLocalName)},
	}
}

func bucketResourceNames(project, env, logicalName string) map[string]resourceRef {
	at := resourceCoordinate(project, env, logicalName, naming.KindBucket)
	completerRole := naming.ResourceID(at.Kind, at.Name, uploadCompleterLocalName+"-role")
	return map[string]resourceRef{
		"bucket":                        {Token: tokenS3Bucket, Name: naming.ResourceID(at.Kind, at.Name)},
		"publicAccessBlock":             {Token: tokenS3PublicAccess, Name: naming.ResourceID(at.Kind, at.Name, "public-access-block")},
		"cors":                          {Token: tokenS3CORS, Name: naming.ResourceID(at.Kind, at.Name, "cors")},
		"uploadCompleterRole":           {Token: tokenIAMRole, Name: completerRole},
		"uploadCompleterS3Policy":       {Token: tokenIAMRolePolicy, Name: completerRole + "-policy-s3"},
		"uploadCompleterSessionsPolicy": {Token: tokenIAMRolePolicy, Name: completerRole + "-policy-sessions"},
		"uploadCompleterLogsPolicy":     {Token: tokenIAMRoleAttachment, Name: naming.ResourceID(at.Kind, at.Name, uploadCompleterLocalName+"-logs-policy")},
		"uploadCompleterLogGroup":       {Token: tokenLogGroup, Name: naming.ResourceID(at.Kind, at.Name, uploadCompleterLocalName+"-logs")},
		"uploadCompleter":               {Token: tokenLambdaFunction, Name: naming.ResourceID(at.Kind, at.Name, uploadCompleterLocalName)},
		"uploadCompleterPermission":     {Token: tokenLambdaPermission, Name: naming.ResourceID(at.Kind, at.Name, uploadCompleterLocalName+"-permission")},
		"notification":                  {Token: tokenS3Notification, Name: naming.ResourceID(at.Kind, at.Name, "notification")},
	}
}

func postgresResourceNames(project, env, logicalName string) map[string]resourceRef {
	at := resourceCoordinate(project, env, logicalName, naming.KindDatabase)
	return map[string]resourceRef{
		"securityGroup": {Token: tokenEC2SecurityGroup, Name: naming.ResourceID(at.Kind, at.Name, "security-group")},
		"subnetGroup":   {Token: tokenRDSSubnetGroup, Name: naming.ResourceID(at.Kind, at.Name, "subnet-group")},
		"cluster":       {Token: tokenRDSCluster, Name: naming.ResourceID(at.Kind, at.Name)},
		"instance":      {Token: tokenRDSClusterMember, Name: naming.ResourceID(at.Kind, at.Name, "instance")},
	}
}
