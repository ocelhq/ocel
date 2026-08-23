package deploy

import (
	"context"
	"fmt"
	"runtime/debug"
	"strconv"
	"time"

	ec2 "github.com/pulumi/pulumi-aws/sdk/v7/go/aws/ec2"
	"github.com/pulumi/pulumi/sdk/v3/go/auto"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"

	"github.com/ocelhq/ocel/pkg/naming"
	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"
	linksv1 "github.com/ocelhq/ocel/pkg/proto/common/links/v1"
	progressv1 "github.com/ocelhq/ocel/pkg/proto/common/progress/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/providerkit"
)

func runInfraStack(ctx context.Context, cfg Config, in stackInputs, manifest *contractv1.Manifest, plan Plan, log func(string)) ([]*linksv1.Link, error) {
	program := func(pctx *pulumi.Context) error {
		vpc, err := ec2.LookupVpc(pctx, &ec2.LookupVpcArgs{Default: pulumi.BoolRef(true)})
		if err != nil {
			return fmt.Errorf("look up default VPC: %w", err)
		}
		subnets, err := ec2.GetSubnets(pctx, &ec2.GetSubnetsArgs{
			Filters: []ec2.GetSubnetsFilter{{Name: "vpc-id", Values: []string{vpc.Id}}},
		})
		if err != nil {
			return fmt.Errorf("look up default VPC subnets: %w", err)
		}
		project, env := naming.Sanitize(manifest.GetSlug()), plan.InfraStack.Env
		for _, r := range manifest.GetResources() {
			if r.GetLinked() {
				continue
			}
			var err error
			switch {
			case r.GetPostgres() != nil:
				err = registerPostgres(pctx, project, env, r.GetLogicalName(), in.transformed.forPostgres(r.GetLogicalName(), r.GetPostgres()), vpc.Id, vpc.CidrBlock, subnets.Ids)
			case r.GetBucket() != nil:
				err = registerBucket(pctx, project, env, r.GetLogicalName(), in.transformed.forBucket(r.GetLogicalName(), r.GetBucket()), cfg.StateTable, cfg.AppBoundaryARN, in.sessions, in.completer)
			default:
				continue
			}
			if err != nil {
				return fmt.Errorf("declare %s: %w", r.GetLogicalName(), err)
			}
		}
		return nil
	}

	if err := refuseHandover(ctx, in.release, refFor(cfg, plan.InfraStack), manifest); err != nil {
		return nil, err
	}

	outputs, err := upStack(ctx, cfg, in, plan.InfraStack, infraStackTags(cfg, plan.InfraStack), program, log, cfg.Stages.Provisioning.ID)
	if err != nil {
		return nil, fmt.Errorf("provision infra stack %s: %w", plan.InfraStack, err)
	}
	return collectLinks(ctx, cfg.Secrets, in.sessions, manifest, outputs)
}

func refuseHandover(ctx context.Context, release *Releaser, ref providerkit.StackRef, manifest *contractv1.Manifest) error {
	if len(linkedResources(manifest)) == 0 {
		return nil
	}
	outputs, err := release.Outputs(ctx, ref, nil)
	if err != nil {
		return err
	}
	provisioned := make(map[string]bool, len(outputs))
	for logical := range outputs {
		provisioned[logical] = true
	}
	return handedOver(manifest, provisioned, ref.Name.String())
}

func runAppStack(ctx context.Context, cfg Config, in stackInputs, manifest *contractv1.Manifest, plan Plan, app *contractv1.ManifestApp, id Identity, baked appBundle, builds appBuilds, links []*linksv1.Link, stage Stage, log func(string)) (outs []*progressv1.FunctionOutput, names map[string]string, err error) {
	start := time.Now()
	defer func() { spanForStage(cfg.Tracer, stage, start, time.Now(), err) }()

	name := app.GetName()
	functions := appFunctions(manifest, name)
	caches := builds.caches
	bytecode := builds.bytecode

	if err = checkRuntimeOwnedNames(app); err != nil {
		return nil, nil, err
	}
	if err = checkAppEdgeVariables(cfg, app, baked); err != nil {
		return nil, nil, err
	}

	env := appEnv(manifest, app, baked, cfg, in.sessions)
	router := builds.routers[name]
	guard := builds.guards[name]

	for _, fn := range functions {
		declared := env
		if router.hosts(fn) {
			declared = router.plannedEntryEnv(env, functions)
		}
		if guard.hosts(fn) {
			declared = guard.entryEnv(declared)
		}
		if err = checkFunctionEnvBudget(fn.GetLogicalName(), functionEnv(declared, in.transformed.forFunction(fn), caches[name], bytecode[name])); err != nil {
			return nil, nil, err
		}
	}

	policies, err := appLinkPolicies(manifest, name, links)
	if err != nil {
		return nil, nil, err
	}

	stack := plan.AppStacks[name]
	project := naming.Sanitize(manifest.GetSlug())

	cfg.reportStage(stage)(fmt.Sprintf("Provisioning %s", name))

	var roleTags map[string]string
	if len(functions) > 0 {
		roleTags = in.transformed.forFunction(functions[0]).Tags
	}
	vpcAccess := false
	for _, fn := range functions {
		vpcAccess = vpcAccess || in.transformed.forFunction(fn).VPC.placed()
	}

	program := func(pctx *pulumi.Context) error {
		role, err := newFunctionRole(pctx, roleCoordinate(project, stack), appExecutionRole(cfg, name, caches, bytecode, baked, roleTags, policies, vpcAccess, router))
		if err != nil {
			return err
		}
		return appStackFunctions{
			Project:   project,
			Stack:     stack,
			Functions: functions,
			Args:      in.transformed.forFunction,
			Artifacts: in.artifacts,
			Env:       env,
			ISR:       caches[name],
			Bytecode:  bytecode[name],
			Router:    router,
			Guard:     guard,
			RoleArn:   role.Arn,
			RoleName:  role.Name,
			Layer:     in.layer,
		}.register(pctx)
	}

	outputs, upErr := upStack(ctx, cfg, in, stack, stackTags(cfg, stack, plan.Promotion.PromotionID, id.DeploymentID(), builds.ids[name]), program, log, stage.ID)
	if upErr != nil {
		err = fmt.Errorf("provision app-deploy stack %s: %w", stack, upErr)
		return nil, nil, err
	}
	outs, names, err = collectAppFunctionOutputs(functions, outputs)
	return outs, names, err
}

func appFunctions(manifest *contractv1.Manifest, app string) []*contractv1.ManifestFunction {
	var fns []*contractv1.ManifestFunction
	for _, fn := range manifest.GetFunctions() {
		if fn.GetApp() == app {
			fns = append(fns, fn)
		}
	}
	return fns
}

func infraStackTags(cfg Config, name naming.StackName) map[string]string {
	return stackTags(cfg, name, "", "", "")
}

func stackTags(cfg Config, name naming.StackName, promotionID, deploymentID, buildID string) map[string]string {
	coord := naming.Coordinate{
		Project: naming.Sanitize(cfg.Slug),
		Env:     name.Env,
		App:     name.App,
		Release: name.Release,
	}
	return coord.Tags(naming.Facts{
		ManagedBy:  managedBy(),
		EnvClass:   envClass(cfg.Tier),
		BuildID:    buildID,
		Deployment: deploymentID,
		Promotion:  promotionID,
		ExpiresAt:  expiresAt(cfg.ExpiresAt),
	})
}

func managedBy() string {
	version := "dev"
	if info, ok := debug.ReadBuildInfo(); ok && info.Main.Version != "" && info.Main.Version != "(devel)" {
		version = info.Main.Version
	}
	return "ocel-cli/" + version
}

func envClass(tier environmentv1.Tier) string {
	if tier == environmentv1.Tier_TIER_PREVIEW {
		return "preview"
	}
	return "production"
}

func expiresAt(unix int64) string {
	if unix == 0 {
		return ""
	}
	return strconv.FormatInt(unix, 10)
}

func refFor(cfg Config, name naming.StackName) providerkit.StackRef {
	return providerkit.StackRef{Project: naming.Sanitize(cfg.Slug), Class: classOf(cfg.Tier), Name: name}
}

func classOf(tier environmentv1.Tier) providerkit.Class {
	if tier == environmentv1.Tier_TIER_PREVIEW {
		return providerkit.ClassPreview
	}
	return providerkit.ClassProduction
}

func upStack(ctx context.Context, cfg Config, in stackInputs, name naming.StackName, tags map[string]string, program pulumi.RunFunc, log func(string), parentStage StageID) (auto.OutputMap, error) {
	report := reporterFor(cfg.Tracer, parentStage, nil, log)
	return in.release.up(ctx, refFor(cfg, name), tags, program, report)
}

func collectLinks(ctx context.Context, secrets SecretsReader, sessions sessionScope, manifest *contractv1.Manifest, outputs auto.OutputMap) ([]*linksv1.Link, error) {
	var result []*linksv1.Link
	for _, r := range manifest.GetResources() {
		if r.GetLinked() || (r.GetPostgres() == nil && r.GetBucket() == nil) {
			continue
		}
		name := r.GetLogicalName()
		raw, ok := outputs[name]
		if !ok {
			return nil, fmt.Errorf("stack produced no output for %s", name)
		}
		fields, ok := raw.Value.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("output for %s is not a map", name)
		}
		var (
			out *linksv1.Link
			err error
		)
		switch {
		case r.GetPostgres() != nil:
			out, err = collectPostgresLink(ctx, secrets, name, fields)
		case r.GetBucket() != nil:
			out, err = collectBucketLink(name, sessions, fields)
		}
		if err != nil {
			return nil, err
		}
		result = append(result, out)
	}
	return result, nil
}

func collectAppFunctionOutputs(functions []*contractv1.ManifestFunction, outputs auto.OutputMap) ([]*progressv1.FunctionOutput, map[string]string, error) {
	var result []*progressv1.FunctionOutput
	names := make(map[string]string, len(functions))
	for _, fn := range functions {
		name := fn.GetLogicalName()
		raw, ok := outputs[name]
		if !ok {
			return nil, nil, fmt.Errorf("stack produced no output for %s", name)
		}
		fields, ok := raw.Value.(map[string]any)
		if !ok {
			return nil, nil, fmt.Errorf("output for %s is not a map", name)
		}
		url, err := requireStringField(fields, name, outputKeyFunctionURL)
		if err != nil {
			return nil, nil, err
		}
		if physical, ok := fields[outputKeyFunctionName].(string); ok && physical != "" {
			names[name] = physical
		}
		result = append(result, collectFunctionOutput(name, url))
	}
	return result, names, nil
}
