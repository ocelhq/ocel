package deploy

import (
	"fmt"

	iam "github.com/pulumi/pulumi-aws/sdk/v7/go/aws/iam"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"

	"github.com/ocelhq/ocel/pkg/naming"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/platform/aws/provider/payloads"
)

type appFunction struct {
	Logical string
	RouteID string
}

func (f appFunction) route() string {
	if f.RouteID != "" {
		return f.RouteID
	}
	return f.Logical
}

func manifestAppFunctions(functions []*contractv1.ManifestFunction) []appFunction {
	out := make([]appFunction, 0, len(functions))
	for _, fn := range functions {
		out = append(out, appFunction{Logical: fn.GetLogicalName(), RouteID: fn.GetRouteId()})
	}
	return out
}

type appStackFunctions struct {
	Project   string
	Stack     naming.StackName
	Functions []appFunction
	Args      func(appFunction) functionArgs
	Artifacts map[string]artifactRef
	Env       map[string]string
	ISR       *isrConfig
	Bytecode  *bytecodeConfig
	Router    *routerHost
	Guard     *originGuard
	RoleArn   pulumi.StringInput
	RoleName  pulumi.StringInput
	Layers    map[string]payloads.Placement
	Shipped   map[string]pulumi.Resource
	Pushed    []pulumi.Resource
}

func (a appStackFunctions) register(ctx *pulumi.Context) error {
	membrane, err := a.membraneLayers(ctx)
	if err != nil {
		return err
	}
	siblings := pulumi.StringMap{}
	var arns []pulumi.StringInput
	var entry *appFunction
	for _, fn := range a.Functions {
		if a.Router.hosts(fn) || a.Guard.hosts(fn) {
			entry = &fn
			continue
		}
		ref, err := a.declare(ctx, fn, membrane, a.Env, nil, functionURLAuthIAM)
		if err != nil {
			return err
		}
		siblings[fn.route()] = ref.URL
		arns = append(arns, ref.ARN)
	}
	if entry == nil {
		return nil
	}
	var resolved map[string]pulumi.StringInput
	if a.Router != nil {
		if err := a.grantInvoke(ctx, arns); err != nil {
			return err
		}
		resolved = map[string]pulumi.StringInput{functionURLsEnv: siblingFunctionURLs(siblings)}
	}
	_, err = a.declare(ctx, *entry, membrane, a.Guard.entryEnv(a.Router.entryEnv(a.Env)), resolved, a.Guard.entryURLAuth())
	return err
}

func (a appStackFunctions) membraneLayers(ctx *pulumi.Context) (map[string]pulumi.StringInput, error) {
	layers := map[string]pulumi.StringInput{}
	for _, fn := range a.Functions {
		arch := a.Args(fn).Arch
		if _, published := layers[arch]; published {
			continue
		}
		code := a.Layers[arch]
		if !code.Present() {
			return nil, fmt.Errorf("this release places no %s membrane, so the functions built for it have nothing to boot through", arch)
		}
		layer, err := newMembraneLayer(ctx, membraneLayerCoordinate(a.Project, a.Stack, arch), arch, code)
		if err != nil {
			return nil, err
		}
		layers[arch] = layer.Arn
	}
	return layers, nil
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
	fn appFunction,
	membrane map[string]pulumi.StringInput,
	env map[string]string,
	resolved map[string]pulumi.StringInput,
	urlAuth string,
) (functionRef, error) {
	logical := fn.Logical
	args := a.Args(fn)
	ref, err := registerFunction(ctx, logical, functionCoordinate(a.Project, a.Stack, logical),
		fn.RouteID, args, a.Artifacts[logical], env, resolved, a.ISR, a.Bytecode, a.RoleArn, pulumi.StringArray{membrane[args.Arch]}, urlAuth,
		a.shippedTo(logical)...)
	if err != nil {
		return ref, fmt.Errorf("declare %s: %w", logical, err)
	}
	return ref, nil
}

func (a appStackFunctions) shippedTo(logical string) []pulumi.ResourceOption {
	before := append([]pulumi.Resource(nil), a.Pushed...)
	if object, shipped := a.Shipped[logical]; shipped {
		before = append(before, object)
	}
	if len(before) == 0 {
		return nil
	}
	return []pulumi.ResourceOption{pulumi.DependsOn(before)}
}
