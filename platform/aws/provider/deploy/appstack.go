package deploy

import (
	"fmt"

	iam "github.com/pulumi/pulumi-aws/sdk/v7/go/aws/iam"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"

	"github.com/ocelhq/ocel/pkg/naming"
	deploymentsv1 "github.com/ocelhq/ocel/pkg/proto/deployments/v1"
)

type appStackFunctions struct {
	Project   string
	Stack     naming.StackName
	Functions []*deploymentsv1.ManifestFunction
	Args      func(*deploymentsv1.ManifestFunction) functionArgs
	Artifacts map[string]artifactRef
	Env       map[string]string
	ISR       *isrConfig
	Bytecode  *bytecodeConfig
	Router    *routerHost
	RoleArn   pulumi.StringInput
	RoleName  pulumi.StringInput
}

func (a appStackFunctions) register(ctx *pulumi.Context) error {
	siblings := pulumi.StringMap{}
	var arns []pulumi.StringInput
	var entry *deploymentsv1.ManifestFunction
	for _, fn := range a.Functions {
		if a.Router.hosts(fn) {
			entry = fn
			continue
		}
		ref, err := a.declare(ctx, fn, a.Env, nil)
		if err != nil {
			return err
		}
		siblings[routeID(fn)] = ref.URL
		arns = append(arns, ref.ARN)
	}
	if entry == nil {
		return nil
	}
	if err := a.grantInvoke(ctx, arns); err != nil {
		return err
	}
	_, err := a.declare(ctx, entry, a.Router.entryEnv(a.Env), map[string]pulumi.StringInput{
		functionURLsEnv: siblingFunctionURLs(siblings),
	})
	return err
}

func (a appStackFunctions) grantInvoke(ctx *pulumi.Context, arns []pulumi.StringInput) error {
	optimizer := a.Router.ImageOptimizerURL != ""
	if len(arns) == 0 && !optimizer {
		return nil
	}

	parts := make([]any, 0, len(arns)+1)
	parts = append(parts, a.RoleArn)
	for _, arn := range arns {
		parts = append(parts, arn)
	}

	policy := pulumi.All(parts...).ApplyT(func(resolved []any) (string, error) {
		account := accountOfARN(fmt.Sprint(resolved[0]))
		if optimizer && account == "" {
			return "", fmt.Errorf("scope the entry function's invoke grant: %q names no account", resolved[0])
		}
		siblings := make([]string, 0, len(resolved)-1)
		for _, arn := range resolved[1:] {
			siblings = append(siblings, fmt.Sprint(arn))
		}
		return routerInvokePolicy(siblings, account, optimizer)
	}).(pulumi.StringOutput)

	_, err := iam.NewRolePolicy(ctx, naming.ResourceID(naming.KindRole, roleLocalName, "policy", "router", "invoke"), &iam.RolePolicyArgs{
		Role:   a.RoleName,
		Policy: policy,
	})
	return err
}

func (a appStackFunctions) declare(
	ctx *pulumi.Context,
	fn *deploymentsv1.ManifestFunction,
	env map[string]string,
	resolved map[string]pulumi.StringInput,
) (functionRef, error) {
	logical := fn.GetLogicalName()
	ref, err := registerFunction(ctx, logical, functionCoordinate(a.Project, a.Stack, logical),
		fn.GetRouteId(), a.Args(fn), a.Artifacts[logical], env, resolved, a.ISR, a.Bytecode, a.RoleArn)
	if err != nil {
		return ref, fmt.Errorf("declare %s: %w", logical, err)
	}
	return ref, nil
}
