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
	"maps"
	"os"
	"path/filepath"
	"strings"
	"time"

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
//
// A NEW WORKER VAR IS SUCH A CHANGE, and forgetting it is silent. Version 11 is
// OCEL_REVALIDATE_QUEUE_URL: the var is bound from a bootstrap output, so a
// substrate that gains a revalidator changes what every already-deployed
// project's generic worker should carry — but a project whose root stack
// already stamps this version skips the upload entirely and keeps a worker with
// no queue binding. It then renders every admitted refresh through
// originBlocking, which is the designed unpinned degradation rather than a
// failure, so nothing anywhere reports it. Caught on the live e2e run for
// ocelhq-wvag.27: the CloudFormation output carried the URL and the deployed
// worker did not. Version 10 (the ISR writer binding) is the same class of
// change and did bump; the queue's did not.
const rootStackVersion = "11"

// appDeployResult is one app's app-deploy-stack outcome, fed into
// finalizeProductionDeploy after Run has driven that stack (Pulumi) to
// completion or failure. Record is meaningless when Err is set.
type appDeployResult struct {
	App      string
	Identity DeploymentIdentity
	Record   edge.DeploymentRecord
	Err      error
}

// realize runs one deploy under the stacked model, for both production and
// preview: root reconcile, the infra stack (skipped for an ephemeral preview,
// which has none), N app-deploy stacks in parallel, staged records, and a
// single atomic promote of this deploy's pointer. It is Run's whole body and,
// like Run, is exercised only by opt-in e2e — the sequencing and atomicity it
// drives are unit-tested directly against finalizeDeploy below. Production and
// preview differ only in data threaded here: the store coordinates and
// root-stack state come from the preview substrate for a preview (Config, set
// by the server), the plan is class-aware (BuildPlan/rootStackSpecs), and the
// promote pointer is empty for production, the environment identity for a
// preview.
func realize(ctx context.Context, cfg Config, manifest *deploymentsv1.Manifest, progress Progress, log func(string)) (Result, error) {
	stack, ok := cfg.Edge.(edge.RootStack)
	if !ok {
		return Result{}, fmt.Errorf("deploys require an edge that supports the root stack (instant rollback); %s does not", cfg.Edge.Kind())
	}
	if cfg.StoreEndpoint == "" {
		return Result{}, fmt.Errorf("no deployments-store worker found for this account; re-run `%s` to provision it before deploying", bootstrapCommand(cfg))
	}

	// Validate then check availability up front, before any artifact upload or
	// provisioning, so a bad or duplicate tag never orphans infrastructure. The
	// store's promote re-applies the uniqueness check atomically (the
	// concurrent-deploy backstop); these exist to fail fast with a clear message.
	if err := validateTag(cfg.Tag); err != nil {
		return Result{}, err
	}
	if err := checkTagAvailable(ctx, stack, cfg.RootStackState, cfg.Tag); err != nil {
		return Result{}, err
	}

	// Sealed before the artifacts are packaged: the ciphertext rides inside
	// each function's deployment package, so it has to exist before anything
	// is hashed or uploaded.
	baked, err := renderAppBundles(cfg, manifest)
	if err != nil {
		return Result{}, err
	}

	artifacts, err := uploadFunctionArtifacts(ctx, cfg, manifest, baked, progress)
	if err != nil {
		return Result{}, err
	}
	if err := uploadPrerenderAssets(ctx, cfg, manifest); err != nil {
		return Result{}, err
	}
	if err := uploadStaticAssets(ctx, cfg, manifest); err != nil {
		return Result{}, err
	}
	if err := uploadEdgeBundles(ctx, cfg, manifest); err != nil {
		return Result{}, err
	}

	identities, err := assignIdentities(cfg, manifest, baked)
	if err != nil {
		return Result{}, err
	}
	promotionID, err := newRandomID()
	if err != nil {
		return Result{}, err
	}
	plan, err := BuildPlan(manifest, planEnvironment(cfg), promotionID, identities)
	if err != nil {
		return Result{}, err
	}

	// Root reconcile runs before any AWS provisioning: a broken root stack
	// aborts the deploy up front rather than after paying for infra and every
	// app-deploy stack.
	progress.report(deploymentsv1.Phase_PHASE_PROVISIONING, "Reconciling the root stack", 0, 0)
	specs, err := rootStackSpecs(cfg, manifest, rootStackVersion, log)
	if err != nil {
		return Result{}, err
	}
	state, err := reconcileRootStack(ctx, stack, specs, cfg.RootStackState)
	if err != nil {
		return Result{}, err
	}

	// An ephemeral preview has no infra stack (BuildPlan leaves it empty): its
	// functions get no resource-connection env — an acknowledged gap, not a
	// logical slice. Every other class realizes its per-project infra stack.
	var infraOutputs []*deploymentsv1.ResourceOutput
	if plan.InfraStack != "" {
		progress.report(deploymentsv1.Phase_PHASE_PROVISIONING, "Provisioning infra stack", 0, 0)
		infraOutputs, err = runInfraStack(ctx, cfg, manifest, plan, log)
		if err != nil {
			return Result{RootStackState: state}, err
		}
	}
	resourceEnv := resourceEnvValues(manifest, infraOutputs)

	progress.report(deploymentsv1.Phase_PHASE_PROVISIONING, "Provisioning app-deploy stacks", 0, 0)
	apps := manifestApps(manifest)
	results := make([]appDeployResult, len(apps))
	appOutputs := make([][]*deploymentsv1.ResourceOutput, len(apps))
	appFunctionNames := make([]map[string]string, len(apps))
	runAppStacks(apps, func(i int, app *deploymentsv1.ManifestApp) {
		id := identities[app.GetName()]
		outs, names, err := runAppStack(ctx, cfg, manifest, plan, app, resourceEnv, artifacts, baked[app.GetName()], log)
		appOutputs[i] = outs
		appFunctionNames[i] = names
		record, recErr := buildDeploymentRecord(cfg, manifest, app, id, outs)
		if err == nil {
			err = recErr
		}
		results[i] = appDeployResult{App: app.GetName(), Identity: id, Record: record, Err: err}
	})

	// Warming runs here — after every app's stack, before the promote — and the
	// ordering is load-bearing. A function is invokable as soon as its stack
	// succeeds, but no traffic reaches it until Promote, so the first real
	// request already finds a full cache. It is also the only ordering that is
	// safe against the upload leg's create-if-absent semantics: an organic cold
	// start that wins that race publishes whatever fraction of the app it
	// happened to compile, permanently, for the life of the build.
	warmDeployedFunctions(ctx, cfg, manifest, appFunctionNames, log)

	progress.report(deploymentsv1.Phase_PHASE_FINALIZING, "Staging and promoting", 0, 0)
	if err := stageAndPromote(ctx, cfg, stack, state, promotionID, cfg.Tag, promotePointer(cfg), time.Now().Unix(), results); err != nil {
		return Result{RootStackState: state}, err
	}

	outputs := append([]*deploymentsv1.ResourceOutput{}, infraOutputs...)
	for _, outs := range appOutputs {
		outputs = append(outputs, outs...)
	}
	outputs = append(outputs, workerURLOutputs(cfg, manifest)...)
	return Result{
		Outputs:        outputs,
		AppURLs:        appURLs(manifest, outputs),
		PromotionID:    promotionID,
		RootStackState: state,
	}, nil
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
	history, err := stack.History(ctx, state, "")
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
func stageAndPromote(ctx context.Context, cfg Config, stack edge.RootStack, state edge.RootStackState, promotionID, tag, pointer string, now int64, results []appDeployResult) error {
	// Before anything is staged, and therefore before anything can serve: a
	// build that goes live without its origin record has routes that enqueue a
	// revalidation and never receive one, with the send succeeding and the colo
	// sentinel re-arming. See originrecord.go.
	if err := writeOriginRecords(ctx, cfg, results); err != nil {
		return err
	}

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
		builds[r.App] = r.Identity.String()
	}
	if len(failed) > 0 {
		return fmt.Errorf("app-deploy failed for %s; promote aborted, the previous Deployment keeps serving", strings.Join(failed, "; "))
	}

	if err := stack.Promote(ctx, state, edge.Promotion{PromotionID: promotionID, Ts: now, Builds: builds, Tag: tag}, pointer); err != nil {
		return fmt.Errorf("promote %s: %w", promotionID, err)
	}
	return nil
}

// planEnvironment is the environment identity BuildPlan plans against: it
// carries the class, lifecycle, and identity from the deploy Config so a
// preview's stacks are scoped by its store pointer (an ephemeral preview also
// keys off its lifecycle). Production leaves lifecycle and identity empty.
func planEnvironment(cfg Config) *deploymentsv1.Environment {
	return &deploymentsv1.Environment{
		Class:     cfg.Class,
		Lifecycle: cfg.Lifecycle,
		Identity:  cfg.Identity,
	}
}

// promotePointer is the store pointer this deploy's promote moves: empty for
// production (the store's reserved default pointer), the environment identity
// (the DNS-safe preview slug/name) for a preview.
func promotePointer(cfg Config) string {
	if cfg.Class == deploymentsv1.Environment_CLASS_PREVIEW {
		return cfg.Identity
	}
	return ""
}

// bootstrapCommand is the bootstrap invocation a class's deploy tells the user
// to re-run when the account is missing something a deploy needs.
func bootstrapCommand(cfg Config) string {
	if cfg.Class == deploymentsv1.Environment_CLASS_PREVIEW {
		return "ocel bootstrap --preview"
	}
	return "ocel bootstrap"
}

// finalizeDeploy composes reconcileRootStack and stageAndPromote — the same
// order realize drives them in, just without any AWS provisioning between the
// two, promoting the given pointer (empty for production, the preview identity
// for a preview). Pure of Pulumi/AWS/Cloudflare, so this is what unit tests
// exercise directly against the edge.RootStack fake to assert the reconcile ->
// stage -> promote sequence and the abort-on-failure behavior.
func finalizeDeploy(ctx context.Context, cfg Config, stack edge.RootStack, specs []edge.RootStackSpec, prior edge.RootStackState, promotionID, tag, pointer string, now int64, results []appDeployResult) (edge.RootStackState, error) {
	state, err := reconcileRootStack(ctx, stack, specs, prior)
	if err != nil {
		return prior, err
	}
	if err := stageAndPromote(ctx, cfg, stack, state, promotionID, tag, pointer, now, results); err != nil {
		return state, err
	}
	return state, nil
}

// rootStackSpecs builds one edge.RootStackSpec per app needing a generic worker
// (workerApps), plus a no-app fallback when a production project has none — the
// project's store instance still has to be seeded for every app's Deployment
// record to be staged into it, even one served straight off its own Function
// URL. Every spec shares the version, slug and shared-store coordinates; only
// the app-keyed fields (GenericName, Domains, the app's vars) vary per app, and
// the hostname intent (PruneRoutes, RequiredRecord) per environment class. warn,
// when non-nil, receives the edge's
// non-fatal hostname advisories (an uncovered TLS name, a blocking DNS record).
func rootStackSpecs(cfg Config, manifest *deploymentsv1.Manifest, version string, warn func(string)) ([]edge.RootStackSpec, error) {
	generic, err := genericWorkerBundle(cfg)
	if err != nil {
		return nil, err
	}
	// The generic worker signs its Function-URL forwards (the Lambdas are
	// AWS_IAM-gated) with the edge reader's key, and addresses the ISR stores
	// directly under the same key. The bundle is the same bytes for every app, so
	// both are bound once here.
	generic = withRevalidateQueue(withImageOptimizer(withCacheCoordinates(withEdgeSigningCreds(generic, cfg), cfg), cfg), cfg)

	base := edge.RootStackSpec{
		Version:         version,
		Generic:         generic,
		Slug:            cfg.Slug,
		StoreScriptName: cfg.StoreScriptName,
		StoreEndpoint:   cfg.StoreEndpoint,
		BootstrapCred:   cfg.StoreBootstrapCred,

		ISRWriterScriptName: cfg.ISRWriterScriptName,
		Values:              cfg.EdgeValues,
		PruneRoutes:         true,
		Warn:                warn,
	}

	preview := cfg.Class == deploymentsv1.Environment_CLASS_PREVIEW

	genericName := func(app string) string {
		if preview {
			return previewGenericName(cfg.Slug, app)
		}
		return workerScriptName(cfg.StackName, app)
	}

	apps := workerApps(manifest)
	if len(apps) == 0 {
		spec := base
		spec.GenericName = genericName("root")
		if preview {
			spec.Generic = withPreviewVars(generic, "", "")
		}
		return []edge.RootStackSpec{spec}, nil
	}

	resolved, err := resolveWorkerHostnames(cfg, manifest, apps)
	if err != nil {
		return nil, err
	}
	specs := make([]edge.RootStackSpec, 0, len(apps))
	for _, app := range apps {
		name := app.GetName()
		spec := base
		spec.GenericName = genericName(name)
		spec.Generic = withVar(generic, "OCEL_APP", name)
		spec.Domains = resolved.routes[name]
		// A preview attaches only this pointer's own exact route to a script its
		// sibling pointers share, so it prunes nothing. Everything else stays
		// keyed to the declared wildcard: it is the one DNS record every pointer
		// under the base resolves through (shared, so required rather than
		// planted), and its base domain plus the app's route suffix are how the
		// worker recovers the pointer from a request's subdomain.
		if preview {
			baseDomain := resolved.baseDomains[name]
			spec.PruneRoutes = false
			spec.RequiredRecord = previewWildcard(baseDomain)
			spec.Generic = withPreviewVars(spec.Generic, baseDomain, previewRouteSuffix(cfg.Slug, name))
		}
		specs = append(specs, spec)
	}
	return specs, nil
}

// Env var names the frozen preview worker reads to enter preview mode
// (workers/nextjs/src/index.ts).
const (
	envPreview            = "OCEL_PREVIEW"
	envPreviewBaseDomain  = "OCEL_PREVIEW_BASE_DOMAIN"
	envPreviewLabelSuffix = "OCEL_PREVIEW_LABEL_SUFFIX"
)

// withPreviewVars turns a generic worker bundle into a preview root worker,
// setting the base domain and route-label suffix only when they are known. The
// worker recovers a request's pointer by stripping labelSuffix off the subdomain
// label, and degrades to reading the whole label as the pointer without it.
func withPreviewVars(worker edge.Worker, baseDomain, labelSuffix string) edge.Worker {
	worker = withVar(worker, envPreview, "1")
	if baseDomain != "" {
		worker = withVar(worker, envPreviewBaseDomain, baseDomain)
	}
	if labelSuffix != "" {
		worker = withVar(worker, envPreviewLabelSuffix, labelSuffix)
	}
	return worker
}

// previewGenericName shapes the preview root worker's name as
// "ocel-<project>-preview-<app>" so it can never collide with the production
// root worker ("ocel-<project>-<app>") in the same account.
func previewGenericName(slug, app string) string {
	return workerScriptName(slug+"-preview", app)
}

// previewWorkerPrefix is the name stem every one of a project's preview generic
// workers shares — the un-clamped stack segment of previewGenericName, so
// previewGenericName(slug, app) == previewWorkerPrefix(slug)+"-"+<app segment>
// for every app (see workerScriptName, which only clamps this stem when an
// unusually long app segment leaves it no budget). Preview teardown sweeps every
// deployed worker under this prefix, which is how it reclaims the shared,
// pointer-independent generic worker after the store history that named it is
// gone (a prior `ocel preview rm` dropped it). The "-preview" segment keeps the
// prefix off this project's own production workers ("ocel-<slug>-production-…").
// It cannot, by name alone, tell a sibling project slugged "<slug>-preview-…"
// apart — the same prefix-collision caveat classifyProjectStacks carries — but
// slugs are unique per org and that shape is pathological. Pure.
func previewWorkerPrefix(slug string) string {
	return sanitizeWorkerName("ocel-" + slug + "-preview")
}

// firstDomain is the first hostname in an app's declared domain list, or "" for
// none. A preview declares exactly one wildcard, so this is that wildcard.
func firstDomain(domains []string) string {
	if len(domains) == 0 {
		return ""
	}
	return domains[0]
}

// previewBaseDomain strips the leading "*." from a preview wildcard domain —
// "*.preview.app.com" becomes "preview.app.com". Returns "" for a non-wildcard.
// Mirrors projectconfig.PreviewBaseDomain (a separate Go module).
func previewBaseDomain(wildcard string) string {
	if !strings.HasPrefix(wildcard, "*.") {
		return ""
	}
	return wildcard[len("*."):]
}

// previewWildcard is previewBaseDomain's inverse: the one DNS record every
// pointer hostname under base resolves through. Shared by every project and
// pointer on that base, so a preview reconcile requires it rather than planting
// one of its own (RootStackSpec.RequiredRecord). No base yields no record.
func previewWildcard(base string) string {
	if base == "" {
		return ""
	}
	return "*." + base
}

// workerHostnames is one deploy's resolved hostname intent for its worker-backed
// apps: the hostnames each app's worker routes are attached to, plus — preview
// only — the base domain the app's declared wildcard names, which the concrete
// route hostname has nothing to recover it from.
type workerHostnames struct {
	routes      map[string][]string
	baseDomains map[string]string
}

// resolveWorkerHostnames is the single resolution from declared domains
// (workerDomains) to what a deploy actually serves on, shared by the root-stack
// spec, the reported app URLs and the Deployment record so those three can never
// disagree.
func resolveWorkerHostnames(cfg Config, manifest *deploymentsv1.Manifest, apps []*deploymentsv1.ManifestApp) (workerHostnames, error) {
	declared, err := workerDomains(cfg, manifest, apps)
	if err != nil {
		return workerHostnames{}, err
	}
	if cfg.Class != deploymentsv1.Environment_CLASS_PREVIEW {
		return workerHostnames{routes: declared}, nil
	}
	return previewHostnames(cfg, declared)
}

// previewHostnames resolves each app's declared preview wildcard to the one exact
// hostname this pointer is served on, so the sibling pointers sharing the app's
// script each hold a route of their own instead of fighting over one wildcard
// route (ocelhq-5w3). It is the only place a declared domain reaches
// previewBaseDomain: fed a concrete route host instead, that returns "" and the
// worker silently leaves preview mode. Pure.
func previewHostnames(cfg Config, declared map[string][]string) (workerHostnames, error) {
	resolved := workerHostnames{
		routes:      make(map[string][]string, len(declared)),
		baseDomains: make(map[string]string, len(declared)),
	}
	for app, domains := range declared {
		domain := firstDomain(domains)
		base := previewBaseDomain(domain)
		if base == "" {
			return workerHostnames{}, fmt.Errorf("app %q declares the preview domain %q, which is not a \"*.\" wildcard: every pointer is served on its own subdomain, so declare \"*.%s\" instead — or declare no preview domain and be served on the edge's own subdomain", app, domain, domain)
		}
		resolved.baseDomains[app] = base

		host := previewRouteHost(cfg.Slug, app, cfg.Identity, base)
		if host == "" {
			continue
		}
		if label, _, _ := strings.Cut(host, "."); len(label) > previewLabelMaxLen {
			return workerHostnames{}, fmt.Errorf("preview pointer %q makes the route label %q %d characters, over the %d-character DNS limit: the pointer must be at most %d characters", cfg.Identity, label, len(label), previewLabelMaxLen, previewPointerMaxLen)
		}
		resolved.routes[app] = []string{host}
	}
	return resolved, nil
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

// withCacheCoordinates binds the account-global stores the worker's cache
// entrypoint reads and writes the ISR cache in — see the var names in cloud/edge
// for why they are worker vars rather than per-deployment record fields. A store
// the substrate has not got (a bootstrap predating it) is left unbound rather
// than bound empty, so the worker reads it as absent the way it already reads a
// missing credential.
func withCacheCoordinates(worker edge.Worker, cfg Config) edge.Worker {
	for name, value := range map[string]string{
		edge.AWSRegionVar:   cfg.Region,
		edge.StateTableVar:  cfg.StateTable,
		edge.AssetBucketVar: cfg.AssetBucket,
	} {
		if value != "" {
			worker = withVar(worker, name, value)
		}
	}
	return worker
}

// withImageOptimizer binds the substrate's image optimizer Function URL onto the
// worker, which signs its POSTs to it with the edge credentials bound above. A
// substrate with no optimizer (an older bootstrap, or a provider build pinning no
// artifact) binds nothing rather than binding empty: the worker reads that as no
// origin and answers every valid /_next/image request 502, which is what it did
// before an optimizer existed.
func withImageOptimizer(worker edge.Worker, cfg Config) edge.Worker {
	if cfg.ImageOptimizerURL == "" {
		return worker
	}
	return withVar(worker, edge.ImageOptimizerURLVar, cfg.ImageOptimizerURL)
}

// withRevalidateQueue binds the substrate's ISR revalidation queue onto the
// worker, which sends an admitted background refresh to it rather than rendering
// through the origin.
//
// Empty binds nothing, and empty is what a substrate whose bootstrap rendered no
// consumer reports: bootstrap publishes the queue URL only alongside the
// revalidator, so this var tracks the drain rather than the queue. That is a
// correctness requirement rather than tidiness — an edge that enqueues into a
// queue nothing drains gets a successful send, reports the refresh landed,
// re-arms its colo sentinel, and stops revalidating the route until it hard-
// expires, with nothing anywhere reporting a failure.
func withRevalidateQueue(worker edge.Worker, cfg Config) edge.Worker {
	if cfg.RevalidateQueueURL == "" {
		return worker
	}
	return withVar(worker, edge.RevalidateQueueURLVar, cfg.RevalidateQueueURL)
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

// newRandomID mints a fresh random id: a production deploy's Promotion id, or a
// build id for a framework whose build carries none of its own.
func newRandomID() (string, error) {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("mint random id: %w", err)
	}
	return hex.EncodeToString(buf), nil
}

// assignIdentities is the deploy host's per-app Deployment-identity assignment
// BuildPlan consumes, and the one place an identity is derived: the app's build
// id — a Next app's routing-manifest buildId (assigned at build time, immutable
// per build), or a freshly minted id for a framework with none — plus the
// fingerprint of the values baked into this Deployment.
//
// The fingerprint comes from the bundles already rendered for this deploy, so
// the values that were sealed and the values the identity names cannot drift
// apart. An app that bakes nothing has no fingerprint and keeps its bare build
// id.
func assignIdentities(cfg Config, manifest *deploymentsv1.Manifest, bundles map[string]appBundle) (DeploymentIdentities, error) {
	identities := make(DeploymentIdentities, len(manifest.GetApps()))
	for _, app := range manifestApps(manifest) {
		name := app.GetName()
		buildID, err := appBuildID(cfg, app)
		if err != nil {
			return nil, err
		}
		id, err := NewDeploymentIdentity(buildID, bundles[name].Fingerprint)
		if err != nil {
			return nil, fmt.Errorf("deployment identity for %s: %w", name, err)
		}
		identities[name] = id
	}
	return identities, nil
}

// appBuildID is the build id one app's build carries: a Next app's
// routing-manifest buildId, or a freshly minted id for a framework whose build
// stamps none.
func appBuildID(cfg Config, app *deploymentsv1.ManifestApp) (string, error) {
	if app.GetFramework() == frameworkNext {
		return nextBuildID(cfg, app.GetName())
	}
	return newRandomID()
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
func buildDeploymentRecord(cfg Config, manifest *deploymentsv1.Manifest, app *deploymentsv1.ManifestApp, id DeploymentIdentity, outs []*deploymentsv1.ResourceOutput) (edge.DeploymentRecord, error) {
	name := app.GetName()
	urlByLogical := functionURLsByLogicalName(outs)
	fingerprint, variables := recordedAudit(cfg, app)
	record := edge.DeploymentRecord{
		App:              name,
		Identity:         id.String(),
		FunctionURLs:     appFunctionURLsByRoute(manifest.GetFunctions(), name, urlByLogical),
		CreatedAt:        time.Now().Unix(),
		ValueFingerprint: fingerprint,
		Variables:        variables,
	}
	if app.GetFramework() != frameworkNext {
		return record, nil
	}
	// Only a Next app ever has static output for uploadStaticAssets to have
	// published; leaving AssetPrefix set for any other app would point at a
	// prefix nothing was ever uploaded to.
	record.AssetPrefix = appAssetR2Prefix(manifest.GetSlug(), name, id.BuildID())

	routeHostnames, err := recordedRouteHostnames(cfg, manifest, name)
	if err != nil {
		return edge.DeploymentRecord{}, err
	}
	record.RouteHostnames = routeHostnames

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
		record.IsrWriteSecret = isr.WriterSecret
	}

	edgeWorkers, err := appEdgeWorkers(cfg, manifest.GetSlug(), name, id.BuildID())
	if err != nil {
		return edge.DeploymentRecord{}, err
	}
	record.EdgeWorkers = edgeWorkers
	return record, nil
}

// recordedRouteHostnames are the worker-route hostnames one app's deploy
// registered and its teardown therefore owns. Only a preview has any: its routes
// are minted and retired with the pointer, so `preview rm` deletes exactly these
// (edge.DeploymentRecord.RouteHostnames) without needing the project config that
// named them. Production records none — its routes are project-lifetime and
// reconciled declaratively, and deleting one per pointer would take the project's
// own domain off the edge.
func recordedRouteHostnames(cfg Config, manifest *deploymentsv1.Manifest, app string) ([]string, error) {
	if cfg.Class != deploymentsv1.Environment_CLASS_PREVIEW {
		return nil, nil
	}
	resolved, err := resolveWorkerHostnames(cfg, manifest, workerApps(manifest))
	if err != nil {
		return nil, err
	}
	return resolved.routes[app], nil
}

// workerURLOutputs reports each worker-fronted app's user-facing URL under the
// same workerOutputName appURLs already reads: its custom domain for production,
// and for a preview the concrete per-pointer subdomain (the ref under the
// wildcard's base domain) this deploy actually serves — never the wildcard
// itself. An app with no domain is served off the edge's own vendor subdomain,
// which the root stack does not report back today — that app falls back to its
// own Function URLs, same as a non-worker app.
func workerURLOutputs(cfg Config, manifest *deploymentsv1.Manifest) []*deploymentsv1.ResourceOutput {
	apps := workerApps(manifest)
	if len(apps) == 0 {
		return nil
	}
	resolved, err := resolveWorkerHostnames(cfg, manifest, apps)
	if err != nil {
		return nil
	}
	var outs []*deploymentsv1.ResourceOutput
	for _, app := range apps {
		if url := workerAppURL(resolved.routes[app.GetName()]); url != "" {
			outs = append(outs, collectFunctionOutput(workerOutputName(app.GetName()), url))
		}
	}
	return outs
}

// workerAppURL turns an app's route hostnames into the URL to feature. A preview's
// single hostname is already the concrete one this pointer is served on, so it is
// reported as-is. Production features the first non-wildcard hostname, or the
// first declared when every hostname is a wildcard (a pure multitenant deploy).
// No hostnames (a vendor-subdomain app) yields no URL. Its production branch
// mirrors cloudflare.canonicalDomainURL (a separate Go module): keep the two in
// step.
func workerAppURL(domains []string) string {
	if len(domains) == 0 {
		return ""
	}
	for _, host := range domains {
		if !strings.HasPrefix(host, "*.") {
			return "https://" + host
		}
	}
	return "https://" + domains[0]
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
func runAppStack(ctx context.Context, cfg Config, manifest *deploymentsv1.Manifest, plan Plan, app *deploymentsv1.ManifestApp, resourceEnv map[string]string, artifacts map[string]artifactRef, baked appBundle, log func(string)) ([]*deploymentsv1.ResourceOutput, map[string]string, error) {
	name := app.GetName()
	functions := appFunctions(manifest, name)

	caches, err := appCaches(cfg, manifest)
	if err != nil {
		return nil, nil, err
	}

	if err := checkRuntimeOwnedNames(app); err != nil {
		return nil, nil, err
	}

	env := make(map[string]string, len(resourceEnv))
	maps.Copy(env, resourceEnv)
	maps.Copy(env, variableEnv(app))
	maps.Copy(env, baked.env())

	// Accounted before the stack runs: an over-budget environment is a deploy
	// that cannot succeed, and it must not cost any provisioning first.
	for _, fn := range functions {
		if err := checkFunctionEnvBudget(fn.GetLogicalName(), functionEnv(env, translateFunction(fn), caches[name])); err != nil {
			return nil, nil, err
		}
	}

	program := func(pctx *pulumi.Context) error {
		role, err := newFunctionRole(pctx, appExecutionRole(cfg, name, caches, baked))
		if err != nil {
			return err
		}
		for _, fn := range functions {
			if err := registerFunction(pctx, fn.GetLogicalName(), ocelTags(name, cfg.Env, manifest.GetSlug()), translateFunction(fn), artifacts[fn.GetLogicalName()], env, caches[name], role.Arn); err != nil {
				return fmt.Errorf("declare %s: %w", fn.GetLogicalName(), err)
			}
		}
		return nil
	}

	stackName := plan.AppStacks[name]
	res, err := upStack(ctx, cfg, stackName, program, log)
	if err != nil {
		return nil, nil, fmt.Errorf("provision app-deploy stack %s: %w", stackName, err)
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
		auto.EnvVars(pulumiEnv(cfg.Region, cfg.BackendURL, cfg.Passphrase)),
	)
	if err != nil {
		return auto.UpResult{}, fmt.Errorf("prepare stack %s: %w", stackName, err)
	}

	// An ephemeral preview's stacks carry its expiry so `ocel preview ls` can
	// surface age/expiry and a future reaper can find orphans. A no-op (zero
	// ExpiresAt) for production and persistent previews.
	if err := stampExpiry(ctx, stack, cfg.ExpiresAt); err != nil {
		return auto.UpResult{}, err
	}

	logWriter := lineWriter(log)
	upOpts := []optup.Option{optup.Parallel(64)}
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
// one app-deploy stack's own functions, plus each function's realized physical
// Lambda name keyed by logical name.
//
// A missing URL fails the deploy and a missing name does not: the URL is how
// the app is served, while the name only feeds the warm pass, which is an
// optimization no deploy may fail for.
func collectAppFunctionOutputs(functions []*deploymentsv1.ManifestFunction, outputs auto.OutputMap) ([]*deploymentsv1.ResourceOutput, map[string]string, error) {
	var result []*deploymentsv1.ResourceOutput
	names := make(map[string]string, len(functions))
	for _, fn := range functions {
		name := fn.GetLogicalName()
		raw, ok := outputs[name]
		if !ok {
			return nil, nil, fmt.Errorf("stack produced no output for %s", name)
		}
		fields, ok := raw.Value.(map[string]interface{})
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
