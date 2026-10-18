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
	"slices"

	cf "github.com/cloudflare/cloudflare-go/v4"
	"github.com/cloudflare/cloudflare-go/v4/option"
	"github.com/cloudflare/cloudflare-go/v4/workers"

	"github.com/ocelhq/ocel/pkg/buildoutput"
	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/environment"
)

const bootstrapSecretBinding = "BOOTSTRAP_SECRET"

const secretBytes = 32

func storeScriptNameFor(namespace string, tier environment.Tier) (string, error) {
	return accountNameFor("releases store", namespace, tier, "releases-store")
}

func accountNameFor(what, namespace string, tier environment.Tier, stem string) (string, error) {
	if namespace == "" {
		return "", fmt.Errorf("%s: the cloudflare edge was opened without a bootstrap namespace, so it cannot name the account-wide workers and buckets this tier depends on", what)
	}
	var name string
	switch tier {
	case environment.TierProduction:
		name = namespace + "-" + stem
	case environment.TierPreview:
		name = namespace + "-" + stem + "-preview"
	default:
		return "", fmt.Errorf("%s: unknown tier %q", what, tier)
	}
	if len(name) > maxWorkerNameLength {
		return "", fmt.Errorf("%s: %q is %d characters, and a Cloudflare worker or bucket name allows at most %d; shorten the bootstrap namespace %q", what, name, len(name), maxWorkerNameLength, namespace)
	}
	return name, nil
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
	releasesStoreWorker = durableObjectWorker{
		classes: []durableObjectClass{
			{binding: "RELEASES_DO", className: "ReleasesStore"},
		},
		migrations: []migrationStep{
			{tag: "v1", sqliteClasses: []string{"ReleasesStore"}},
		},
	}
)

const genericStoreBinding = "RELEASES"

const genericSlugBinding = "OCEL_SLUG"

const genericDomainAppsBinding = "OCEL_DOMAIN_APPS"

const genericServesWorkersDevBinding = "OCEL_SERVES_WORKERS_DEV"

const purgePath = "/" + buildoutput.StateDir + "/purge"

type private struct {
	EntryWorkers []string `json:"entryWorkers,omitempty"`
	EnvelopeKey  string   `json:"envelopeKey,omitempty"`
}

func (own private) wrapsEnvelopes() bool {
	return own.EnvelopeKey != "" && len(own.EntryWorkers) > 0
}

type stack struct {
	p     *cloudflare
	state edge.StackState
	own   private
}

func (s *stack) State() edge.StackState {
	current := s.state
	current.Private = edge.Own(s.own)
	return current
}

func (p *cloudflare) Open(state edge.StackState) (edge.EdgeStack, error) {
	s := &stack{p: p, state: state}
	if err := state.Private.Into(&s.own); err != nil {
		return nil, err
	}
	return s, nil
}

func (p *cloudflare) Reconcile(ctx context.Context, spec edge.StackSpec, prior edge.StackState) (edge.EdgeStack, error) {
	accountID, err := requireAccountID("reconcile the Cloudflare stack")
	if err != nil {
		return nil, err
	}
	program, err := p.buildEntryProgram(spec.Tier, spec.Slug, spec.Program)
	if err != nil {
		return nil, err
	}
	if !spec.PruneOnly {
		if err := p.refuseBootstrapBehind(ctx, accountID, spec.Tier, bootstrapCaller{worker: "the entry worker this deploy uploads", retry: "deploy again"}); err != nil {
			return nil, err
		}
	}

	slug := spec.Slug
	endpoint := program.store.Endpoint

	var recorded private
	if err := prior.Private.Into(&recorded); err != nil {
		return nil, err
	}
	envelopeKey := recorded.EnvelopeKey
	if !spec.PruneOnly && envelopeKey == "" {
		minted, err := mintEnvelopeKey()
		if err != nil {
			return nil, err
		}
		envelopeKey = minted
	}

	generic, err := genericWorker(spec, program, envelopeKey)
	if err != nil {
		return nil, err
	}
	stamp, err := specStamp(spec, program, generic)
	if err != nil {
		return nil, err
	}

	secret, stamps, err := p.ensureInstance(ctx, spec, program.store, prior)
	if err != nil {
		return nil, err
	}
	held := edge.StackState{Endpoint: endpoint, Slug: slug, Secret: secret, Tier: spec.Tier}
	if err := p.putGate(ctx, held, spec.Gate); err != nil {
		return nil, fmt.Errorf("publish the %s gate of %s: %w", spec.Tier, slug, err)
	}
	opened := func(state edge.StackState) (edge.EdgeStack, error) {
		own := private{EnvelopeKey: envelopeKey}
		if !spec.PruneOnly {
			own.EntryWorkers = p.recordEntryWorker(slug, program.name)
		}
		return &stack{p: p, state: state, own: own}, nil
	}
	openedAsDeployed := func(state edge.StackState) (edge.EdgeStack, error) {
		switch {
		case spec.GlobalPreview != "":
			purgeURL, err := p.readSharedPreviewPurgeURL(ctx, accountID, slug)
			if err != nil {
				return nil, err
			}
			state.PurgeURL = purgeURL
		case spec.PruneOnly:
			state.PurgeURL = ""
		case state.PurgeURL == "":
			workersDev, err := p.subdomainURL(ctx, accountID, program.name)
			if err != nil {
				return nil, fmt.Errorf("read generic worker subdomain: %w", err)
			}
			state.PurgeURL = workersDev + purgePath
		}
		return opened(state)
	}
	upToDate := stamps[program.name] == stamp
	if upToDate && p.skipChecks {
		return openedAsDeployed(prior)
	}

	if spec.PruneOnly && program.name == previewEntryScript {
		return nil, fmt.Errorf("prune-only stack may not target the shared preview entry worker %q", program.name)
	}

	var purgeURL string
	genericUp := upload{accountID: accountID, scriptName: program.name}
	if !upToDate && !spec.PruneOnly {
		genericUp.worker = generic
		if err := p.putWorkerScript(ctx, genericUp, "generic worker"); err != nil {
			return nil, err
		}
	}

	if err := p.reconcileWorkerRoutes(ctx, genericUp, routeSpec{
		desired:         spec.Domains,
		bound:           prior.Bound,
		servedElsewhere: spec.ServedElsewhere,
		prune:           spec.PruneRoutes,
		pruneStem:       program.pruneWorkerStem,
		owns:            projectOwnsScript(p.namespace, slug),
	}, spec.Warn); err != nil {
		return nil, err
	}
	if upToDate {
		return openedAsDeployed(prior)
	}

	if spec.PruneOnly {
		if err := p.deleteScript(ctx, accountID, program.name); err != nil {
			return nil, fmt.Errorf("delete retired generic worker %q: %w", program.name, err)
		}
		if spec.GlobalPreview != "" {
			if purgeURL, err = p.readSharedPreviewPurgeURL(ctx, accountID, slug); err != nil {
				return nil, err
			}
		}
	} else {
		workersDev, err := p.setSubdomain(ctx, genericUp, true)
		if err != nil {
			return nil, fmt.Errorf("set generic worker subdomain: %w", err)
		}
		purgeURL = workersDev + purgePath
	}
	stamps[program.name] = stamp
	encoded, err := stamps.encode()
	if err != nil {
		return nil, err
	}
	if err := p.putVersionStamp(ctx, endpoint, slug, secret, encoded); err != nil {
		return nil, fmt.Errorf("set stack version stamp: %w", err)
	}

	next := prior
	next.PurgeURL = purgeURL
	next.Slug = slug
	next.Endpoint = endpoint
	next.Secret = secret
	next.Tier = spec.Tier
	return opened(next)
}

func (p *cloudflare) recordEntryWorker(slug, name string) []string {
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

func (p *cloudflare) putWorkerScript(ctx context.Context, up upload, what string) error {
	assetsJWT, err := p.uploadAssets(ctx, up)
	if err != nil {
		return fmt.Errorf("upload %s assets: %w", what, err)
	}
	if err := p.putScript(ctx, up, assetsJWT); err != nil {
		return fmt.Errorf("put %s: %w", what, err)
	}
	return nil
}

func (p *cloudflare) ensureInstance(ctx context.Context, spec edge.StackSpec, store edge.ReleasesStore, prior edge.StackState) (string, stampSet, error) {
	if secret := prior.Secret; secret != "" && prior.Slug == spec.Slug {
		current, res, err := p.getVersionStamp(ctx, store.Endpoint, spec.Slug, secret)
		switch {
		case err == nil:
			return secret, decodeStampSet(current), nil
		case !isUnauthorized(res):
			return "", nil, fmt.Errorf("read stack version stamp: %w", err)
		}
	}

	secret, err := mintSecret()
	if err != nil {
		return "", nil, fmt.Errorf("mint project store secret: %w", err)
	}
	if err := p.initializeInstance(ctx, store.Endpoint, spec.Slug, store.BootstrapCredential, secret); err != nil {
		return "", nil, fmt.Errorf("initialize project store instance: %w", err)
	}
	return secret, stampSet{}, nil
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
		errs = append(errs, fmt.Errorf("destroy releases-store instance: %w", err))
	}
	return errors.Join(errs...)
}

func (p *cloudflare) stackWorkers(ctx context.Context, state edge.StackState) ([]string, error) {
	named := map[string]bool{}
	var apps []string
	if secret := state.Secret; secret != "" {
		stamped, res, err := p.getVersionStamp(ctx, state.Endpoint, state.Slug, secret)
		if err != nil {
			if isUnauthorized(res) {
				return nil, fmt.Errorf("read stack version stamp: the releases store rejected project %q's secret, so the workers it deployed cannot be named: %w", state.Slug, err)
			}
			return nil, fmt.Errorf("read stack version stamp: %w", err)
		}
		for name := range decodeStampSet(stamped) {
			named[name] = true
		}
		deployed, err := p.readServedApps(ctx, state)
		if err != nil {
			return nil, fmt.Errorf("read the apps the releases store served, which names the workers they ran on: %w", err)
		}
		apps = deployed
	}

	conventional, err := conventionWorkerNames(p.namespace, state.Slug, state.Tier, apps)
	if err != nil {
		return nil, err
	}
	for _, name := range conventional {
		named[name] = true
	}
	return slices.Sorted(maps.Keys(named)), nil
}

func (p *cloudflare) destroyWorkers(ctx context.Context, names []string) error {
	accountID, err := requireAccountID("destroy the Cloudflare stack")
	if err != nil {
		return err
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

func (p *cloudflare) putDurableObjectScript(ctx context.Context, up upload, do durableObjectWorker, deployedClasses, inherited []string) error {
	body, contentType, err := buildDurableObjectScriptMultipart(up.worker, do, deployedClasses, inherited)
	if err != nil {
		return err
	}
	_, err = p.client.Workers.Scripts.Update(ctx, up.scriptName, workers.ScriptUpdateParams{
		AccountID: cf.F(up.accountID),
	}, option.WithRequestBody(contentType, body))
	return err
}

func (p *cloudflare) putBootstrapSecret(ctx context.Context, accountID, scriptName, value string) error {
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

func buildDurableObjectScriptMultipart(worker workerScript, do durableObjectWorker, deployedClasses, inherited []string) ([]byte, string, error) {
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
	for _, mod := range append([]workerModule{worker.Main}, worker.Modules...) {
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
			return nil, fmt.Errorf("deployed script declares Durable Object class %q, which this build's migration log does not create", class)
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

func withSecret(worker workerScript, name, value string) workerScript {
	secrets := make(map[string]string, len(worker.Secrets)+1)
	for k, v := range worker.Secrets {
		secrets[k] = v
	}
	secrets[name] = value
	worker.Secrets = secrets
	return worker
}

func genericWorker(spec edge.StackSpec, program entryProgram, envelopeKey string) (workerScript, error) {
	slug := spec.Slug
	worker := withVar(
		withService(program.worker, genericStoreBinding, program.store.Name),
		genericSlugBinding,
		slug,
	)
	if envelopeKey != "" {
		worker = withSecret(worker, envelopeKeyBinding, envelopeKey)
	}
	if len(spec.Domains)+len(spec.ServedElsewhere) == 0 {
		worker = withVar(worker, genericServesWorkersDevBinding, "1")
	}
	if len(spec.DomainApps) > 0 {
		encoded, err := json.Marshal(spec.DomainApps)
		if err != nil {
			return workerScript{}, fmt.Errorf("encode the apps each domain of %s serves: %w", slug, err)
		}
		worker = withVar(worker, genericDomainAppsBinding, string(encoded))
	}
	return bindCodeLoader(worker), nil
}

func withService(worker workerScript, name, service string) workerScript {
	services := make(map[string]string, len(worker.Services)+1)
	for k, v := range worker.Services {
		services[k] = v
	}
	services[name] = service
	worker.Services = services
	return worker
}

func withVar(worker workerScript, name, value string) workerScript {
	variables := make(map[string]string, len(worker.Variables)+1)
	for k, v := range worker.Variables {
		variables[k] = v
	}
	variables[name] = value
	worker.Variables = variables
	return worker
}
