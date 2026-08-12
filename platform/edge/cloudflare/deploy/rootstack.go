package cloudflare

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
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
		return "", fmt.Errorf("%s: unknown substrate class %q", worker, class)
	}
}

type durableObjectClass struct {
	binding   string
	className string
}

const durableObjectBindingType = "durable_object_namespace"

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

func (p *provider) ReconcileRootStack(ctx context.Context, spec edge.RootStackSpec, prior edge.RootStackState) (edge.RootStackState, error) {
	accountID := os.Getenv(envAccountID)
	if accountID == "" {
		return nil, fmt.Errorf("%s is not set; it is required to reconcile the Cloudflare root stack", envAccountID)
	}

	slug := spec.Slug
	endpoint := spec.StoreEndpoint

	generic := genericWorker(spec, slug)
	stamp, err := specStamp(spec, generic)
	if err != nil {
		return nil, err
	}

	id, stamps, err := p.ensureInstance(ctx, spec, prior)
	if err != nil {
		return nil, err
	}
	upToDate := stamps[spec.GenericName] == stamp
	if upToDate && skipEdgeReconcile() {
		return prior, nil
	}

	if spec.PruneOnly && spec.GenericName == edge.SharedPreviewEntryScript {
		return nil, fmt.Errorf("prune-only root stack may not target the shared preview entry worker %q", spec.GenericName)
	}

	genericUp := upload{accountID: accountID, scriptName: spec.GenericName}
	if !upToDate && !spec.PruneOnly {
		genericUp.worker = generic
		if err := p.putWorkerScript(ctx, genericUp, "generic worker"); err != nil {
			return nil, err
		}
	}

	if err := p.reconcileWorkerRoutes(ctx, genericUp, routePlan{
		desired:        spec.Domains,
		prune:          spec.PruneRoutes,
		pruneStem:      spec.PruneWorkerStem,
		requiredRecord: spec.RequiredRecord,
	}, spec.Warn); err != nil {
		return nil, err
	}
	if upToDate {
		return prior, nil
	}

	if spec.PruneOnly {
		if err := p.deleteScript(ctx, accountID, spec.GenericName); err != nil {
			return nil, fmt.Errorf("delete retired generic worker %q: %w", spec.GenericName, err)
		}
	} else if _, err := p.setSubdomain(ctx, genericUp, len(spec.Domains) == 0); err != nil {
		return nil, fmt.Errorf("set generic worker subdomain: %w", err)
	}
	stamps[spec.GenericName] = stamp
	encoded, err := stamps.encode()
	if err != nil {
		return nil, err
	}
	if err := p.putVersionStamp(ctx, endpoint, slug, id.secret, encoded); err != nil {
		return nil, fmt.Errorf("set root-stack version stamp: %w", err)
	}

	return edge.RootStackState{
		edge.RootStackKeySlug:       slug,
		edge.RootStackKeyEndpoint:   endpoint,
		edge.RootStackKeySecret:     id.secret,
		edge.RootStackKeyOwnerToken: id.ownerToken,
	}, nil
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

func (p *provider) ensureInstance(ctx context.Context, spec edge.RootStackSpec, prior edge.RootStackState) (storeIdentity, stampSet, error) {
	if secret := prior[edge.RootStackKeySecret]; secret != "" && prior[edge.RootStackKeySlug] == spec.Slug {
		current, res, err := p.getVersionStamp(ctx, spec.StoreEndpoint, spec.Slug, secret)
		switch {
		case err == nil:
			return storeIdentity{secret: secret, ownerToken: prior[edge.RootStackKeyOwnerToken]}, decodeStampSet(current), nil
		case !unauthorized(res):
			return storeIdentity{}, nil, fmt.Errorf("read root-stack version stamp: %w", err)
		}
	}

	minted, err := mintIdentity()
	if err != nil {
		return storeIdentity{}, nil, err
	}
	adopted, err := p.initializeInstance(ctx, spec.StoreEndpoint, spec.Slug, spec.BootstrapCred, minted)
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

func (p *provider) ListDeployedWorkers(ctx context.Context, stem string) ([]string, error) {
	accountID := os.Getenv(envAccountID)
	if accountID == "" {
		return nil, fmt.Errorf("%s is not set; it is required to list deployed workers", envAccountID)
	}
	var names []string
	iter := p.client.Workers.Scripts.ListAutoPaging(ctx, workers.ScriptListParams{AccountID: cf.F(accountID)})
	for iter.Next() {
		if name := iter.Current().ID; edge.NameUnderStem(stem, name) {
			names = append(names, name)
		}
	}
	if err := iter.Err(); err != nil {
		return nil, fmt.Errorf("list workers: %w", err)
	}
	return names, nil
}

func (p *provider) DestroyRootStack(ctx context.Context, names []string) error {
	accountID := os.Getenv(envAccountID)
	if accountID == "" {
		return fmt.Errorf("%s is not set; it is required to destroy the Cloudflare root stack", envAccountID)
	}

	routes := p.routeSnapshot()
	var errs []error
	for _, name := range names {
		if name == "" {
			continue
		}
		if err := p.detachCustomDomains(ctx, accountID, name); err != nil {
			errs = append(errs, err)
		}
		if err := p.detachRouteRecords(ctx, routes, accountID, name); err != nil {
			errs = append(errs, err)
		}
		if err := p.deleteScript(ctx, accountID, name); err != nil {
			errs = append(errs, fmt.Errorf("delete worker %q: %w", name, err))
		}
	}
	return errors.Join(errs...)
}

func (p *provider) putDurableObjectScript(ctx context.Context, up upload, do durableObjectWorker, deployedClasses []string) error {
	body, contentType, err := buildDurableObjectScriptMultipart(up.worker, do, deployedClasses)
	if err != nil {
		return err
	}
	_, err = p.client.Workers.Scripts.Update(ctx, up.scriptName, workers.ScriptUpdateParams{
		AccountID: cf.F(up.accountID),
	}, option.WithRequestBody(contentType, body))
	return err
}

func buildDurableObjectScriptMultipart(worker edge.Worker, do durableObjectWorker, deployedClasses []string) ([]byte, string, error) {
	bindings := scriptBindings(worker, false)
	for _, class := range do.classes {
		bindings = append(bindings, map[string]any{
			"type":       durableObjectBindingType,
			"name":       class.binding,
			"class_name": class.className,
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

func genericWorker(spec edge.RootStackSpec, slug string) edge.Worker {
	worker := withVar(
		withService(spec.Generic, genericStoreBinding, spec.StoreScriptName),
		genericSlugBinding,
		slug,
	)
	if spec.ISRWriterScriptName != "" {
		worker = withService(worker, genericISRWriterBinding, spec.ISRWriterScriptName)
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
