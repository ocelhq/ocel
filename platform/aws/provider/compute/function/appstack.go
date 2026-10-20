package function

import (
	"fmt"

	awsiam "github.com/pulumi/pulumi-aws/sdk/v7/go/aws/iam"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"

	"github.com/ocelhq/ocel/pkg/naming"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/platform/aws/provider/component"
	"github.com/ocelhq/ocel/platform/aws/provider/functionurl"
	"github.com/ocelhq/ocel/platform/aws/provider/resources/variables"
	"github.com/ocelhq/ocel/platform/aws/provider/site"
)

type AppFunction struct {
	Logical string
	RouteID string

	HasWorkerEntry bool
}

func (f AppFunction) route() string {
	if f.RouteID != "" {
		return f.RouteID
	}
	return f.Logical
}

func manifestAppFunctions(functions []*contractv1.ManifestFunction) []AppFunction {
	out := make([]AppFunction, 0, len(functions))
	for _, fn := range functions {
		out = append(out, AppFunction{Logical: fn.GetLogicalName(), RouteID: fn.GetRouteId(), HasWorkerEntry: fn.GetHasWorkerEntry()})
	}
	return out
}

type AppStack struct {
	Namespace string
	Project   string
	Stack     naming.StackName
	Functions []AppFunction
	Args      func(AppFunction) Args
	Artifacts map[string]Artifact
	Env       map[string]string
	ISR       *site.ISR
	Bytecode  *Bytecode
	Dispatch  *DispatchHost
	Guard     *OriginGuard
	RoleArn   pulumi.StringInput
	RoleName  pulumi.StringInput
	KmsKeyARN string
	Layers    map[string]string
	Shipped   map[string]pulumi.Resource
	Pushed    []pulumi.Resource
}

func (a AppStack) CheckEnvBudget() error {
	for _, fn := range a.Functions {
		declared := a.Env
		if a.Dispatch.hosts(fn) {
			declared = a.Dispatch.plannedRootFunctionEnv(a.Env, a.Functions)
		}
		if a.Guard.hosts(fn) {
			declared = a.Guard.rootFunctionEnv(declared)
		}
		if err := variables.CheckFunctionEnvBudget(fn.Logical, Env(declared, a.Args(fn), a.ISR, a.Bytecode)); err != nil {
			return err
		}
	}
	return nil
}

func (a AppStack) Declare(ctx *pulumi.Context) error {
	siblings := pulumi.StringMap{}
	var arns []pulumi.StringInput
	var rootFunction *AppFunction
	for _, fn := range a.Functions {
		if a.Dispatch.hosts(fn) || a.Guard.hosts(fn) {
			rootFunction = &fn
			continue
		}
		ref, err := a.declare(ctx, fn, a.Env, nil, functionurl.AuthIAM)
		if err != nil {
			return err
		}
		siblings[fn.route()] = ref.URL
		arns = append(arns, ref.ARN)
	}
	if rootFunction == nil {
		return nil
	}
	var resolved map[string]pulumi.StringInput
	if a.Dispatch != nil {
		if err := a.grantInvoke(ctx, arns); err != nil {
			return err
		}
		resolved = map[string]pulumi.StringInput{functionURLsEnv: siblingFunctionURLs(siblings)}
	}
	_, err := a.declare(ctx, *rootFunction, a.Guard.rootFunctionEnv(a.Dispatch.rootFunctionEnv(a.Env)), resolved, a.Guard.rootFunctionURLAuth())
	return err
}

func (a AppStack) grantInvoke(ctx *pulumi.Context, arns []pulumi.StringInput) error {
	optimizer := a.Dispatch.ImageOptimizerURL != ""
	if len(arns) == 0 && !optimizer {
		return nil
	}

	parts := make([]any, 0, len(arns)+1)
	parts = append(parts, a.RoleArn)
	for _, arn := range arns {
		parts = append(parts, arn)
	}

	policy := pulumi.All(parts...).ApplyT(func(resolved []any) (string, error) {
		account := component.AccountOfARN(fmt.Sprint(resolved[0]))
		if optimizer && account == "" {
			return "", fmt.Errorf("scope the root function's invoke grant: %q names no account", resolved[0])
		}
		siblings := make([]string, 0, len(resolved)-1)
		for _, arn := range resolved[1:] {
			siblings = append(siblings, fmt.Sprint(arn))
		}
		return renderDispatchInvokePolicy(siblings, account, optimizer)
	}).(pulumi.StringOutput)

	role, err := RoleName(a.Namespace, component.AppRoleCoordinate(a.Project, a.Stack))
	if err != nil {
		return err
	}
	_, err = awsiam.NewRolePolicy(ctx, role.Part(dispatchPolicyName), &awsiam.RolePolicyArgs{
		Name:   pulumi.String(dispatchPolicyName),
		Role:   a.RoleName,
		Policy: policy,
	})
	return err
}

func (a AppStack) declare(
	ctx *pulumi.Context,
	fn AppFunction,
	env map[string]string,
	resolved map[string]pulumi.StringInput,
	urlAuth string,
) (functionRef, error) {
	logical := fn.Logical
	args := a.Args(fn)
	name, err := Name(a.Namespace, a.Project, a.Stack, logical)
	if err != nil {
		return functionRef{}, err
	}
	ref, err := registerFunction(ctx, logical, Coordinate(a.Project, a.Stack, logical), name,
		fn.RouteID, args, a.Artifacts[logical], env, resolved, a.ISR, a.Bytecode, a.RoleArn,
		RuntimeLayerInput(a.Layers[args.Arch]), urlAuth, a.KmsKeyARN,
		a.shippedTo(logical)...)
	if err != nil {
		return ref, fmt.Errorf("declare %s: %w", logical, err)
	}
	return ref, nil
}

func (a AppStack) shippedTo(logical string) []pulumi.ResourceOption {
	before := append([]pulumi.Resource(nil), a.Pushed...)
	if object, shipped := a.Shipped[logical]; shipped {
		before = append(before, object)
	}
	if len(before) == 0 {
		return nil
	}
	return []pulumi.ResourceOption{pulumi.DependsOn(before)}
}
