package cloudflare

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"mime/multipart"
	"os"
	"slices"

	cf "github.com/cloudflare/cloudflare-go/v4"
	"github.com/cloudflare/cloudflare-go/v4/option"
	"github.com/cloudflare/cloudflare-go/v4/workers"

	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

const bootstrapSecretBinding = "BOOTSTRAP_SECRET"

const secretBytes = 32

const (
	sharedStoreScriptName  = "ocel-deployments-store"
	previewStoreScriptName = "ocel-deployments-store-preview"

	isrWriterScriptName        = "ocel-isr-writer"
	previewISRWriterScriptName = "ocel-isr-writer-preview"
)

func storeScriptNameFor(class edge.Class) (string, error) {
	return accountScriptNameFor("deployments store", class, sharedStoreScriptName, previewStoreScriptName)
}

func isrWriterScriptNameFor(class edge.Class) (string, error) {
	return accountScriptNameFor("isr writer", class, isrWriterScriptName, previewISRWriterScriptName)
}

func accountScriptNameFor(worker string, class edge.Class, production, preview string) (string, error) {
	switch class {
	case edge.ClassProduction:
		return production, nil
	case edge.ClassPreview:
		return preview, nil
	default:
		return "", fmt.Errorf("%s: unknown class %q", worker, class)
	}
}

type durableObjectClass struct {
	binding   string
	className string
}

const durableObjectBindingType = "durable_object_namespace"

const inheritedBindingType = "inherit"

type migrationStep struct {
	tag           string
	sqliteClasses []string
}

type durableObjectWorker struct {
	classes    []durableObjectClass
	migrations []migrationStep
}

var (
	deploymentsStoreWorker = durableObjectWorker{
		classes: []durableObjectClass{
			{binding: "DEPLOYMENTS_DO", className: "DeploymentsStore"},
		},
		migrations: []migrationStep{
			{tag: "v1", sqliteClasses: []string{"DeploymentsStore"}},
		},
	}
	isrWriterWorker = durableObjectWorker{
		classes: []durableObjectClass{
			{binding: "ISR_WRITER_DO", className: "IsrDeploy"},
			{binding: "ISR_SNAPSHOT_DO", className: "IsrSnapshot"},
		},
		migrations: []migrationStep{
			{tag: "v1", sqliteClasses: []string{"IsrDeploy"}},
			{tag: "v2", sqliteClasses: []string{"IsrSnapshot"}},
		},
	}
)

const genericStoreBinding = "DEPLOYMENTS"

const genericISRWriterBinding = "ISR_WRITER"

const genericSlugBinding = "OCEL_SLUG"

type private struct {
	EntryWorkers []string `json:"entryWorkers,omitempty"`
}

type stack struct {
	p     *provider
	state edge.StackState
	own   private
}

func (s *stack) State() edge.StackState {
	held := s.state
	held.Adapter = edge.Own(s.own)
	return held
}

func (s *stack) Ledger() edge.Ledger { return s }

func (p *provider) Open(state edge.StackState) (edge.EdgeStack, error) {
	s := &stack{p: p, state: state}
	if err := state.Adapter.Into(&s.own); err != nil {
		return nil, err
	}
	return s, nil
}

func (p *provider) Reconcile(ctx context.Context, spec edge.StackSpec, prior edge.StackState) (edge.EdgeStack, error) {
	accountID := os.Getenv(envAccountID)
	if accountID == "" {
		return nil, fmt.Errorf("%s is not set; it is required to reconcile the Cloudflare stack", envAccountID)
	}
	program := spec.Program
	if program == nil {
		return nil, fmt.Errorf("the Cloudflare edge runs the entry worker; stack %q carries no program", spec.Slug)
	}

	slug := spec.Slug
	endpoint := program.StoreEndpoint

	generic := genericWorker(spec, slug)
	stamp, err := specStamp(spec, generic)
	if err != nil {
		return nil, err
	}

	id, stamps, err := p.ensureInstance(ctx, spec, prior)
	if err != nil {
		return nil, err
	}
	opened := func(state edge.StackState) (edge.EdgeStack, error) {
		var own private
		if !spec.PruneOnly {
			own.EntryWorkers = p.recordEntryWorker(slug, program.Name)
		}
		return &stack{p: p, state: state, own: own}, nil
	}
	upToDate := stamps[program.Name] == stamp
	if upToDate && skipEdgeReconcile() {
		return opened(prior)
	}

	if spec.PruneOnly && program.Name == previewEntryScript {
		return nil, fmt.Errorf("prune-only stack may not target the shared preview entry worker %q", program.Name)
	}

	genericUp := upload{accountID: accountID, scriptName: program.Name}
	if !upToDate && !spec.PruneOnly {
		genericUp.worker = generic
		if err := p.putWorkerScript(ctx, genericUp, "generic worker"); err != nil {
			return nil, err
		}
	}

	if err := p.reconcileWorkerRoutes(ctx, genericUp, routePlan{
		desired:        spec.Domains,
		bound:          prior.Bound,
		prune:          spec.PruneRoutes,
		pruneStem:      program.PruneWorkerStem,
		requiredRecord: program.RequiredRecord,
	}, spec.Warn); err != nil {
		return nil, err
	}
	if upToDate {
		return opened(prior)
	}

	if spec.PruneOnly {
		if err := p.deleteScript(ctx, accountID, program.Name); err != nil {
			return nil, fmt.Errorf("delete retired generic worker %q: %w", program.Name, err)
		}
	} else if _, err := p.setSubdomain(ctx, genericUp, len(spec.Domains) == 0); err != nil {
		return nil, fmt.Errorf("set generic worker subdomain: %w", err)
	}
	stamps[program.Name] = stamp
	encoded, err := stamps.encode()
	if err != nil {
		return nil, err
	}
	if err := p.putVersionStamp(ctx, endpoint, slug, id.secret, encoded); err != nil {
		return nil, fmt.Errorf("set stack version stamp: %w", err)
	}

	next := prior
	next.Slug = slug
	next.Endpoint = endpoint
	next.Secret = id.secret
	next.OwnerToken = id.ownerToken
	next.Class = spec.Class
	return opened(next)
}

func (p *provider) recordEntryWorker(slug, name string) []string {
	p.entryMu.Lock()
	defer p.entryMu.Unlock()
	if p.entryWorkers == nil {
		p.entryWorkers = map[string][]string{}
	}
	if !slices.Contains(p.entryWorkers[slug], name) {
		p.entryWorkers[slug] = append(p.entryWorkers[slug], name)
		slices.Sort(p.entryWorkers[slug])
	}
	return slices.Clone(p.entryWorkers[slug])
}

func (p *provider) putWorkerScript(ctx context.Context, up upload, what string) error {
	assetsJWT, err := p.uploadAssets(ctx, up)
	if err != nil {
		return fmt.Errorf("upload %s assets: %w", what, err)
	}
	if err := p.putScript(ctx, up, assetsJWT); err != nil {
		return fmt.Errorf("put %s: %w", what, err)
	}
	return nil
}

type storeIdentity struct {
	secret     string
	ownerToken string
}

func (p *provider) ensureInstance(ctx context.Context, spec edge.StackSpec, prior edge.StackState) (storeIdentity, stampSet, error) {
	if secret := prior.Secret; secret != "" && prior.Slug == spec.Slug {
		current, res, err := p.getVersionStamp(ctx, spec.Program.StoreEndpoint, spec.Slug, secret)
		switch {
		case err == nil:
			return storeIdentity{secret: secret, ownerToken: prior.OwnerToken}, decodeStampSet(current), nil
		case !unauthorized(res):
			return storeIdentity{}, nil, fmt.Errorf("read stack version stamp: %w", err)
		}
	}

	minted, err := mintIdentity()
	if err != nil {
		return storeIdentity{}, nil, err
	}
	adopted, err := p.initializeInstance(ctx, spec.Program.StoreEndpoint, spec.Slug, spec.Program.BootstrapCred, minted)
	if err != nil {
		return storeIdentity{}, nil, fmt.Errorf("initialize project store instance: %w", err)
	}
	return adopted, stampSet{}, nil
}

func mintIdentity() (storeIdentity, error) {
	secret, err := mintSecret()
	if err != nil {
		return storeIdentity{}, fmt.Errorf("mint project store secret: %w", err)
	}
	ownerToken, err := mintSecret()
	if err != nil {
		return storeIdentity{}, fmt.Errorf("mint project store owner token: %w", err)
	}
	return storeIdentity{secret: secret, ownerToken: ownerToken}, nil
}

func (s *stack) Destroy(ctx context.Context) error {
	var errs []error
	for _, hostname := range s.state.Bound {
		if err := s.UnbindDomain(ctx, hostname); err != nil {
			errs = append(errs, fmt.Errorf("unbind %q before destroying the stack that serves it: %w", hostname, err))
		}
	}
	names, err := s.p.stackWorkers(ctx, s.state)
	if err != nil {
		return errors.Join(append(errs, err)...)
	}
	if err := s.p.destroyWorkers(ctx, names); err != nil {
		return errors.Join(append(errs, err)...)
	}
	if err := s.p.destroyInstance(ctx, s.state); err != nil {
		errs = append(errs, fmt.Errorf("destroy deployments-store instance: %w", err))
	}
	return errors.Join(errs...)
}

func (p *provider) stackWorkers(ctx context.Context, state edge.StackState) ([]string, error) {
	named := map[string]bool{}
	var apps []string
	if secret := state.Secret; secret != "" {
		stamped, res, err := p.getVersionStamp(ctx, state.Endpoint, state.Slug, secret)
		if err != nil {
			if unauthorized(res) {
				return nil, fmt.Errorf("read stack version stamp: the deployments store rejected project %q's secret, so the workers it deployed cannot be named: %w", state.Slug, err)
			}
			return nil, fmt.Errorf("read stack version stamp: %w", err)
		}
		for name := range decodeStampSet(stamped) {
			named[name] = true
		}
		deployed, err := p.deployedApps(ctx, state)
		if err != nil {
			return nil, err
		}
		apps = deployed
	}

	conventional, err := conventionWorkerNames(state.Slug, state.Class, apps)
	if err != nil {
		return nil, err
	}
	for _, name := range conventional {
		named[name] = true
	}
	return slices.Sorted(maps.Keys(named)), nil
}

func (p *provider) deployedApps(ctx context.Context, state edge.StackState) ([]string, error) {
	history, err := (&stack{p: p, state: state}).History(ctx, "")
	if err != nil {
		return nil, fmt.Errorf("read the project's promotion history, which names the workers it deployed: %w", err)
	}
	apps := map[string]bool{}
	for _, entry := range history {
		for app := range entry.Builds {
			apps[app] = true
		}
	}
	return slices.Sorted(maps.Keys(apps)), nil
}

func (p *provider) destroyWorkers(ctx context.Context, names []string) error {
	accountID := os.Getenv(envAccountID)
	if accountID == "" {
		return fmt.Errorf("%s is not set; it is required to destroy the Cloudflare stack", envAccountID)
	}
	if len(names) == 0 {
		return nil
	}

	var errs []error
	for _, name := range names {
		if name == "" {
			continue
		}
		if err := p.detachCustomDomains(ctx, accountID, name); err != nil {
			errs = append(errs, err)
		}
		if err := p.deleteScript(ctx, accountID, name); err != nil {
			errs = append(errs, fmt.Errorf("delete worker %q: %w", name, err))
		}
	}
	return errors.Join(errs...)
}

func (p *provider) putDurableObjectScript(ctx context.Context, up upload, do durableObjectWorker, deployedClasses, inherited []string) error {
	body, contentType, err := buildDurableObjectScriptMultipart(up.worker, do, deployedClasses, inherited)
	if err != nil {
		return err
	}
	_, err = p.client.Workers.Scripts.Update(ctx, up.scriptName, workers.ScriptUpdateParams{
		AccountID: cf.F(up.accountID),
	}, option.WithRequestBody(contentType, body))
	return err
}

func (p *provider) putBootstrapSecret(ctx context.Context, accountID, scriptName, value string) error {
	_, err := p.client.Workers.Scripts.Secrets.Update(ctx, scriptName, workers.ScriptSecretUpdateParams{
		AccountID: cf.F(accountID),
		Body: workers.ScriptSecretUpdateParamsBodyWorkersBindingKindSecretText{
			Name: cf.F(bootstrapSecretBinding),
			Text: cf.F(value),
			Type: cf.F(workers.ScriptSecretUpdateParamsBodyWorkersBindingKindSecretTextTypeSecretText),
		},
	})
	return err
}

func buildDurableObjectScriptMultipart(worker edge.Worker, do durableObjectWorker, deployedClasses, inherited []string) ([]byte, string, error) {
	bindings := scriptBindings(worker, false)
	for _, class := range do.classes {
		bindings = append(bindings, map[string]any{
			"type":       durableObjectBindingType,
			"name":       class.binding,
			"class_name": class.className,
		})
	}
	for _, name := range inherited {
		bindings = append(bindings, map[string]any{
			"type": inheritedBindingType,
			"name": name,
		})
	}
	metadata := map[string]any{
		"main_module":         worker.Main.Name,
		"compatibility_date":  compatDate,
		"compatibility_flags": compatFlags,
		"observability":       observability(),
		"bindings":            bindings,
	}
	migrations, err := pendingMigrations(do.migrations, deployedClasses)
	if err != nil {
		return nil, "", err
	}
	if migrations != nil {
		metadata["migrations"] = migrations
	}
	metadataJSON, err := json.Marshal(metadata)
	if err != nil {
		return nil, "", fmt.Errorf("marshal %s worker metadata: %w", worker.Main.Name, err)
	}

	buf := &bytes.Buffer{}
	w := multipart.NewWriter(buf)
	if err := writePart(w, "metadata", "", "application/json", metadataJSON); err != nil {
		return nil, "", err
	}
	for _, mod := range append([]edge.WorkerModule{worker.Main}, worker.Modules...) {
		if err := writePart(w, mod.Name, mod.Name, mod.ContentType, mod.Content); err != nil {
			return nil, "", err
		}
	}
	if err := w.Close(); err != nil {
		return nil, "", err
	}
	return buf.Bytes(), w.FormDataContentType(), nil
}

func pendingMigrations(log []migrationStep, deployedClasses []string) (map[string]any, error) {
	for _, class := range deployedClasses {
		if !slices.ContainsFunc(log, func(step migrationStep) bool { return slices.Contains(step.sqliteClasses, class) }) {
			return nil, fmt.Errorf("deployed script carries Durable Object class %q, which this build's migration log does not create", class)
		}
	}

	steps := make([]map[string]any, 0, len(log))
	for _, step := range log {
		missing := make([]string, 0, len(step.sqliteClasses))
		for _, class := range step.sqliteClasses {
			if !slices.Contains(deployedClasses, class) {
				missing = append(missing, class)
			}
		}
		if len(missing) > 0 {
			steps = append(steps, map[string]any{"new_sqlite_classes": missing})
		}
	}
	if len(steps) == 0 {
		return nil, nil
	}
	return map[string]any{
		"new_tag": log[len(log)-1].tag,
		"steps":   steps,
	}, nil
}

func mintSecret() (string, error) {
	buf := make([]byte, secretBytes)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}

func withSecret(worker edge.Worker, name, value string) edge.Worker {
	secrets := make(map[string]string, len(worker.Secrets)+1)
	for k, v := range worker.Secrets {
		secrets[k] = v
	}
	secrets[name] = value
	worker.Secrets = secrets
	return worker
}

func genericWorker(spec edge.StackSpec, slug string) edge.Worker {
	worker := withVar(
		withService(spec.Program.Worker, genericStoreBinding, spec.Program.StoreScriptName),
		genericSlugBinding,
		slug,
	)
	if spec.Program.ISRWriterScriptName != "" {
		worker = withService(worker, genericISRWriterBinding, spec.Program.ISRWriterScriptName)
	}
	return bindCodeLoader(bindObjectStore(worker, spec.Values))
}

func withService(worker edge.Worker, name, service string) edge.Worker {
	services := make(map[string]string, len(worker.Services)+1)
	for k, v := range worker.Services {
		services[k] = v
	}
	services[name] = service
	worker.Services = services
	return worker
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
