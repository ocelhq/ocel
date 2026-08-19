package deploy

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"runtime/debug"
	"slices"
	"strconv"
	"strings"
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
	linksv1 "github.com/ocelhq/ocel/pkg/proto/links/v1"
	"github.com/ocelhq/ocel/platform/aws/provider/dns"
	"github.com/ocelhq/ocel/platform/aws/provider/membrane"
	cloudflare "github.com/ocelhq/ocel/platform/edge/cloudflare/deploy"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

const stackVersion = "12"

type appDeployResult struct {
	App      string
	Identity Identity
	Record   edge.DeploymentRecord
	Err      error
}

func realize(ctx context.Context, cfg Config, manifest *deploymentsv1.Manifest, progress Progress, log func(string)) (Result, error) {
	uploadStart := time.Now()
	finishUploading := func(err error) error {
		spanForStage(cfg.Tracer, cfg.Stages.Uploading, uploadStart, time.Now(), err)
		return err
	}

	if cfg.Edge.Kind() == cloudflare.Kind && cfg.StoreEndpoint == "" {
		return Result{}, finishUploading(fmt.Errorf("no deployments-store worker found for this account; re-run `%s` to provision it before deploying", bootstrapCommand(cfg)))
	}

	for _, app := range manifestApps(manifest) {
		if err := naming.ValidateDeploymentID(app.GetDeploymentId()); err != nil {
			return Result{}, finishUploading(fmt.Errorf("app %q: %w; upgrade the CLI so every app is built under one", app.GetName(), err))
		}
	}
	if err := checkStoreSchema(ctx, cfg); err != nil {
		return Result{}, finishUploading(err)
	}
	if err := validateTag(cfg.Tag); err != nil {
		return Result{}, finishUploading(err)
	}
	if err := checkMembraneServices(manifest, membrane.Serves); err != nil {
		return Result{}, finishUploading(err)
	}
	if err := checkListenerCode(manifest, cfg.ListenerCodePath); err != nil {
		return Result{}, finishUploading(err)
	}
	needs, err := checkNeeds(ctx, cfg, manifest)
	if err != nil {
		return Result{}, finishUploading(err)
	}
	cfg.needs = needs
	consumed, err := consumeLinks(ctx, cfg, manifest, log)
	if err != nil {
		return Result{}, finishUploading(err)
	}
	envName, err := EnvName(planEnvironment(cfg))
	if err != nil {
		return Result{}, finishUploading(err)
	}
	cfg.sessions = newSessionScope(naming.Sanitize(manifest.GetSlug()), envName, cfg.StateTableARN)
	if err := checkInlinePolicyBudget(manifest, consumed, cfg.sessions); err != nil {
		return Result{}, finishUploading(err)
	}
	if err := checkTagAvailable(ctx, cfg, cfg.Tag); err != nil {
		return Result{}, finishUploading(err)
	}

	apps := manifestApps(manifest)
	appStages := cfg.AppStages

	baked, err := renderAppBundles(cfg, manifest, consumed)
	if err != nil {
		return Result{}, finishUploading(err)
	}

	if err := checkISRWriterAgrees(cfg); err != nil {
		return Result{}, finishUploading(err)
	}
	builds, err := resolveAppBuilds(cfg, manifest, baked)
	if err != nil {
		return Result{}, finishUploading(err)
	}

	artifacts, err := uploadFunctionArtifacts(ctx, cfg, manifest, builds, progress)
	if err != nil {
		return Result{}, finishUploading(err)
	}

	progress.report(deploymentsv1.Phase_PHASE_UPLOADING, "Uploading prerender assets", 0, 0)
	if err := uploadPrerenderAssets(ctx, cfg, builds); err != nil {
		return Result{}, finishUploading(err)
	}
	progress.report(deploymentsv1.Phase_PHASE_UPLOADING, "Uploading static assets", 0, 0)
	if err := uploadStaticAssets(ctx, cfg, manifest, builds); err != nil {
		return Result{}, finishUploading(err)
	}
	progress.report(deploymentsv1.Phase_PHASE_UPLOADING, "Uploading edge bundles", 0, 0)
	if err := uploadEdgeBundles(ctx, cfg, manifest, builds); err != nil {
		return Result{}, finishUploading(err)
	}
	finishUploading(nil)

	provisionStart := time.Now()
	finishProvisioning := func(err error) error {
		spanForStage(cfg.Tracer, cfg.Stages.Provisioning, provisionStart, time.Now(), err)
		return err
	}

	identities := builds.identities
	promotionID, err := newRandomID()
	if err != nil {
		return Result{}, finishProvisioning(err)
	}
	plan, err := BuildPlan(manifest, planEnvironment(cfg), promotionID, identities)
	if err != nil {
		return Result{}, finishProvisioning(err)
	}

	if cfg.transformed, err = resolveTransforms(ctx, cfg, manifest); err != nil {
		return Result{}, finishProvisioning(err)
	}

	index, err := stackIndex(cfg.Stacks)
	if err != nil {
		return Result{}, finishProvisioning(err)
	}
	if err := index.AddProject(ctx, naming.Sanitize(manifest.GetSlug())); err != nil {
		return Result{}, finishProvisioning(err)
	}

	progress.report(deploymentsv1.Phase_PHASE_PROVISIONING, "Reconciling the edge stack", 0, 0)
	specs, err := stackSpecs(cfg, manifest, stackVersion, log)
	if err != nil {
		return Result{}, finishProvisioning(err)
	}
	edgeStack, err := reconcileStack(ctx, cfg.Edge, specs, MarkGlobalPreview(cfg.StackState, cfg, manifest))
	if err != nil {
		return Result{StackState: reconciledState(edgeStack, cfg)}, finishProvisioning(err)
	}
	state := MarkGlobalPreview(edgeStack.State(), cfg, manifest)
	state, err = settleStackRecords(ctx, cfg, specs, state, log)
	if err != nil {
		return Result{StackState: state}, finishProvisioning(err)
	}

	var links []*linksv1.Link
	if !plan.InfraStack.IsZero() {
		progress.report(deploymentsv1.Phase_PHASE_PROVISIONING, "Provisioning infra stack", 0, 0)
		links, err = runInfraStack(ctx, cfg, manifest, plan, log)
		if err != nil {
			return Result{StackState: state}, finishProvisioning(err)
		}
	}
	if err := checkLinkGrants(links); err != nil {
		return Result{StackState: state}, finishProvisioning(err)
	}
	if err := publishLinkRecords(ctx, cfg, manifest, links); err != nil {
		return Result{StackState: state}, finishProvisioning(err)
	}
	reportGrantVersions(consumed, log)
	granting := append(slices.Clone(links), consumedLinks(consumed)...)

	progress.report(deploymentsv1.Phase_PHASE_PROVISIONING, "Provisioning app-deploy stacks", 0, 0)
	results := make([]appDeployResult, len(apps))
	appOutputs := make([][]*deploymentsv1.FunctionOutput, len(apps))
	appFunctionNames := make([]map[string]string, len(apps))
	runAppStacks(apps, func(i int, app *deploymentsv1.ManifestApp) {
		id := identities[app.GetName()]
		outs, names, err := runAppStack(ctx, cfg, manifest, plan, app, id, artifacts, baked[app.GetName()], builds, granting, appStages[app.GetName()], log)
		appOutputs[i] = outs
		appFunctionNames[i] = names
		record, recErr := buildDeploymentRecord(cfg, manifest, app, id, outs, builds, names)
		if err == nil {
			err = recErr
		}
		results[i] = appDeployResult{App: app.GetName(), Identity: id, Record: record, Err: err}
	})

	warmed := warmDeployedFunctions(ctx, cfg, manifest, appFunctionNames, builds, log)

	embedBytecodeCaches(ctx, cfg, manifest, artifacts, warmed, builds, log)
	finishProvisioning(nil)

	finalizeStart := time.Now()
	progress.report(deploymentsv1.Phase_PHASE_FINALIZING, "Staging and promoting", 0, 0)
	promoted, err := stageAndPromote(ctx, cfg, edgeStack, promotionID, cfg.Tag, promotePointer(cfg), time.Now().Unix(), results)
	if err != nil {
		spanForStage(cfg.Tracer, cfg.Stages.Finalizing, finalizeStart, time.Now(), err)
		return Result{StackState: state}, err
	}
	spanForStage(cfg.Tracer, cfg.Stages.Finalizing, finalizeStart, time.Now(), nil)

	var functions []*deploymentsv1.FunctionOutput
	for _, outs := range appOutputs {
		functions = append(functions, outs...)
	}
	functions = append(functions, workerURLOutputs(cfg, manifest)...)
	return Result{
		Links:       links,
		Functions:   functions,
		AppURLs:     appURLs(manifest, functions),
		PromotionID: promotionID,
		Flip:        promoted.Flip,
		StackState:  state,
	}, nil
}

func openStoreStack(cfg Config) (edge.EdgeStack, error) {
	state := maps.Clone(cfg.StackState)
	if state == nil {
		state = edge.StackState{}
	}
	state[edge.StackKeySlug] = cfg.Slug
	state[edge.StackKeyClass] = string(edgeClass(cfg.Class))
	if cfg.StoreEndpoint != "" {
		state[edge.StackKeyEndpoint] = cfg.StoreEndpoint
	}
	return cfg.Edge.Open(state)
}

func checkStoreSchema(ctx context.Context, cfg Config) error {
	stack, err := openStoreStack(cfg)
	if err != nil {
		return err
	}
	got, err := stack.Ledger().SchemaVersion(ctx)
	switch {
	case errors.Is(err, edge.ErrStoreAbsent):
		return nil
	case errors.Is(err, edge.ErrStoreSchemaUnreadable):
		return fmt.Errorf("the deployments store predates the schema-version check, so this deploy cannot tell what it speaks; re-run `%s`", bootstrapCommand(cfg))
	case err != nil:
		return fmt.Errorf("read the deployments store schema version: %w; re-run `%s` to bring the store up to date", err, bootstrapCommand(cfg))
	case got != edge.StoreSchemaVersion:
		return fmt.Errorf("the deployments store speaks schema %d, this CLI speaks %d; re-run `%s`", got, edge.StoreSchemaVersion, bootstrapCommand(cfg))
	}
	return nil
}

const maxTagLen = 64

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

func checkTagAvailable(ctx context.Context, cfg Config, tag string) error {
	if tag == "" || len(cfg.StackState) == 0 {
		return nil
	}
	stack, err := openStoreStack(cfg)
	if err != nil {
		return err
	}
	history, err := stack.Ledger().History(ctx, "")
	if errors.Is(err, edge.ErrStoreAbsent) {
		return nil
	}
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

func reconcileStack(ctx context.Context, e edge.Edge, specs []edge.StackSpec, prior edge.StackState) (edge.EdgeStack, error) {
	stack, err := e.Open(prior)
	if err != nil {
		return nil, err
	}
	var reconciled edge.EdgeStack
	for _, spec := range specs {
		next, err := e.Reconcile(ctx, spec, stack.State())
		if err != nil {
			return reconciled, fmt.Errorf("reconcile stack %q: %w", specName(spec), err)
		}
		stack, reconciled = next, next
	}
	return stack, nil
}

func specName(spec edge.StackSpec) string {
	if spec.Program == nil {
		return spec.Slug
	}
	return spec.Program.Name
}

func settleStackRecords(ctx context.Context, cfg Config, specs []edge.StackSpec, state edge.StackState, say func(string)) (edge.StackState, error) {
	var hosts []string
	for _, spec := range specs {
		hosts = append(hosts, spec.Domains...)
	}
	records, err := pointableRecords(edge.TargetFor(cfg.Edge, state), state, hosts, say)
	if err != nil {
		return state, err
	}
	if cfg.DNS == nil {
		if len(records) == 0 || cfg.DNSAwait == nil {
			return state, nil
		}
		return state, cfg.DNSAwait.Await(ctx, records, say)
	}
	prior, err := edge.WrittenRecords(cfg.StackState)
	if err != nil {
		return state, err
	}
	written, err := cfg.DNS.EnsureRecords(ctx, records, say)
	if err != nil {
		return state, err
	}
	if departed := recordsDropped(prior, written); len(departed) > 0 {
		for _, rec := range departed {
			say(fmt.Sprintf("Removing %s: no app serves it any more", rec))
		}
		if err := cfg.DNS.DeleteRecords(ctx, departed); err != nil {
			return state, err
		}
	}
	return edge.WithWrittenRecords(state, written)
}

func pointableRecords(target edge.DNSTarget, state edge.StackState, hosts []string, say func(string)) ([]edge.Record, error) {
	bound := edge.BoundDomains(state)
	records := make([]edge.Record, 0, len(hosts))
	for _, host := range hosts {
		if host == "" {
			continue
		}
		if edge.Pointable(target, bound, host) {
			pointed, err := edge.RecordsFor(target, []string{host})
			if err != nil {
				return nil, err
			}
			records = append(records, pointed...)
			continue
		}
		if strings.HasPrefix(host, "*.") {
			return nil, fmt.Errorf(
				"nothing to point %s at, and no command binds a wildcard to it: serve this project's previews on the substrate-wide wildcard with `ocel domain use '%s' --preview` and drop domains.preview from its config, or deploy onto an edge that fronts the wildcard the config declares",
				host, host,
			)
		}
		say(fmt.Sprintf(
			"Leaving %s unpointed: the %s edge serves nothing there yet — run `ocel domain add` to settle its certificate and surface, then deploy again",
			host, target.Kind,
		))
	}
	return records, nil
}

func releaseRecords(ctx context.Context, cfg Config, state edge.StackState, say func(string)) error {
	records, err := edge.WrittenRecords(state)
	if err != nil {
		return err
	}
	return dns.Release(ctx, cfg.DNS, records, say)
}

func recordsDropped(prior, kept []edge.Record) []edge.Record {
	var dropped []edge.Record
	for _, rec := range prior {
		if !slices.Contains(kept, rec) {
			dropped = append(dropped, rec)
		}
	}
	return dropped
}

func reconciledState(stack edge.EdgeStack, cfg Config) edge.StackState {
	if stack == nil {
		return cfg.StackState
	}
	return stack.State()
}

func stageAndPromote(ctx context.Context, cfg Config, stack edge.EdgeStack, promotionID, tag, pointer string, now int64, results []appDeployResult) (edge.Promotion, error) {
	if err := writeOriginRecords(ctx, cfg, results); err != nil {
		return edge.Promotion{}, err
	}

	var failed []string
	builds := make(map[string]string, len(results))
	for _, r := range results {
		if r.Err != nil {
			failed = append(failed, fmt.Sprintf("%s: %v", r.App, r.Err))
			continue
		}
		if err := stack.Ledger().PutStaged(ctx, r.Record); err != nil {
			return edge.Promotion{}, fmt.Errorf("stage deployment for %s: %w", r.App, err)
		}
		builds[r.App] = r.Identity.String()
	}
	if len(failed) > 0 {
		return edge.Promotion{}, fmt.Errorf("app-deploy failed for %s; promote aborted, the previous Deployment keeps serving", strings.Join(failed, "; "))
	}

	return promote(ctx, cfg.Edge, stack, edge.Promotion{PromotionID: promotionID, Ts: now, Builds: builds, Tag: tag}, pointer)
}

func promote(ctx context.Context, e edge.Edge, stack edge.EdgeStack, promotion edge.Promotion, pointer string) (edge.Promotion, error) {
	flip := e.FlipBound()
	promotion.Flip = &flip
	if err := stack.Promote(ctx, promotion, pointer); err != nil {
		return edge.Promotion{}, fmt.Errorf("promote %s: %w", promotion.PromotionID, err)
	}
	return promotion, nil
}

func planEnvironment(cfg Config) *deploymentsv1.Environment {
	return &deploymentsv1.Environment{
		Class:     cfg.Class,
		Lifecycle: cfg.Lifecycle,
		Identity:  cfg.Identity,
	}
}

func promotePointer(cfg Config) string {
	if cfg.Class == deploymentsv1.Environment_CLASS_PREVIEW {
		return cfg.Identity
	}
	return ""
}

func bootstrapCommand(cfg Config) string {
	if cfg.Class == deploymentsv1.Environment_CLASS_PREVIEW {
		return "ocel bootstrap --preview"
	}
	return "ocel bootstrap"
}

func finalizeDeploy(ctx context.Context, cfg Config, specs []edge.StackSpec, prior edge.StackState, promotionID, tag, pointer string, now int64, results []appDeployResult) (edge.StackState, error) {
	stack, err := reconcileStack(ctx, cfg.Edge, specs, prior)
	if err != nil {
		return prior, err
	}
	if _, err := stageAndPromote(ctx, cfg, stack, promotionID, tag, pointer, now, results); err != nil {
		return stack.State(), err
	}
	return stack.State(), nil
}

func sharedWorker(cfg Config) (edge.Worker, error) {
	generic, err := genericWorkerBundle(cfg)
	if err != nil {
		return edge.Worker{}, err
	}
	return withOriginBodyBudget(withRevalidateQueue(withImageOptimizer(withCacheCoordinates(withEdgeSigningCreds(generic, cfg), cfg), cfg), cfg)), nil
}

func stackSpecs(cfg Config, manifest *deploymentsv1.Manifest, version string, warn func(string)) ([]edge.StackSpec, error) {
	_, programmable := cfg.Edge.(edge.Programmable)
	var generic edge.Worker
	if programmable {
		var err error
		if generic, err = sharedWorker(cfg); err != nil {
			return nil, err
		}
	}

	base := edge.StackSpec{
		Version:     version,
		Class:       edgeClass(cfg.Class),
		Slug:        cfg.Slug,
		Values:      cfg.EdgeValues,
		PruneRoutes: true,
		Warn:        warn,
	}
	program := func(name string, worker edge.Worker) *edge.ProgramSpec {
		if !programmable {
			return nil
		}
		return &edge.ProgramSpec{
			Name:                name,
			Worker:              worker,
			StoreScriptName:     cfg.StoreScriptName,
			StoreEndpoint:       cfg.StoreEndpoint,
			BootstrapCred:       cfg.StoreBootstrapCred,
			ISRWriterScriptName: cfg.ISRWriterScriptName,
		}
	}
	previewProgram := func(baseDomain string, apps []*deploymentsv1.ManifestApp) *edge.ProgramSpec {
		spec := program(previewWorkerName(cfg.Slug), withPreviewVars(generic, baseDomain, apps))
		if spec == nil {
			return nil
		}
		spec.PruneWorkerStem = previewWorkerStem(cfg.Slug)
		return spec
	}

	apps := workerApps(cfg.ArtifactRoot, manifest)

	if cfg.Class == deploymentsv1.Environment_CLASS_PREVIEW {
		spec := base
		if servesOnGlobalPreviewDomain(cfg, manifest) {
			if _, err := resolveWorkerHostnames(cfg, manifest, apps); err != nil {
				return nil, err
			}
			spec.Program = previewProgram("", apps)
			spec.PruneOnly = true
			return []edge.StackSpec{spec}, nil
		}
		if len(apps) == 0 {
			spec.Program = previewProgram("", apps)
			spec.PruneOnly = true
			return []edge.StackSpec{spec}, nil
		}
		resolved, err := resolveWorkerHostnames(cfg, manifest, apps)
		if err != nil {
			return nil, err
		}
		spec.Domains = []string{previewWildcard(resolved.previewBase)}
		spec.Program = previewProgram(resolved.previewBase, apps)
		return []edge.StackSpec{spec}, nil
	}

	if len(apps) == 0 {
		spec := base
		spec.Program = program(rootWorkerName(cfg.Slug, cfg.Env), generic)
		return []edge.StackSpec{spec}, nil
	}

	resolved, err := resolveWorkerHostnames(cfg, manifest, apps)
	if err != nil {
		return nil, err
	}
	specs := make([]edge.StackSpec, 0, len(apps))
	for _, app := range apps {
		name := app.GetName()
		spec := base
		spec.Program = program(workerScriptName(cfg.Slug, cfg.Env, name), withVar(generic, "OCEL_APP", name))
		spec.Domains = resolved.hosts[name]
		specs = append(specs, spec)
	}
	return specs, nil
}

func edgeClass(class deploymentsv1.Environment_Class) edge.Class {
	if class == deploymentsv1.Environment_CLASS_PREVIEW {
		return edge.ClassPreview
	}
	return edge.ClassProduction
}

const (
	envPreview           = "OCEL_PREVIEW"
	envPreviewBaseDomain = "OCEL_PREVIEW_BASE_DOMAIN"
	envPreviewApps       = "OCEL_PREVIEW_APPS"
)

func withPreviewVars(worker edge.Worker, baseDomain string, apps []*deploymentsv1.ManifestApp) edge.Worker {
	worker = withVar(worker, envPreview, "1")
	worker = withVar(worker, envPreviewApps, previewAppNames(apps))
	if baseDomain != "" {
		worker = withVar(worker, envPreviewBaseDomain, baseDomain)
	}
	return worker
}

func previewAppNames(apps []*deploymentsv1.ManifestApp) string {
	names := make([]string, 0, len(apps))
	for _, app := range apps {
		if name := strings.ToLower(strings.TrimSpace(app.GetName())); name != "" {
			names = append(names, name)
		}
	}
	return strings.Join(names, ",")
}

func firstDomain(domains []string) string {
	if len(domains) == 0 {
		return ""
	}
	return domains[0]
}

func previewBaseDomain(wildcard string) string {
	if !strings.HasPrefix(wildcard, "*.") {
		return ""
	}
	return wildcard[len("*."):]
}

func previewWildcard(base string) string {
	if base == "" {
		return ""
	}
	return "*." + base
}

type workerHostnames struct {
	hosts       map[string][]string
	previewBase string
}

func resolveWorkerHostnames(cfg Config, manifest *deploymentsv1.Manifest, apps []*deploymentsv1.ManifestApp) (workerHostnames, error) {
	declared, err := workerDomains(cfg, manifest, apps)
	if err != nil {
		return workerHostnames{}, err
	}
	if cfg.Class != deploymentsv1.Environment_CLASS_PREVIEW {
		return workerHostnames{hosts: declared}, nil
	}
	if servesOnGlobalPreviewDomain(cfg, manifest) {
		resolved, err := globalPreviewHostnames(cfg, apps)
		if err != nil {
			return workerHostnames{}, err
		}
		if err := checkPreviewLabels(cfg.Slug, apps, resolved); err != nil {
			return workerHostnames{}, err
		}
		return resolved, nil
	}
	resolved, err := previewHostnames(cfg, apps, declared)
	if err != nil {
		return workerHostnames{}, err
	}
	if err := checkPreviewLabels("", apps, resolved); err != nil {
		return workerHostnames{}, err
	}
	return resolved, nil
}

func checkPreviewLabels(slug string, apps []*deploymentsv1.ManifestApp, resolved workerHostnames) error {
	for _, app := range apps {
		if err := PreviewLabelProblem(slug, resolved.hosts[app.GetName()]); err != nil {
			return err
		}
	}
	return nil
}

func servesOnGlobalPreviewDomain(cfg Config, manifest *deploymentsv1.Manifest) bool {
	return cfg.Class == deploymentsv1.Environment_CLASS_PREVIEW &&
		cfg.GlobalPreviewDomain != "" && !declaresPreviewDomain(manifest)
}

func globalPreviewHostnames(cfg Config, apps []*deploymentsv1.ManifestApp) (workerHostnames, error) {
	if err := checkPreviewPointer(cfg.Identity); err != nil {
		return workerHostnames{}, err
	}
	hosts := make(map[string][]string, len(apps))
	for _, app := range apps {
		name := app.GetName()
		qualifier := name
		if len(apps) == 1 {
			qualifier = ""
		}
		host := edge.PreviewHost(cfg.Slug, cfg.Identity, qualifier, cfg.GlobalPreviewDomain)
		if host == "" {
			continue
		}
		hosts[name] = []string{host}
	}
	return workerHostnames{hosts: hosts, previewBase: cfg.GlobalPreviewDomain}, nil
}

func checkPreviewPointer(pointer string) error {
	if strings.Contains(pointer, previewAppSeparator) {
		return fmt.Errorf("preview name %q contains %q, which separates the preview from the app in the hostname it is served on: use a single hyphen", pointer, previewAppSeparator)
	}
	return nil
}

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

	if err := checkPreviewPointer(cfg.Identity); err != nil {
		return workerHostnames{}, err
	}

	hosts := make(map[string][]string, len(apps))
	for _, app := range apps {
		name := app.GetName()
		host := previewHost(cfg.Identity, name, base, len(apps) == 1)
		if host == "" {
			continue
		}
		hosts[name] = []string{host}
	}
	return workerHostnames{hosts: hosts, previewBase: base}, nil
}

func withVar(worker edge.Worker, name, value string) edge.Worker {
	vars := make(map[string]string, len(worker.Vars)+1)
	for k, v := range worker.Vars {
		vars[k] = v
	}
	vars[name] = value
	worker.Vars = vars
	return worker
}

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

func withImageOptimizer(worker edge.Worker, cfg Config) edge.Worker {
	if cfg.ImageOptimizerURL == "" {
		return worker
	}
	return withVar(worker, edge.ImageOptimizerURLVar, cfg.ImageOptimizerURL)
}

func withOriginBodyBudget(worker edge.Worker) edge.Worker {
	worker = withVar(worker, edge.OriginBodyLimitVar, strconv.Itoa(lambdaOriginBodyLimitBytes))
	return withVar(worker, edge.OriginBodyEncodingVar, edge.OriginBodyEncodingBase64)
}

func withRevalidateQueue(worker edge.Worker, cfg Config) edge.Worker {
	if cfg.RevalidateQueueURL == "" {
		return worker
	}
	return withVar(worker, edge.RevalidateQueueURLVar, cfg.RevalidateQueueURL)
}

func genericWorkerBundle(cfg Config) (edge.Worker, error) {
	bundles, err := edge.LoadBundleManifest()
	if err != nil {
		return edge.Worker{}, err
	}
	path, err := bundles.Path(cfg.Edge.Kind())
	if err != nil {
		return edge.Worker{}, err
	}
	return loadWorkerBundle(path)
}

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

func newRandomID() (string, error) {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("mint random id: %w", err)
	}
	return hex.EncodeToString(buf), nil
}

func readRoutingManifest(cfg Config, app string) (any, bool, error) {
	raw, err := os.ReadFile(filepath.Join(appArtifactRoot(cfg.ArtifactRoot, app), edge.RoutingManifestFile))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("read routing manifest for %s: %w", app, err)
	}
	var routing any
	if err := json.Unmarshal(raw, &routing); err != nil {
		return nil, false, fmt.Errorf("parse routing manifest for %s: %w", app, err)
	}
	return routing, true, nil
}

func buildDeploymentRecord(cfg Config, manifest *deploymentsv1.Manifest, app *deploymentsv1.ManifestApp, id Identity, outs []*deploymentsv1.FunctionOutput, builds appBuilds, functionNames map[string]string) (edge.DeploymentRecord, error) {
	name := app.GetName()
	urlByLogical := functionURLsByLogicalName(outs)
	fingerprint, variables := recordedAudit(cfg, app)
	record := edge.DeploymentRecord{
		App:              name,
		Framework:        app.GetFramework(),
		Identity:         id.String(),
		DeploymentID:     id.DeploymentID(),
		BuildID:          builds.ids[name],
		FunctionURLs:     appFunctionURLsByRoute(manifest.GetFunctions(), name, urlByLogical),
		CreatedAt:        time.Now().Unix(),
		ValueFingerprint: fingerprint,
		Variables:        variables,
	}
	desc, _, err := readServeDescriptor(cfg.ArtifactRoot, name)
	if err != nil {
		return edge.DeploymentRecord{}, err
	}
	routing, routed, err := readRoutingManifest(cfg, name)
	if err != nil {
		return edge.DeploymentRecord{}, err
	}
	if desc.EdgeRouting && !routed {
		return edge.DeploymentRecord{}, fmt.Errorf("app %s declares edge routing but its build wrote no %s; rebuild the app", name, edge.RoutingManifestFile)
	}
	record.Entry = desc.Entry
	record.EntryFunction = functionNames[entryLogicalName(manifest, name, desc.Entry)]
	record.Needs, record.SupportInEffect, record.Waived = cfg.needs.forApp(name)
	if routed {
		record.RoutingManifest = routing
		record.AssetPrefix = appAssetPrefix(builds.coords[name])
	}

	if isr := builds.caches[name]; isr != nil {
		record.IsrPrefix = isr.Prefix
		record.IsrWriteSecret = isr.WriterSecret
	}

	edgeWorkers, err := appEdgeWorkers(cfg, builds.coords[name], name)
	if err != nil {
		return edge.DeploymentRecord{}, err
	}
	record.EdgeWorkers = edgeWorkers
	if edgeWorkers != nil {
		if env := variableEnv(app); len(env) > 0 {
			record.Env = env
		}
		if bundle := builds.baked[name]; edgeSealedDelivered(cfg, bundle) {
			record.Envelope = bundle.Envelope
		}
	}
	return record, nil
}

func entryLogicalName(manifest *deploymentsv1.Manifest, app, entry string) string {
	if entry == "" {
		return ""
	}
	for _, fn := range manifest.GetFunctions() {
		if fn.GetApp() == app && routeID(fn) == entry {
			return fn.GetLogicalName()
		}
	}
	return ""
}

func workerURLOutputs(cfg Config, manifest *deploymentsv1.Manifest) []*deploymentsv1.FunctionOutput {
	apps := workerApps(cfg.ArtifactRoot, manifest)
	if len(apps) == 0 {
		return nil
	}
	resolved, err := resolveWorkerHostnames(cfg, manifest, apps)
	if err != nil {
		return nil
	}
	var outs []*deploymentsv1.FunctionOutput
	for _, app := range apps {
		if url := workerAppURL(resolved.hosts[app.GetName()]); url != "" {
			outs = append(outs, collectFunctionOutput(workerOutputName(app.GetName()), url))
		}
	}
	return outs
}

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

func runInfraStack(ctx context.Context, cfg Config, manifest *deploymentsv1.Manifest, plan Plan, log func(string)) ([]*linksv1.Link, error) {
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
				err = registerPostgres(pctx, project, env, r.GetLogicalName(), cfg.transformed.forPostgres(r.GetLogicalName(), r.GetPostgres()), vpc.Id, vpc.CidrBlock, subnets.Ids)
			case r.GetBucket() != nil:
				err = registerBucket(pctx, project, env, r.GetLogicalName(), cfg.transformed.forBucket(r.GetLogicalName(), r.GetBucket()), cfg.StateTable, cfg.sessions, cfg.ListenerCodePath)
			default:
				continue
			}
			if err != nil {
				return fmt.Errorf("declare %s: %w", r.GetLogicalName(), err)
			}
		}
		return nil
	}

	stack, err := prepareStack(ctx, cfg, plan.InfraStack, infraStackTags(cfg, plan.InfraStack), program)
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
	return collectLinks(ctx, cfg.Secrets, cfg.sessions, manifest, res.Outputs)
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

func runAppStack(ctx context.Context, cfg Config, manifest *deploymentsv1.Manifest, plan Plan, app *deploymentsv1.ManifestApp, id Identity, artifacts map[string]artifactRef, baked appBundle, builds appBuilds, links []*linksv1.Link, stage Stage, log func(string)) (outs []*deploymentsv1.FunctionOutput, names map[string]string, err error) {
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

	env := appEnv(manifest, app, baked, cfg)
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
		if err = checkFunctionEnvBudget(fn.GetLogicalName(), functionEnv(declared, cfg.transformed.forFunction(fn), caches[name], bytecode[name])); err != nil {
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
		roleTags = cfg.transformed.forFunction(functions[0]).Tags
	}
	vpcAccess := false
	for _, fn := range functions {
		vpcAccess = vpcAccess || cfg.transformed.forFunction(fn).VPC.placed()
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
			Args:      cfg.transformed.forFunction,
			Artifacts: artifacts,
			Env:       env,
			ISR:       caches[name],
			Bytecode:  bytecode[name],
			Router:    router,
			Guard:     guard,
			RoleArn:   role.Arn,
			RoleName:  role.Name,
		}.register(pctx)
	}

	res, upErr := upStack(ctx, cfg, stack, stackTags(cfg, stack, plan.Promotion.PromotionID, id.DeploymentID(), builds.ids[name]), program, log, stage.ID)
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
		EnvClass:   envClass(cfg.Class),
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

func envClass(class deploymentsv1.Environment_Class) string {
	if class == deploymentsv1.Environment_CLASS_PREVIEW {
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

func upStack(ctx context.Context, cfg Config, name naming.StackName, tags map[string]string, program pulumi.RunFunc, log func(string), parentStage StageID) (auto.UpResult, error) {
	stack, err := prepareStack(ctx, cfg, name, tags, program)
	if err != nil {
		return auto.UpResult{}, err
	}
	return runStack(ctx, cfg, stack, log, parentStage)
}

func prepareStack(ctx context.Context, cfg Config, name naming.StackName, tags map[string]string, program pulumi.RunFunc) (auto.Stack, error) {
	stack, err := auto.UpsertStackInlineSource(ctx, name.String(), cfg.PulumiProject, program,
		auto.Pulumi(cfg.Pulumi),
		auto.SecretsProvider("passphrase"),
		auto.EnvVars(pulumiEnv(cfg.Region, cfg.BackendURL, cfg.Passphrase)),
	)
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
	if err := cfg.realized.realize(ctx, index, naming.Sanitize(cfg.Slug), name); err != nil {
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

func collectAppFunctionOutputs(functions []*deploymentsv1.ManifestFunction, outputs auto.OutputMap) ([]*deploymentsv1.FunctionOutput, map[string]string, error) {
	var result []*deploymentsv1.FunctionOutput
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
