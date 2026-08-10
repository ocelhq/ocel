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
// A NEW WORKER VAR IS SUCH A CHANGE, and forgetting it is silent. Version 12 is
// OCEL_PREVIEW_APPS, which the preview entrypoint worker matches a request's
// host label against: a project whose root stack still stamps 11 keeps a worker
// that reads no app list, and every preview host under it 404s. Version 11 is
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
const rootStackVersion = "12"

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
	// The partial state is carried out with the error on purpose: reconcile
	// deploys workers before it records anything, so dropping what it did get to
	// leaves a live worker no teardown can ever name again.
	state, err := reconcileRootStack(ctx, stack, specs, cfg.RootStackState)
	if err != nil {
		return Result{RootStackState: state}, err
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
	warmed := warmDeployedFunctions(ctx, cfg, manifest, appFunctionNames, log)

	// Embedding follows warming for the obvious reason — it has nothing to embed
	// until the caches exist, and only the warm summaries carry the keys they
	// were published under — and stays before the promote for the same reason
	// warming does: it re-points functions at new code, and no traffic may see a
	// function mid-update. It is opt-in and cannot fail a deploy; a bundle it
	// skips keeps the package it was warmed on.
	embedBytecodeCaches(ctx, cfg, manifest, artifacts, warmed, log)

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

// rootStackSpecs builds the edge.RootStackSpecs one deploy reconciles: for
// production, one per app needing a generic worker (workerApps) plus a no-app
// fallback — the project's store instance still has to be seeded for every app's
// Deployment record to be staged into it, even one served straight off its own
// Function URL. A preview builds exactly one spec for the whole project: its
// entrypoint worker is attached to the project's declared wildcard and resolves
// the app from the request host, so it is neither per-app nor per-pointer. warn,
// when non-nil, receives the edge's non-fatal hostname advisories (an uncovered
// TLS name, a blocking DNS record).
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

	apps := workerApps(manifest)

	if cfg.Class == deploymentsv1.Environment_CLASS_PREVIEW {
		spec := base
		spec.GenericName = previewWorkerName(cfg.Slug)
		// The wildcard is the whole project's desired route set, so the sweep is
		// the whole project's preview worker family — the entrypoint worker and
		// the per-app workers an earlier shape of this deploy left standing,
		// whose pointer-exact routes outrank the wildcard and would shadow it on
		// those hostnames for good. It reaches nothing else: a production worker
		// is "ocel-<slug>-<env>-<app>", outside this stem, and so is a sibling
		// project's, subject to previewWorkerName's collision caveat.
		spec.PruneWorkerStem = previewWorkerName(cfg.Slug)
		// A project with no worker-backed app has nothing to serve at the edge:
		// the spec exists only to seed the project's store instance, and the
		// worker is attached to no hostname. The missing-preview-domain refusal
		// is deliberately not reached here — it exists so a preview that WOULD be
		// served does not end up on the wrong hostname, and a project that gains
		// its first worker-backed app meets it on that deploy.
		if len(apps) == 0 {
			spec.Generic = withPreviewVars(generic, "", apps)
			return []edge.RootStackSpec{spec}, nil
		}
		resolved, err := resolveWorkerHostnames(cfg, manifest, apps)
		if err != nil {
			return nil, err
		}
		// The declared wildcard is the project's complete desired route set, held
		// for the project's lifetime: every pointer of every app is served under
		// it, so the worker is attached to it once and anything else on the script
		// is drift. The project owns the base domain outright, so Ocel plants the
		// record behind it like any other hostname it serves.
		spec.Domains = []string{previewWildcard(resolved.previewBase)}
		spec.Generic = withPreviewVars(generic, resolved.previewBase, apps)
		return []edge.RootStackSpec{spec}, nil
	}

	if len(apps) == 0 {
		spec := base
		spec.GenericName = workerScriptName(cfg.StackName, "root")
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
		spec.GenericName = workerScriptName(cfg.StackName, name)
		spec.Generic = withVar(generic, "OCEL_APP", name)
		spec.Domains = resolved.hosts[name]
		specs = append(specs, spec)
	}
	return specs, nil
}

// Env var names the frozen preview worker reads to enter preview mode
// (workers/nextjs/src/index.ts).
const (
	envPreview           = "OCEL_PREVIEW"
	envPreviewBaseDomain = "OCEL_PREVIEW_BASE_DOMAIN"
	envPreviewApps       = "OCEL_PREVIEW_APPS"
)

// withPreviewVars turns a generic worker bundle into the project's preview
// entrypoint worker, setting the base domain when it is known. The worker
// recovers a request's pointer and app from the label below that base domain,
// so — unlike a production worker — it is bound to no single app; the app list
// is what it recovers the app half against, and is always bound.
func withPreviewVars(worker edge.Worker, baseDomain string, apps []*deploymentsv1.ManifestApp) edge.Worker {
	worker = withVar(worker, envPreview, "1")
	worker = withVar(worker, envPreviewApps, previewAppNames(apps))
	if baseDomain != "" {
		worker = withVar(worker, envPreviewBaseDomain, baseDomain)
	}
	return worker
}

// previewAppNames is the OCEL_PREVIEW_APPS value: the project's worker-backed
// app names, lowercased and comma-separated, in manifest order. The preview
// entrypoint worker recognises the "<app>" half of a "<pointer>--<app>" host
// label by matching this list — longest match winning — rather than by splitting
// on the separator, and accepts a bare "<pointer>" label only when the list
// names exactly one app. A project with no worker-backed app 404s every preview
// host, which is why the var is bound on every preview reconcile rather than
// only when there is something to say: an unbound var and an empty one would
// otherwise differ, and the worker reads both as "no apps". Pure.
func previewAppNames(apps []*deploymentsv1.ManifestApp) string {
	names := make([]string, 0, len(apps))
	for _, app := range apps {
		if name := strings.ToLower(strings.TrimSpace(app.GetName())); name != "" {
			names = append(names, name)
		}
	}
	return strings.Join(names, ",")
}

// previewWorkerName is the project's one preview entrypoint worker,
// "ocel-<project>-preview": project-scoped rather than per-app, because the
// worker resolves the app from the request host, and never colliding with a
// production root worker ("ocel-<project>-<env>-<app>") in the same account. It
// is the single deterministic name preview teardown reclaims, so nothing has to
// enumerate the edge to find it. It cannot, by name alone, tell a sibling
// project slugged "<slug>-preview" apart — the same collision caveat
// classifyProjectStacks carries — but slugs are unique per org and that shape is
// pathological. Pure.
func previewWorkerName(slug string) string {
	return sanitizeWorkerName("ocel-" + slug + "-preview")
}

// ProjectOwnsWorker reports whether an edge worker script name is one of this
// project's own: every worker Ocel deploys for a project is named "ocel-<slug>…"
// (previewWorkerName, workerScriptName), so the slug segment is what tells a
// project's own hold on a hostname apart from another project's. It answers the
// domain-claim check's "is this mine?", and it lives here rather than in the CLI
// because only the deploy host derives a worker name from a slug.
//
// It carries previewWorkerName's collision caveat — a sibling project slugged
// "<slug>-something" reads as this one — and one of its own: a slug long enough
// for workerScriptName to clamp the project segment is not recognised, which
// reads as someone else's claim and refuses rather than lets a deploy through.
// Pure.
func ProjectOwnsWorker(slug, script string) bool {
	if slug == "" || script == "" {
		return false
	}
	return edge.NameUnderStem(sanitizeWorkerName("ocel-"+slug), script)
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

// previewWildcard is previewBaseDomain's inverse: the wildcard the project
// declared, which is both its one worker route and the one DNS record every
// pointer hostname under base resolves through. No base yields no wildcard.
func previewWildcard(base string) string {
	if base == "" {
		return ""
	}
	return "*." + base
}

// workerHostnames is one deploy's resolved hostname intent for its worker-backed
// apps: the hostnames each app is served on, plus — preview only — the project's
// preview base domain, which the concrete pointer hostnames have nothing to
// recover it from.
type workerHostnames struct {
	hosts       map[string][]string
	previewBase string
}

// resolveWorkerHostnames is the single resolution from declared domains
// (workerDomains) to what a deploy actually serves on, shared by the root-stack
// spec and the reported app URLs so the two can never disagree.
func resolveWorkerHostnames(cfg Config, manifest *deploymentsv1.Manifest, apps []*deploymentsv1.ManifestApp) (workerHostnames, error) {
	declared, err := workerDomains(cfg, manifest, apps)
	if err != nil {
		return workerHostnames{}, err
	}
	if cfg.Class != deploymentsv1.Environment_CLASS_PREVIEW {
		return workerHostnames{hosts: declared}, nil
	}
	return previewHostnames(cfg, apps, declared)
}

// previewHostnames resolves the project's declared preview wildcard to the
// hostname each app is served on for this pointer. The base domain belongs to
// the whole project — one wildcard, one entrypoint worker — so every worker-
// backed app is served under it, and a deploy with a worker-backed app but no
// wildcard to serve it under is refused rather than silently falling back to the
// production default pointer. It is the only place a declared domain reaches
// previewBaseDomain. Pure.
func previewHostnames(cfg Config, apps []*deploymentsv1.ManifestApp, declared map[string][]string) (workerHostnames, error) {
	base := ""
	for _, app := range apps {
		name := app.GetName()
		domain := firstDomain(declared[name])
		if domain == "" {
			continue
		}
		resolved := previewBaseDomain(domain)
		if resolved == "" {
			return workerHostnames{}, fmt.Errorf("app %q declares the preview domain %q, which is not a \"*.\" wildcard: every preview is served on its own subdomain of it, so declare \"*.%s\" instead", name, domain, domain)
		}
		if base != "" && resolved != base {
			return workerHostnames{}, fmt.Errorf("this project declares more than one preview domain (%q and %q): a preview domain is claimed by the whole project, which serves every app from one wildcard, so declare a single project-level domains.preview", previewWildcard(base), domain)
		}
		base = resolved
	}
	if base == "" {
		return workerHostnames{}, fmt.Errorf("this project declares no preview domain, so a preview deploy has nowhere to serve: add a project-level domains.preview wildcard (e.g. \"*.preview.acme.com\") to your ocel config and deploy again")
	}

	// The separator is the grammar's, not a pointer's: the entrypoint worker
	// recovers the app half of "<pointer>--<app>" by matching the project's app
	// names, so a pointer carrying it can resolve to a shorter pointer naming no
	// deployment. The CLI refuses such a name (previewid.ValidateLabel); this is
	// the same rule at the RPC boundary, which any caller can cross.
	if strings.Contains(cfg.Identity, previewAppSeparator) {
		return workerHostnames{}, fmt.Errorf("preview name %q contains %q, which separates the preview from the app in the hostname it is served on: use a single hyphen", cfg.Identity, previewAppSeparator)
	}

	hosts := make(map[string][]string, len(apps))
	for _, app := range apps {
		name := app.GetName()
		host := previewHost(cfg.Identity, name, base, len(apps) == 1)
		if host == "" {
			continue
		}
		if label, _, _ := strings.Cut(host, "."); len(label) > previewLabelMaxLen {
			return workerHostnames{}, fmt.Errorf("preview pointer %q makes the hostname label %q %d characters, over the %d-character DNS limit: use a shorter preview name", cfg.Identity, label, len(label), previewLabelMaxLen)
		}
		hosts[name] = []string{host}
	}
	return workerHostnames{hosts: hosts, previewBase: base}, nil
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
// per-deploy modules/vars/assets at all — it resolves everything it serves from
// the Deployment record at request time, which is what lets one frozen script
// front a whole project's previews as well as each production app.
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
		App: name,
		// One entrypoint worker fronts a whole project, so what can serve a
		// Deployment travels on the Deployment: the worker dispatches on this and
		// answers 501 for a framework it has no handler for.
		Framework:        app.GetFramework(),
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
		if url := workerAppURL(resolved.hosts[app.GetName()]); url != "" {
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
