package cloudflare

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"

	cf "github.com/cloudflare/cloudflare-go/v4"
	"github.com/cloudflare/cloudflare-go/v4/option"
	"github.com/cloudflare/cloudflare-go/v4/workers"

	"github.com/ocelhq/ocel/pkg/buildoutput"
	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/tracing/content"
)

const bootstrapSecretBinding = "BOOTSTRAP_SECRET"

const secretBytes = 32

const releasesStoreStem = "releases-store"

func (n originNames) storeScript(tier environment.Tier) (string, error) {
	return n.accountName("releases store", tier, releasesStoreStem)
}

func refuseUnknownTier(what string, tier environment.Tier) error {
	if !slices.Contains(workerTiers, tier) {
		return fmt.Errorf("%s: unknown tier %q", what, tier)
	}
	return nil
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
	EntryWorker string   `json:"entryWorker,omitempty"`
	EnvelopeKey string   `json:"envelopeKey,omitempty"`
	Routed      []string `json:"routed,omitempty"`
}

func (own private) wrapsEnvelopes() bool {
	return own.EnvelopeKey != "" && own.EntryWorker != ""
}

type stack struct {
	p     *cloudflare
	state edge.StackState
	own   private
}

func (s *stack) State() edge.StackState { return withOwn(s.state, s.own) }

func withOwn(state edge.StackState, own private) edge.StackState {
	state.Private = edge.Own(own)
	return state
}

func (p *cloudflare) Open(state edge.StackState) (edge.EdgeStack, error) {
	s := &stack{p: p, state: state}
	if err := state.Private.Into(&s.own); err != nil {
		return nil, err
	}
	return s, nil
}

func (p *cloudflare) Reconcile(ctx context.Context, spec edge.StackSpec, prior edge.StackState) (edge.EdgeStack, error) {
	accountID, err := p.readTierAccount(ctx, spec.Tier, "reconcile the Cloudflare stack")
	if err != nil {
		return nil, err
	}
	names, err := p.resolveNames(ctx)
	if err != nil {
		return nil, err
	}
	program, err := buildEntryProgram(names, spec.Tier, spec.Slug, spec.Program)
	if err != nil {
		return nil, err
	}
	if !spec.PruneOnly {
		if err := p.refuseBootstrapBehind(ctx, accountID, names, spec.Tier, bootstrapCaller{worker: "the entry worker this deploy uploads", retry: "deploy again"}); err != nil {
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
	routed := recorded.Routed
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

	secret, deployedStamp, err := p.ensureInstance(ctx, spec, program.store, prior)
	if err != nil {
		return nil, err
	}
	held := edge.StackState{Endpoint: endpoint, Slug: slug, Secret: secret, Tier: spec.Tier}
	if err := p.putGate(ctx, held, spec.Gate); err != nil {
		return nil, fmt.Errorf("publish the %s gate of %s: %w", spec.Tier, slug, err)
	}
	opened := func(state edge.StackState) (edge.EdgeStack, error) {
		own := private{EnvelopeKey: envelopeKey, Routed: routed}
		if !spec.PruneOnly {
			own.EntryWorker = program.name
		}
		return &stack{p: p, state: state, own: own}, nil
	}
	openedAsDeployed := func(state edge.StackState) (edge.EdgeStack, error) {
		switch {
		case spec.GlobalPreview != "":
			purgeURL, err := p.readSharedPreviewPurgeURL(ctx, accountID, names, slug)
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
	upToDate := deployedStamp == stamp
	if upToDate && p.skipChecks {
		return openedAsDeployed(prior)
	}

	if spec.PruneOnly && program.shared {
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

	owns, err := names.ownsScript(slug)
	if err != nil {
		return nil, err
	}
	if err := p.reconcileWorkerRoutes(ctx, names, genericUp, routeSpec{
		desired:         spec.Domains,
		bound:           prior.Bound,
		servedElsewhere: spec.ServedElsewhere,
		routed:          recorded.Routed,
		owns:            owns,
	}, spec.Warn); err != nil {
		return nil, err
	}
	routed = slices.Clone(spec.Domains)
	if upToDate {
		return openedAsDeployed(prior)
	}

	if spec.PruneOnly {
		if err := p.deleteScript(ctx, accountID, program.name); err != nil {
			return nil, fmt.Errorf("delete retired generic worker %q: %w", program.name, err)
		}
		if spec.GlobalPreview != "" {
			if purgeURL, err = p.readSharedPreviewPurgeURL(ctx, accountID, names, slug); err != nil {
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
	if err := p.putVersionStamp(ctx, endpoint, slug, secret, stamp); err != nil {
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

func (p *cloudflare) ensureInstance(ctx context.Context, spec edge.StackSpec, store edge.ReleasesStore, prior edge.StackState) (secret, stamp string, err error) {
	if secret := prior.Secret; secret != "" && prior.Slug == spec.Slug {
		current, res, err := p.getVersionStamp(ctx, store.Endpoint, spec.Slug, secret)
		switch {
		case err == nil:
			return secret, current, nil
		case !isUnauthorized(res):
			return "", "", fmt.Errorf("read stack version stamp: %w", err)
		}
	}

	secret, err = mintSecret()
	if err != nil {
		return "", "", fmt.Errorf("mint project store secret: %w", err)
	}
	if err := p.initializeInstance(ctx, store.Endpoint, spec.Slug, store.BootstrapCredential, secret); err != nil {
		return "", "", fmt.Errorf("initialize project store instance: %w", err)
	}
	return secret, "", nil
}

func (s *stack) Destroy(ctx context.Context) error {
	accountID, err := s.p.readTierAccount(ctx, s.state.Tier, "destroy the Cloudflare stack")
	if err != nil {
		return err
	}
	names, err := s.p.resolveNames(ctx)
	if err != nil {
		return err
	}
	var errs []error
	for _, hostname := range s.state.Bound {
		if err := s.unbindDomain(ctx, names, accountID, hostname); err != nil {
			errs = append(errs, fmt.Errorf("unbind %q before destroying the stack that serves it: %w", hostname, err))
		}
	}
	workers, err := s.stackWorkers(ctx, names)
	if err != nil {
		return errors.Join(append(errs, err)...)
	}
	if err := s.p.destroyWorkers(ctx, accountID, workers); err != nil {
		return errors.Join(append(errs, err)...)
	}
	if err := s.p.destroyInstance(ctx, s.state); err != nil {
		errs = append(errs, fmt.Errorf("destroy releases-store instance: %w", err))
	}
	return errors.Join(errs...)
}

func (s *stack) stackWorkers(ctx context.Context, names originNames) ([]string, error) {
	if secret := s.state.Secret; secret != "" {
		if _, res, err := s.p.getVersionStamp(ctx, s.state.Endpoint, s.state.Slug, secret); err != nil {
			if isUnauthorized(res) {
				return nil, fmt.Errorf("read stack version stamp: the releases store rejected project %q's secret, so this state is not the one that deployed its workers: %w", s.state.Slug, err)
			}
			return nil, fmt.Errorf("read stack version stamp: %w", err)
		}
	}
	named := []string{}
	if s.own.EntryWorker != "" {
		named = append(named, s.own.EntryWorker)
	}
	if s.state.Slug == "" || s.state.Tier == "" {
		return named, nil
	}
	conventional, err := names.entryWorker(s.state.Slug, s.state.Tier)
	if err != nil {
		return nil, err
	}
	if !slices.Contains(named, conventional) {
		named = append(named, conventional)
	}
	return named, nil
}

func (p *cloudflare) destroyWorkers(ctx context.Context, accountID string, names []string) error {
	var errs []error
	for _, name := range names {
		if name == "" {
			continue
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
	_, err := p.client.Workers.Scripts.Secrets.Update(content.Omit(ctx), scriptName, workers.ScriptSecretUpdateParams{
		AccountID: cf.F(accountID),
		Body: workers.ScriptSecretUpdateParamsBodyWorkersBindingKindSecretText{
			Name: cf.F(bootstrapSecretBinding),
			Text: cf.F(value),
			Type: cf.F(workers.ScriptSecretUpdateParamsBodyWorkersBindingKindSecretTextTypeSecretText),
		},
	})
	return err
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
