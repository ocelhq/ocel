// Production deploy orchestration (ADR 0001): the stacked sequence — reconcile
// the frozen root stack, run the stable infra stack, run one app-deploy stack
// per app in parallel, stage each app's Deployment record, and issue a single
// atomic promote only once every app succeeded. Any app failure aborts the
// promote; the previous Deployment keeps serving and the failed stack/record
// is left for prune to sweep later.
//
// The Pulumi-touching halves (runInfraStack, runAppStack, runProduction) are
// exercised only by opt-in e2e, like Run itself. finalizeProductionDeploy and
// the plan/record/spec builders around it take already-computed results as
// plain data, so they have no Pulumi/AWS dependency and are what unit tests
// exercise directly against the edge.RootStack fake to assert the reconcile ->
// stage -> promote sequence and the abort-on-failure behavior.
package deploy

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"sync"

	ec2 "github.com/pulumi/pulumi-aws/sdk/v7/go/aws/ec2"
	"github.com/pulumi/pulumi/sdk/v3/go/auto"
	"github.com/pulumi/pulumi/sdk/v3/go/auto/optup"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"

	"github.com/ocelhq/ocel/cloud/edge"
	deploymentsv1 "github.com/ocelhq/ocel/pkg/proto/deployments/v1"
)

// rootStackVersion is the ocel root-stack revision this build expects.
// ReconcileRootStack is a no-op once a project's root stack already carries it;
// bump it only when the frozen generic/store worker bundles change shape in a
// way that needs re-deploying.
const rootStackVersion = "8"

// appDeployResult is one app's app-deploy-stack outcome, fed into
// finalizeProductionDeploy after Run has driven that stack (Pulumi) to
// completion or failure. Record is meaningless when Err is set.
type appDeployResult struct {
	App     string
	BuildID string
	Record  edge.DeploymentRecord
	Err     error
}

// runProduction realizes one production deploy under the stacked model: root
// reconcile, the infra stack, N app-deploy stacks in parallel, staged
// records, and a single atomic promote. It is Run's production branch and,
// like Run, is exercised only by opt-in e2e — the sequencing and atomicity it
// drives are unit-tested directly against finalizeProductionDeploy below.
func runProduction(ctx context.Context, cfg Config, manifest *deploymentsv1.Manifest, progress Progress, log func(string)) ([]*deploymentsv1.ResourceOutput, []string, edge.RootStackState, error) {
	stack, ok := cfg.Edge.(edge.RootStack)
	if !ok {
		return nil, nil, nil, fmt.Errorf("production deploys require an edge that supports the root stack (instant rollback); %s does not", cfg.Edge.Kind())
	}
	if cfg.StoreEndpoint == "" {
		return nil, nil, nil, fmt.Errorf("no shared deployments-store worker found for this account; re-run `ocel bootstrap` to provision it before a production deploy")
	}

	// Validate then check availability up front, before any artifact upload or
	// provisioning, so a bad or duplicate tag never orphans infrastructure. The
	// store's promote re-applies the uniqueness check atomically (the
	// concurrent-deploy backstop); these exist to fail fast with a clear message.
	if err := validateTag(cfg.Tag); err != nil {
		return nil, nil, nil, err
	}
	if err := checkTagAvailable(ctx, stack, cfg.RootStackState, cfg.Tag); err != nil {
		return nil, nil, nil, err
	}

	artifacts, err := uploadFunctionArtifacts(ctx, cfg, manifest, progress)
	if err != nil {
		return nil, nil, nil, err
	}
	if err := uploadPrerenderAssets(ctx, cfg, manifest); err != nil {
		return nil, nil, nil, err
	}
	if err := uploadStaticAssets(ctx, cfg, manifest); err != nil {
		return nil, nil, nil, err
	}

	builds, err := assignBuildIDs(cfg, manifest)
	if err != nil {
		return nil, nil, nil, err
	}
	promotionID, err := newRandomID()
	if err != nil {
		return nil, nil, nil, err
	}
	plan, err := BuildPlan(manifest, &deploymentsv1.Environment{Class: cfg.Class}, promotionID, builds)
	if err != nil {
		return nil, nil, nil, err
	}

	// Root reconcile runs before any AWS provisioning: a broken root stack
	// aborts the deploy up front rather than after paying for infra and every
	// app-deploy stack.
	progress.report(deploymentsv1.Phase_PHASE_PROVISIONING, "Reconciling the root stack", 0, 0)
	specs, err := rootStackSpecs(cfg, manifest, rootStackVersion)
	if err != nil {
		return nil, nil, nil, err
	}
	state, err := reconcileRootStack(ctx, stack, specs, cfg.RootStackState)
	if err != nil {
		return nil, nil, nil, err
	}

	progress.report(deploymentsv1.Phase_PHASE_PROVISIONING, "Provisioning infra stack", 0, 0)
	infraOutputs, err := runInfraStack(ctx, cfg, manifest, plan, log)
	if err != nil {
		return nil, nil, state, err
	}
	resourceEnv := resourceEnvValues(manifest, infraOutputs)

	progress.report(deploymentsv1.Phase_PHASE_PROVISIONING, "Provisioning app-deploy stacks", 0, 0)
	apps := manifestApps(manifest)
	results := make([]appDeployResult, len(apps))
	appOutputs := make([][]*deploymentsv1.ResourceOutput, len(apps))
	var wg sync.WaitGroup
	for i, app := range apps {
		i, app := i, app
		wg.Add(1)
		go func() {
			defer wg.Done()
			buildID := builds[app.GetName()]
			outs, err := runAppStack(ctx, cfg, manifest, plan, app, resourceEnv, artifacts, log)
			appOutputs[i] = outs
			record, recErr := buildDeploymentRecord(cfg, manifest, app, buildID, outs)
			if err == nil {
				err = recErr
			}
			results[i] = appDeployResult{App: app.GetName(), BuildID: buildID, Record: record, Err: err}
		}()
	}
	wg.Wait()

	progress.report(deploymentsv1.Phase_PHASE_FINALIZING, "Staging and promoting", 0, 0)
	if err := stageAndPromote(ctx, stack, state, promotionID, cfg.Tag, time.Now().Unix(), results); err != nil {
		return nil, nil, state, err
	}

	outputs := append([]*deploymentsv1.ResourceOutput{}, infraOutputs...)
	for _, outs := range appOutputs {
		outputs = append(outputs, outs...)
	}
	outputs = append(outputs, workerURLOutputs(cfg, manifest)...)
	return outputs, appURLs(manifest, outputs), state, nil
}

// maxTagLen bounds a deployment tag, mirroring the CLI's own limit.
const maxTagLen = 64

// validateTag re-checks the deployment-tag format host-side — the CLI validates
// too, but the RPC is a trust boundary a non-CLI caller could cross, so a
// malformed tag must never reach the store. Empty is the untagged default.
func validateTag(tag string) error {
	if tag == "" {
		return nil
	}
	if len(tag) > maxTagLen {
		return fmt.Errorf("tag must be at most %d characters (got %d)", maxTagLen, len(tag))
	}
	for _, r := range tag {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' ||
			r == '.' || r == '_' || r == '-' {
			continue
		}
		return fmt.Errorf("tag %q has an invalid character %q; use only letters, digits, '.', '_' and '-'", tag, r)
	}
	return nil
}

// checkTagAvailable rejects a deploy whose tag is already held by a live
// promotion. A no-op for an untagged deploy and for a project with no store yet
// (no prior state to read a history from). Pure of AWS — only edge.RootStack is
// called.
func checkTagAvailable(ctx context.Context, stack edge.RootStack, state edge.RootStackState, tag string) error {
	if tag == "" || state[edge.RootStackKeyEndpoint] == "" {
		return nil
	}
	history, err := stack.History(ctx, state)
	if err != nil {
		return fmt.Errorf("check tag availability: %w", err)
	}
	for _, h := range history {
		if h.Tag == tag {
			return fmt.Errorf("tag %q is already used by promotion %s; pick another tag, or roll back to it with `ocel rollback --tag %s`", tag, h.PromotionID, tag)
		}
	}
	return nil
}

// reconcileRootStack reconciles the root stack once per spec, threading the
// resulting state forward so a project with several worker-fronted apps
// reconciles its (shared) store once and each app's generic-worker deployment
// in turn. Pure of Pulumi/AWS: only edge.RootStack is called.
func reconcileRootStack(ctx context.Context, stack edge.RootStack, specs []edge.RootStackSpec, prior edge.RootStackState) (edge.RootStackState, error) {
	state := prior
	for _, spec := range specs {
		next, err := stack.ReconcileRootStack(ctx, spec, state)
		if err != nil {
			return state, fmt.Errorf("reconcile root stack %q: %w", spec.GenericName, err)
		}
		state = next
	}
	return state, nil
}

// stageAndPromote stages every successful app's Deployment record into an
// already-reconciled root stack, and — only if every app succeeded — issues
// the single atomic promote that makes them all live together. Any app
// failure aborts before Promote is ever called: the store still holds
// whatever it staged (harmless — never promoted, swept by a later prune) but
// the active pointer never moves and the previous Deployment keeps serving.
// Pure of Pulumi/AWS/Cloudflare: the caller has already reconciled the root
// stack and run every app-deploy stack.
func stageAndPromote(ctx context.Context, stack edge.RootStack, state edge.RootStackState, promotionID, tag string, now int64, results []appDeployResult) error {
	var failed []string
	builds := make(map[string]string, len(results))
	for _, r := range results {
		if r.Err != nil {
			failed = append(failed, fmt.Sprintf("%s: %v", r.App, r.Err))
			continue
		}
		if err := stack.PutStaged(ctx, state, r.Record); err != nil {
			return fmt.Errorf("stage deployment for %s: %w", r.App, err)
		}
		builds[r.App] = r.BuildID
	}
	if len(failed) > 0 {
		return fmt.Errorf("app-deploy failed for %s; promote aborted, the previous Deployment keeps serving", strings.Join(failed, "; "))
	}

	if err := stack.Promote(ctx, state, edge.Promotion{PromotionID: promotionID, Ts: now, Builds: builds, Tag: tag}); err != nil {
		return fmt.Errorf("promote %s: %w", promotionID, err)
	}
	return nil
}

// finalizeProductionDeploy composes reconcileRootStack and stageAndPromote —
// the same order runProduction drives them in, just without any AWS
// provisioning between the two. Pure of Pulumi/AWS/Cloudflare, so this is
// what unit tests exercise directly against the edge.RootStack fake to assert
// the reconcile -> stage -> promote sequence and the abort-on-failure
// behavior.
func finalizeProductionDeploy(ctx context.Context, stack edge.RootStack, specs []edge.RootStackSpec, prior edge.RootStackState, promotionID, tag string, now int64, results []appDeployResult) (edge.RootStackState, error) {
	state, err := reconcileRootStack(ctx, stack, specs, prior)
	if err != nil {
		return prior, err
	}
	if err := stageAndPromote(ctx, stack, state, promotionID, tag, now, results); err != nil {
		return state, err
	}
	return state, nil
}

// rootStackSpecs builds one edge.RootStackSpec per app needing a generic worker
// (workerApps), plus a no-app fallback when a production project has none — the
// project's store instance still has to be seeded for every app's Deployment
// record to be staged into it, even one served straight off its own Function
// URL. Every spec shares the version, slug and shared-store coordinates; only
// GenericName/Domain vary per app.
func rootStackSpecs(cfg Config, manifest *deploymentsv1.Manifest, version string) ([]edge.RootStackSpec, error) {
	generic, err := genericWorkerBundle(cfg)
	if err != nil {
		return nil, err
	}
	// The generic worker signs its Function-URL forwards (the Lambdas are
	// AWS_IAM-gated) with the edge reader's key. The bundle is the same bytes
	// for every app, so the credentials are bound once here.
	generic = withEdgeSigningCreds(generic, cfg)

	base := edge.RootStackSpec{
		Version:         version,
		Generic:         generic,
		Slug:            cfg.Slug,
		StoreScriptName: cfg.StoreScriptName,
		StoreEndpoint:   cfg.StoreEndpoint,
		BootstrapCred:   cfg.StoreBootstrapCred,
		Values:          cfg.EdgeValues,
	}

	apps := workerApps(manifest)
	if len(apps) == 0 {
		spec := base
		spec.GenericName = workerScriptName(cfg.StackName, "root")
		return []edge.RootStackSpec{spec}, nil
	}

	domains, err := workerDomains(cfg, manifest, apps)
	if err != nil {
		return nil, err
	}
	specs := make([]edge.RootStackSpec, 0, len(apps))
	for _, app := range apps {
		name := app.GetName()
		spec := base
		spec.GenericName = workerScriptName(cfg.StackName, name)
		spec.Generic = withVar(generic, "OCEL_APP", name)
		spec.Domain = domains[name]
		specs = append(specs, spec)
	}
	return specs, nil
}

// withVar returns worker with one additional plain-text var, leaving the
// caller's Worker untouched — the generic bundle is the same bytes for every
// app; only its OCEL_APP var tells one deployed copy which app to resolve.
func withVar(worker edge.Worker, name, value string) edge.Worker {
	vars := make(map[string]string, len(worker.Vars)+1)
	for k, v := range worker.Vars {
		vars[k] = v
	}
	vars[name] = value
	worker.Vars = vars
	return worker
}

// withEdgeSigningCreds binds the edge reader's IAM credentials onto the generic
// worker: the access key as a plain var and the secret key as a secret, under
// the names the worker reads to sign its Function-URL forwards. A substrate
// predating edge credentials adds neither — the worker then forwards unsigned,
// reaching only a Lambda that is still public.
func withEdgeSigningCreds(worker edge.Worker, cfg Config) edge.Worker {
	if cfg.EdgeAccessKeyID == "" || cfg.EdgeSecretKey == "" {
		return worker
	}
	worker = withVar(worker, edge.EdgeAccessKeyIDVar, cfg.EdgeAccessKeyID)
	secrets := make(map[string]string, len(worker.Secrets)+1)
	for k, v := range worker.Secrets {
		secrets[k] = v
	}
	secrets[edge.EdgeSecretKeyVar] = cfg.EdgeSecretKey
	worker.Secrets = secrets
	return worker
}

// genericWorkerBundle reads the frozen generic worker's compiled bundle: the
// same Next.js/Cloudflare worker bundle framework registrations already load
// for previews (ADR 0002 gave it request-time Deployment resolution), now
// reused as every production app's frozen worker rather than rebuilt per
// deploy.
func genericWorkerBundle(cfg Config) (edge.Worker, error) {
	bundles, err := edge.LoadBundleManifest()
	if err != nil {
		return edge.Worker{}, err
	}
	path, err := bundles.Path(edge.FrameworkNext, cfg.Edge.Kind())
	if err != nil {
		return edge.Worker{}, err
	}
	return loadWorkerBundle(path)
}

// loadWorkerBundle reads a compiled worker entrypoint off disk into the
// edge.Worker shape ReconcileRootStack uploads: the generic worker carries no
// per-deploy modules/vars/assets — those belong to the framework-assembled
// per-app worker previews still use.
func loadWorkerBundle(path string) (edge.Worker, error) {
	main, err := os.ReadFile(path)
	if err != nil {
		return edge.Worker{}, fmt.Errorf("read worker bundle %s: %w", path, err)
	}
	return edge.Worker{Main: edge.WorkerModule{
		Name:        "index.js",
		ContentType: "application/javascript+module",
		Content:     main,
	}}, nil
}

// newRandomID mints a fresh random identity: a production deploy's Promotion
// id, or a build id for a framework whose build carries none of its own.
func newRandomID() (string, error) {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("mint random id: %w", err)
	}
	return hex.EncodeToString(buf), nil
}

// assignBuildIDs is the deploy host's per-app build-id assignment BuildPlan
// consumes: a Next app's routing-manifest buildId (assigned at build time,
// immutable per build), or a freshly minted id for a framework with none.
func assignBuildIDs(cfg Config, manifest *deploymentsv1.Manifest) (BuildIDs, error) {
	builds := make(BuildIDs, len(manifest.GetApps()))
	for _, app := range manifestApps(manifest) {
		name := app.GetName()
		if app.GetFramework() == frameworkNext {
			id, err := nextBuildID(cfg, name)
			if err != nil {
				return nil, err
			}
			builds[name] = id
			continue
		}
		id, err := newRandomID()
		if err != nil {
			return nil, err
		}
		builds[name] = id
	}
	return builds, nil
}

// nextBuildID reads the buildId a Next app's build stamped into its
// routing-manifest.json.
func nextBuildID(cfg Config, app string) (string, error) {
	var pm prerenderManifest
	raw, err := os.ReadFile(filepath.Join(appArtifactRoot(cfg.ArtifactRoot, app), "routing-manifest.json"))
	if err != nil {
		return "", fmt.Errorf("read routing manifest for %s: %w", app, err)
	}
	if err := json.Unmarshal(raw, &pm); err != nil {
		return "", fmt.Errorf("parse routing manifest for %s: %w", app, err)
	}
	if pm.BuildID == "" {
		return "", fmt.Errorf("routing manifest for %s is missing buildId; rebuild the app", app)
	}
	return pm.BuildID, nil
}

// buildDeploymentRecord assembles one app's Deployment record from its
// app-deploy stack's outputs: the routing manifest and tag namespace for a
// Next app (nil/empty otherwise — the generic worker only dispatches
// Next-shaped records today), and every function's URL keyed by route id.
// AssetPrefix names exactly where uploadStaticAssets put this build's static
// output in the R2 cache store (Next apps only — see below), so the frozen
// worker can read it back with no project/app knowledge beyond what the
// record itself carries.
func buildDeploymentRecord(cfg Config, manifest *deploymentsv1.Manifest, app *deploymentsv1.ManifestApp, buildID string, outs []*deploymentsv1.ResourceOutput) (edge.DeploymentRecord, error) {
	name := app.GetName()
	urlByLogical := functionURLsByLogicalName(outs)
	record := edge.DeploymentRecord{
		App:          name,
		BuildID:      buildID,
		FunctionURLs: appFunctionURLsByRoute(manifest.GetFunctions(), name, urlByLogical),
		CreatedAt:    time.Now().Unix(),
	}
	if app.GetFramework() != frameworkNext {
		return record, nil
	}
	// Only a Next app ever has static output for uploadStaticAssets to have
	// published; leaving AssetPrefix set for any other app would point at a
	// prefix nothing was ever uploaded to.
	record.AssetPrefix = appAssetR2Prefix(manifest.GetProjectId(), name, buildID)

	raw, err := os.ReadFile(filepath.Join(appArtifactRoot(cfg.ArtifactRoot, name), "routing-manifest.json"))
	if err != nil {
		return edge.DeploymentRecord{}, fmt.Errorf("read routing manifest for %s: %w", name, err)
	}
	var routing any
	if err := json.Unmarshal(raw, &routing); err != nil {
		return edge.DeploymentRecord{}, fmt.Errorf("parse routing manifest for %s: %w", name, err)
	}
	record.RoutingManifest = routing

	caches, err := appCaches(cfg, manifest)
	if err != nil {
		return edge.DeploymentRecord{}, err
	}
	if isr := caches[name]; isr != nil {
		record.IsrPrefix = isr.Prefix
	}
	return record, nil
}

// workerURLOutputs reports each worker-fronted app's production URL: its
// custom domain when it has one, under the same workerOutputName appURLs
// already reads. An app with no custom domain is served off the edge's own
// vendor subdomain, which the root stack does not report back today — that app
// falls back to its own Function URLs, same as a non-worker app.
func workerURLOutputs(cfg Config, manifest *deploymentsv1.Manifest) []*deploymentsv1.ResourceOutput {
	apps := workerApps(manifest)
	if len(apps) == 0 {
		return nil
	}
	domains, err := workerDomains(cfg, manifest, apps)
	if err != nil {
		return nil
	}
	var outs []*deploymentsv1.ResourceOutput
	for _, app := range apps {
		if domain := domains[app.GetName()]; domain != "" {
			outs = append(outs, collectFunctionOutput(workerOutputName(app.GetName()), "https://"+domain))
		}
	}
	return outs
}

// runInfraStack provisions the project's SDK-declared resources (postgres,
// bucket) into the stable, per-project infra stack. Untouched by
// rollback. Opt-in-e2e only, like Run's single-stack program.
func runInfraStack(ctx context.Context, cfg Config, manifest *deploymentsv1.Manifest, plan Plan, log func(string)) ([]*deploymentsv1.ResourceOutput, error) {
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
		for _, r := range manifest.GetResources() {
			var err error
			switch {
			case r.GetPostgres() != nil:
				_, err = registerPostgres(pctx, r.GetLogicalName(), translatePostgres(r.GetPostgres()), vpc.Id, vpc.CidrBlock, subnets.Ids)
			case r.GetBucket() != nil:
				_, err = registerBucket(pctx, r.GetLogicalName(), translateBucket(r.GetBucket()), cfg.StateTable, cfg.StateTableARN, cfg.ListenerCodePath)
			default:
				continue
			}
			if err != nil {
				return fmt.Errorf("declare %s: %w", r.GetLogicalName(), err)
			}
		}
		return nil
	}

	res, err := upStack(ctx, cfg, plan.InfraStack, program, log)
	if err != nil {
		return nil, fmt.Errorf("provision infra stack %s: %w", plan.InfraStack, err)
	}
	return collectResourceOutputs(ctx, cfg.Secrets, manifest, res.Outputs)
}

// runAppStack provisions one app's Lambda functions into its per-deploy
// app-deploy stack, wiring resourceEnv (the infra stack's already-resolved
// resource outputs, reduced to plain strings) into each function's env as a
// concrete value rather than a cross-stack Pulumi reference — the two stacks
// never share a Pulumi program. Opt-in-e2e only.
func runAppStack(ctx context.Context, cfg Config, manifest *deploymentsv1.Manifest, plan Plan, app *deploymentsv1.ManifestApp, resourceEnv map[string]string, artifacts map[string]artifactRef, log func(string)) ([]*deploymentsv1.ResourceOutput, error) {
	name := app.GetName()
	functions := appFunctions(manifest, name)

	caches, err := appCaches(cfg, manifest)
	if err != nil {
		return nil, err
	}

	env := pulumi.StringMap{}
	for k, v := range resourceEnv {
		env[k] = pulumi.String(v)
	}

	program := func(pctx *pulumi.Context) error {
		role, err := newFunctionRole(pctx, executionRole{App: name, Cache: caches[name]})
		if err != nil {
			return err
		}
		for _, fn := range functions {
			if err := registerFunction(pctx, fn.GetLogicalName(), ocelTags(name, cfg.Env, manifest.GetProjectId()), translateFunction(fn), artifacts[fn.GetLogicalName()], env, caches[name], role.Arn); err != nil {
				return fmt.Errorf("declare %s: %w", fn.GetLogicalName(), err)
			}
		}
		return nil
	}

	stackName := plan.AppStacks[name]
	res, err := upStack(ctx, cfg, stackName, program, log)
	if err != nil {
		return nil, fmt.Errorf("provision app-deploy stack %s: %w", stackName, err)
	}
	return collectAppFunctionOutputs(functions, res.Outputs)
}

// appFunctions are one app's functions, in manifest order.
func appFunctions(manifest *deploymentsv1.Manifest, app string) []*deploymentsv1.ManifestFunction {
	var fns []*deploymentsv1.ManifestFunction
	for _, fn := range manifest.GetFunctions() {
		if fn.GetApp() == app {
			fns = append(fns, fn)
		}
	}
	return fns
}

// upStack is the Pulumi automation-API call every stack (infra, app-deploy)
// drives a stack through: prepare, then up. Identical to Run's single-stack
// preparation, just parameterized by stack name and program.
func upStack(ctx context.Context, cfg Config, stackName string, program pulumi.RunFunc, log func(string)) (auto.UpResult, error) {
	stack, err := auto.UpsertStackInlineSource(ctx, stackName, cfg.ProjectName, program,
		auto.Pulumi(cfg.Pulumi),
		auto.SecretsProvider("passphrase"),
		auto.EnvVars(map[string]string{
			"PULUMI_BACKEND_URL":       cfg.BackendURL,
			"PULUMI_CONFIG_PASSPHRASE": cfg.Passphrase,
			"AWS_REGION":               cfg.Region,
			"PULUMI_SKIP_CHECKPOINTS":  "true",
			"PULUMI_SKIP_UPDATE_CHECK": "true",
		}),
	)
	if err != nil {
		return auto.UpResult{}, fmt.Errorf("prepare stack %s: %w", stackName, err)
	}

	logWriter := lineWriter(log)
	var upOpts []optup.Option
	if logWriter != nil {
		upOpts = append(upOpts, optup.ProgressStreams(logWriter))
	}
	res, err := stack.Up(ctx, upOpts...)
	logWriter.Flush()
	return res, err
}

// resourceEnvValues reduces the infra stack's typed resource outputs to the
// same OCEL_RESOURCE_<TYPE>_<id> payload strings the single-stack program
// wires as pulumi.Output, so an app-deploy stack's functions see identical
// env despite the resource living in a different Pulumi stack.
func resourceEnvValues(manifest *deploymentsv1.Manifest, outputs []*deploymentsv1.ResourceOutput) map[string]string {
	byLogical := make(map[string]*deploymentsv1.ResourceOutput, len(outputs))
	for _, o := range outputs {
		byLogical[o.GetLogicalName()] = o
	}

	env := make(map[string]string)
	for _, r := range manifest.GetResources() {
		out, ok := byLogical[r.GetLogicalName()]
		if !ok {
			continue
		}
		key := functionEnvKey(r.GetResource().GetType(), r.GetResource().GetName())
		switch {
		case r.GetPostgres() != nil && out.GetPostgres() != nil:
			pg := out.GetPostgres()
			env[key] = postgresEnvPayload(pg.GetUsername(), pg.GetPassword(), pg.GetHost(), int(pg.GetPort()), pg.GetDatabase())
		case r.GetBucket() != nil && out.GetBucket() != nil:
			b := out.GetBucket()
			env[key] = bucketEnvPayload(b.GetAddress(), b.GetBucket())
		}
	}
	return env
}

// collectResourceOutputs is collectOutputs' resource-only half, for the infra
// stack (which declares no functions).
func collectResourceOutputs(ctx context.Context, secrets SecretsReader, manifest *deploymentsv1.Manifest, outputs auto.OutputMap) ([]*deploymentsv1.ResourceOutput, error) {
	var result []*deploymentsv1.ResourceOutput
	for _, r := range manifest.GetResources() {
		if r.GetPostgres() == nil && r.GetBucket() == nil {
			continue
		}
		name := r.GetLogicalName()
		raw, ok := outputs[name]
		if !ok {
			return nil, fmt.Errorf("stack produced no output for %s", name)
		}
		fields, ok := raw.Value.(map[string]interface{})
		if !ok {
			return nil, fmt.Errorf("output for %s is not a map", name)
		}
		var (
			out *deploymentsv1.ResourceOutput
			err error
		)
		switch {
		case r.GetPostgres() != nil:
			out, err = collectPostgresOutput(ctx, secrets, name, fields)
		case r.GetBucket() != nil:
			out, err = collectBucketOutput(name, fields)
		}
		if err != nil {
			return nil, err
		}
		result = append(result, out)
	}
	return result, nil
}

// collectAppFunctionOutputs is collectOutputs' function-only half, scoped to
// one app-deploy stack's own functions.
func collectAppFunctionOutputs(functions []*deploymentsv1.ManifestFunction, outputs auto.OutputMap) ([]*deploymentsv1.ResourceOutput, error) {
	var result []*deploymentsv1.ResourceOutput
	for _, fn := range functions {
		name := fn.GetLogicalName()
		raw, ok := outputs[name]
		if !ok {
			return nil, fmt.Errorf("stack produced no output for %s", name)
		}
		fields, ok := raw.Value.(map[string]interface{})
		if !ok {
			return nil, fmt.Errorf("output for %s is not a map", name)
		}
		url, err := requireStringField(fields, name, outputKeyFunctionURL)
		if err != nil {
			return nil, err
		}
		result = append(result, collectFunctionOutput(name, url))
	}
	return result, nil
}
