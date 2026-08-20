package deploy

import (
	"context"
	"encoding/json"
	"fmt"
	"runtime/debug"
	"strconv"
	"time"

	ec2 "github.com/pulumi/pulumi-aws/sdk/v7/go/aws/ec2"
	"github.com/pulumi/pulumi/sdk/v3/go/auto"
	"github.com/pulumi/pulumi/sdk/v3/go/auto/events"
	"github.com/pulumi/pulumi/sdk/v3/go/auto/optup"
	"github.com/pulumi/pulumi/sdk/v3/go/common/resource/config"
	"github.com/pulumi/pulumi/sdk/v3/go/common/workspace"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"

	"github.com/ocelhq/ocel/pkg/naming"
	deploymentsv1 "github.com/ocelhq/ocel/pkg/proto/deployments/v1"
	environmentv1 "github.com/ocelhq/ocel/pkg/proto/environment/v1"
	linksv1 "github.com/ocelhq/ocel/pkg/proto/links/v1"
	progressv1 "github.com/ocelhq/ocel/pkg/proto/progress/v1"
)

func runInfraStack(ctx context.Context, cfg Config, in stackInputs, manifest *deploymentsv1.Manifest, plan Plan, log func(string)) ([]*linksv1.Link, error) {
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
				err = registerBucket(pctx, project, env, r.GetLogicalName(), in.transformed.forBucket(r.GetLogicalName(), r.GetBucket()), cfg.StateTable, in.sessions, in.completer)
			default:
				continue
			}
			if err != nil {
				return fmt.Errorf("declare %s: %w", r.GetLogicalName(), err)
			}
		}
		return nil
	}

	stack, err := prepareStack(ctx, cfg, in.realized, plan.InfraStack, infraStackTags(cfg, plan.InfraStack), program)
	if err != nil {
		return nil, fmt.Errorf("provision infra stack %s: %w", plan.InfraStack, err)
	}
	if err := refuseHandover(ctx, stack, manifest, plan.InfraStack); err != nil {
		return nil, err
	}

	res, err := runStack(ctx, cfg, stack, log, cfg.Stages.Provisioning.ID)
	if err != nil {
		return nil, fmt.Errorf("provision infra stack %s: %w", plan.InfraStack, err)
	}
	return collectLinks(ctx, cfg.Secrets, in.sessions, manifest, res.Outputs)
}

func refuseHandover(ctx context.Context, stack auto.Stack, manifest *deploymentsv1.Manifest, name naming.StackName) error {
	if len(linkedResources(manifest)) == 0 {
		return nil
	}
	outputs, err := stack.Outputs(ctx)
	if err != nil {
		return fmt.Errorf("read what infra stack %s already provisions: %w", name, err)
	}
	provisioned := make(map[string]bool, len(outputs))
	for logical := range outputs {
		provisioned[logical] = true
	}
	return handedOver(manifest, provisioned, name.String())
}

func runAppStack(ctx context.Context, cfg Config, in stackInputs, manifest *deploymentsv1.Manifest, plan Plan, app *deploymentsv1.ManifestApp, id Identity, baked appBundle, builds appBuilds, links []*linksv1.Link, stage Stage, log func(string)) (outs []*progressv1.FunctionOutput, names map[string]string, err error) {
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

	res, upErr := upStack(ctx, cfg, in.realized, stack, stackTags(cfg, stack, plan.Promotion.PromotionID, id.DeploymentID(), builds.ids[name]), program, log, stage.ID)
	if upErr != nil {
		err = fmt.Errorf("provision app-deploy stack %s: %w", stack, upErr)
		return nil, nil, err
	}
	outs, names, err = collectAppFunctionOutputs(functions, res.Outputs)
	return outs, names, err
}

func appFunctions(manifest *deploymentsv1.Manifest, app string) []*deploymentsv1.ManifestFunction {
	var fns []*deploymentsv1.ManifestFunction
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

func applyDefaultTags(ctx context.Context, stack auto.Stack, tags map[string]string) error {
	if len(tags) == 0 {
		return nil
	}
	encoded, err := json.Marshal(map[string]map[string]string{"tags": tags})
	if err != nil {
		return fmt.Errorf("render default tags: %w", err)
	}
	ws := stack.Workspace()
	settings, err := ws.StackSettings(ctx, stack.Name())
	if err != nil {
		settings = &workspace.ProjectStack{}
	}
	if settings.Config == nil {
		settings.Config = config.Map{}
	}
	settings.Config[config.MustMakeKey("aws", "defaultTags")] = config.NewObjectValue(string(encoded))
	if err := ws.SaveStackSettings(ctx, stack.Name(), settings); err != nil {
		return fmt.Errorf("stamp default tags on %s: %w", stack.Name(), err)
	}
	return nil
}

const resourceLatencyOutlierThreshold = 30 * time.Second

const engineDrainGrace = 30 * time.Second

func startEngineTraceDrain(engineEvents <-chan events.EngineEvent, threshold time.Duration) <-chan EngineTrace {
	result := make(chan EngineTrace, 1)
	go func() {
		b := newEngineTraceBuilder(threshold)
		for ev := range engineEvents {
			b.consume(ev, time.Now())
		}
		result <- b.result()
	}()
	return result
}

func awaitEngineTrace(result <-chan EngineTrace, grace time.Duration) EngineTrace {
	select {
	case trace := <-result:
		return trace
	case <-time.After(grace):
		return EngineTrace{}
	}
}

func upStack(ctx context.Context, cfg Config, realized *Realized, name naming.StackName, tags map[string]string, program pulumi.RunFunc, log func(string), parentStage StageID) (auto.UpResult, error) {
	stack, err := prepareStack(ctx, cfg, realized, name, tags, program)
	if err != nil {
		return auto.UpResult{}, err
	}
	return runStack(ctx, cfg, stack, log, parentStage)
}

func prepareStack(ctx context.Context, cfg Config, realized *Realized, name naming.StackName, tags map[string]string, program pulumi.RunFunc) (auto.Stack, error) {
	access := cfg.pulumiAccess()
	stack, err := auto.UpsertStackInlineSource(ctx, name.String(), access.PulumiProject, program, access.workspace()...)
	if err != nil {
		return auto.Stack{}, fmt.Errorf("prepare stack %s: %w", name, err)
	}

	if err := applyDefaultTags(ctx, stack, tags); err != nil {
		return auto.Stack{}, err
	}

	index, err := stackIndex(cfg.Stacks)
	if err != nil {
		return auto.Stack{}, err
	}
	if err := realized.realize(ctx, index, naming.Sanitize(cfg.Slug), name); err != nil {
		return auto.Stack{}, err
	}

	if err := stampExpiry(ctx, stack, cfg.ExpiresAt); err != nil {
		return auto.Stack{}, err
	}
	return stack, nil
}

func runStack(ctx context.Context, cfg Config, stack auto.Stack, log func(string), parentStage StageID) (auto.UpResult, error) {
	logWriter := lineWriter(log)
	upOpts := []optup.Option{optup.Parallel(64)}
	if logWriter != nil {
		upOpts = append(upOpts, optup.ProgressStreams(logWriter))
	}

	engineEvents := make(chan events.EngineEvent, 256)
	traceResult := startEngineTraceDrain(engineEvents, resourceLatencyOutlierThreshold)
	upOpts = append(upOpts, optup.EventStreams(engineEvents))

	upStart := time.Now()
	res, err := stack.Up(ctx, upOpts...)
	upEnd := time.Now()
	logWriter.Flush()

	trace := awaitEngineTrace(traceResult, engineDrainGrace)
	if trace.Start.IsZero() {
		trace.Start, trace.End = upStart, upEnd
	}
	emitEngineTrace(cfg.Tracer, parentStage, trace, err)
	return res, err
}

func emitEngineTrace(t Tracer, parentStage StageID, trace EngineTrace, upErr error) {
	if t == nil || (trace.ResourceCount == 0 && upErr == nil) {
		return
	}
	batchErr := upErr
	if batchErr == nil && trace.Failed {
		batchErr = errEngineTraceFailed
	}
	spanUnder(t, parentStage, engineBatchSpanName, trace.Start, trace.End, batchErr, AttrResourceCount(trace.ResourceCount))

	for _, s := range trace.Standouts {
		var standoutErr error
		if s.Failed {
			standoutErr = errEngineTraceFailed
		}
		attrs := []Attr{AttrDurationMS(s.End.Sub(s.Start))}
		if s.Type != "" {
			attrs = append(attrs, AttrResourceType(s.Type))
		}
		if s.Name != "" {
			attrs = append(attrs, AttrResourceName(s.Name))
		}
		spanUnder(t, parentStage, resourceStandoutName(s.Op, s.Failed), s.Start, s.End, standoutErr, attrs...)
	}
}

func collectLinks(ctx context.Context, secrets SecretsReader, sessions sessionScope, manifest *deploymentsv1.Manifest, outputs auto.OutputMap) ([]*linksv1.Link, error) {
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

func collectAppFunctionOutputs(functions []*deploymentsv1.ManifestFunction, outputs auto.OutputMap) ([]*progressv1.FunctionOutput, map[string]string, error) {
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
