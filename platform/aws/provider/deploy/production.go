package deploy

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	deploymentsv1 "github.com/ocelhq/ocel/pkg/proto/deployments/v1"
	environmentv1 "github.com/ocelhq/ocel/pkg/proto/environment/v1"
	progressv1 "github.com/ocelhq/ocel/pkg/proto/progress/v1"
	"github.com/ocelhq/ocel/platform/aws/provider/dns"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

const stackVersion = "12"

type appDeployResult struct {
	App      string
	Identity Identity
	Record   edge.DeploymentRecord
	Err      error
}

func openStoreStack(cfg Config) (edge.EdgeStack, error) {
	state := cfg.StackState
	state.Slug = cfg.Slug
	state.Class = edgeClass(cfg.Tier)
	if cfg.StoreEndpoint != "" {
		state.Endpoint = cfg.StoreEndpoint
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

func checkTagAvailable(ctx context.Context, cfg Config, tag string) error {
	if tag == "" || cfg.StackState.Empty() {
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
	prior := cfg.StackState.Records
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
	state.RecordWrites(written)
	return state, nil
}

func pointableRecords(target edge.DNSTarget, state edge.StackState, hosts []string, say func(string)) ([]edge.Record, error) {
	bound := state.Bound
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

func releaseRecords(ctx context.Context, writer edge.DNSWriter, state edge.StackState, say func(string)) error {
	return dns.Release(ctx, writer, state.Records, say)
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

func planEnvironment(cfg Config) *environmentv1.Environment {
	return &environmentv1.Environment{
		Tier:      cfg.Tier,
		Lifecycle: cfg.Lifecycle,
		Identity:  cfg.Identity,
	}
}

func promotePointer(cfg Config) string {
	if cfg.Tier == environmentv1.Tier_TIER_PREVIEW {
		return cfg.Identity
	}
	return ""
}

func bootstrapCommand(cfg Config) string {
	if cfg.Tier == environmentv1.Tier_TIER_PREVIEW {
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

func stackSpecs(cfg Config, manifest *deploymentsv1.Manifest, version string, warn func(string)) ([]edge.StackSpec, error) {
	programmable := cfg.Edge.Facts().RunsCode
	var generic edge.Worker
	if programmable {
		var err error
		if generic, err = sharedWorker(cfg.Edge, cfg.workerFacts()); err != nil {
			return nil, err
		}
	}

	base := edge.StackSpec{
		Version:     version,
		Class:       edgeClass(cfg.Tier),
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
	world := hostingWorldFor(cfg, manifest)

	switch world {
	case hostingGlobalPreview:
		if _, err := world.hostnames(cfg, manifest, apps); err != nil {
			return nil, err
		}
		spec := base
		spec.Program = previewProgram("", apps)
		spec.PruneOnly = true
		return []edge.StackSpec{spec}, nil
	case hostingProjectPreview:
		spec := base
		if len(apps) == 0 {
			spec.Program = previewProgram("", apps)
			spec.PruneOnly = true
			return []edge.StackSpec{spec}, nil
		}
		resolved, err := world.hostnames(cfg, manifest, apps)
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

	resolved, err := world.hostnames(cfg, manifest, apps)
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

func edgeClass(tier environmentv1.Tier) edge.Class {
	if tier == environmentv1.Tier_TIER_PREVIEW {
		return edge.ClassPreview
	}
	return edge.ClassProduction
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

func buildDeploymentRecord(cfg Config, needs needRecords, manifest *deploymentsv1.Manifest, app *deploymentsv1.ManifestApp, id Identity, outs []*progressv1.FunctionOutput, builds appBuilds, functionNames map[string]string) (edge.DeploymentRecord, error) {
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
	record.Needs, record.SupportInEffect, record.Waived = needs.forApp(name)
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

func workerURLOutputs(cfg Config, manifest *deploymentsv1.Manifest) []*progressv1.FunctionOutput {
	apps := workerApps(cfg.ArtifactRoot, manifest)
	if len(apps) == 0 {
		return nil
	}
	resolved, err := hostingWorldFor(cfg, manifest).hostnames(cfg, manifest, apps)
	if err != nil {
		return nil
	}
	var outs []*progressv1.FunctionOutput
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
