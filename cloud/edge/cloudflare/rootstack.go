package cloudflare

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"slices"
	"strings"

	cf "github.com/cloudflare/cloudflare-go/v4"
	"github.com/cloudflare/cloudflare-go/v4/dns"
	"github.com/cloudflare/cloudflare-go/v4/option"
	"github.com/cloudflare/cloudflare-go/v4/workers"
	"github.com/cloudflare/cloudflare-go/v4/zones"

	"github.com/ocelhq/ocel/cloud/edge"
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

	id, upToDate, err := p.ensureInstance(ctx, spec, prior)
	if err != nil {
		return nil, err
	}

	genericUp := upload{accountID: accountID, scriptName: spec.GenericName}
	if !upToDate {
		genericUp.worker = genericWorker(spec, slug)
		assetsJWT, err := p.uploadAssets(ctx, genericUp)
		if err != nil {
			return nil, fmt.Errorf("upload generic worker assets: %w", err)
		}
		if err := p.putScript(ctx, genericUp, assetsJWT); err != nil {
			return nil, fmt.Errorf("put generic worker: %w", err)
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

	if _, err := p.setSubdomain(ctx, genericUp, len(spec.Domains) == 0); err != nil {
		return nil, fmt.Errorf("set generic worker subdomain: %w", err)
	}
	if err := p.putVersionStamp(ctx, endpoint, slug, id.secret, spec.Version); err != nil {
		return nil, fmt.Errorf("set root-stack version stamp: %w", err)
	}

	return edge.RootStackState{
		edge.RootStackKeySlug:       slug,
		edge.RootStackKeyEndpoint:   endpoint,
		edge.RootStackKeySecret:     id.secret,
		edge.RootStackKeyOwnerToken: id.ownerToken,
	}, nil
}

type storeIdentity struct {
	secret     string
	ownerToken string
}

func (p *provider) ensureInstance(ctx context.Context, spec edge.RootStackSpec, prior edge.RootStackState) (storeIdentity, bool, error) {
	if secret := prior[edge.RootStackKeySecret]; secret != "" && prior[edge.RootStackKeySlug] == spec.Slug {
		current, res, err := p.getVersionStamp(ctx, spec.StoreEndpoint, spec.Slug, secret)
		switch {
		case err == nil:
			return storeIdentity{secret: secret, ownerToken: prior[edge.RootStackKeyOwnerToken]}, current == spec.Version, nil
		case !unauthorized(res):
			return storeIdentity{}, false, fmt.Errorf("read root-stack version stamp: %w", err)
		}
	}

	minted, err := mintIdentity()
	if err != nil {
		return storeIdentity{}, false, err
	}
	adopted, err := p.initializeInstance(ctx, spec.StoreEndpoint, spec.Slug, spec.BootstrapCred, minted)
	if err != nil {
		return storeIdentity{}, false, fmt.Errorf("initialize project store instance: %w", err)
	}
	return adopted, false, nil
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

func unauthorized(res *http.Response) bool {
	return res != nil && res.StatusCode == http.StatusUnauthorized
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

	var errs []error
	for _, name := range names {
		if name == "" {
			continue
		}
		if err := p.detachCustomDomains(ctx, accountID, name); err != nil {
			errs = append(errs, err)
		}
		if err := p.detachRouteRecords(ctx, accountID, name); err != nil {
			errs = append(errs, err)
		}
		if err := p.deleteScript(ctx, accountID, name); err != nil {
			errs = append(errs, fmt.Errorf("delete worker %q: %w", name, err))
		}
	}
	return errors.Join(errs...)
}

func (p *provider) detachRouteRecords(ctx context.Context, accountID, scriptName string) error {
	owned := p.client.Zones.ListAutoPaging(ctx, zones.ZoneListParams{
		Account: cf.F(zones.ZoneListParamsAccount{ID: cf.F(accountID)}),
	})
	var errs []error
	for owned.Next() {
		zoneID := owned.Current().ID
		routes := p.client.Workers.Routes.ListAutoPaging(ctx, workers.RouteListParams{ZoneID: cf.F(zoneID)})
		for routes.Next() {
			route := routes.Current()
			if route.Script != scriptName {
				continue
			}
			hostname := strings.TrimSuffix(route.Pattern, "/*")
			if err := p.deleteProxiedRecord(ctx, zoneID, hostname); err != nil {
				errs = append(errs, err)
			}
		}
		if err := routes.Err(); err != nil {
			errs = append(errs, fmt.Errorf("list worker routes in zone %s: %w", zoneID, err))
		}
	}
	if err := owned.Err(); err != nil {
		errs = append(errs, fmt.Errorf("list zones: %w", err))
	}
	return errors.Join(errs...)
}

func (p *provider) deleteProxiedRecord(ctx context.Context, zoneID, hostname string) error {
	records := p.client.DNS.Records.ListAutoPaging(ctx, dns.RecordListParams{
		ZoneID: cf.F(zoneID),
		Name:   cf.F(dns.RecordListParamsName{Exact: cf.F(hostname)}),
		Type:   cf.F(dns.RecordListParamsTypeAAAA),
	})
	for records.Next() {
		rec := records.Current()
		if rec.Content != routeRecordContent {
			continue
		}
		if _, err := p.client.DNS.Records.Delete(ctx, rec.ID, dns.RecordDeleteParams{ZoneID: cf.F(zoneID)}); err != nil {
			return fmt.Errorf("delete DNS record %q: %w", hostname, err)
		}
	}
	if err := records.Err(); err != nil {
		return fmt.Errorf("list DNS records for %q: %w", hostname, err)
	}
	return nil
}

func (p *provider) DestroyInstance(ctx context.Context, state edge.RootStackState) error {
	if state[edge.RootStackKeySecret] == "" {
		return nil
	}
	res, err := p.storeRequest(ctx, state, http.MethodPost, "/destroy", nil, nil)
	if unauthorized(res) {
		return nil
	}
	return err
}

func (p *provider) detachCustomDomains(ctx context.Context, accountID, scriptName string) error {
	attached := p.client.Workers.Domains.ListAutoPaging(ctx, workers.DomainListParams{
		AccountID: cf.F(accountID),
		Service:   cf.F(scriptName),
	})
	for attached.Next() {
		dom := attached.Current()
		if err := p.client.Workers.Domains.Delete(ctx, dom.ID, workers.DomainDeleteParams{
			AccountID: cf.F(accountID),
		}); err != nil {
			return fmt.Errorf("detach custom domain %q: %w", dom.Hostname, err)
		}
	}
	if err := attached.Err(); err != nil {
		return fmt.Errorf("list custom domains for %q: %w", scriptName, err)
	}
	return nil
}

func (p *provider) deleteScript(ctx context.Context, accountID, scriptName string) error {
	_, err := p.client.Workers.Scripts.Delete(ctx, scriptName, workers.ScriptDeleteParams{
		AccountID: cf.F(accountID),
		Force:     cf.F(true),
	})
	if hasStatus(err, http.StatusNotFound) {
		return nil
	}
	return err
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

func (p *provider) PutStaged(ctx context.Context, state edge.RootStackState, record edge.DeploymentRecord) error {
	_, err := p.storeRequest(ctx, state, http.MethodPut, "/staged", record, nil)
	return err
}

type promoteBody struct {
	edge.Promotion
	Pointer string `json:"pointer,omitempty"`
}

func (p *provider) Promote(ctx context.Context, state edge.RootStackState, promotion edge.Promotion, pointer string) error {
	_, err := p.storeRequest(ctx, state, http.MethodPost, "/promote", promoteBody{Promotion: promotion, Pointer: pointer}, nil)
	return err
}

func (p *provider) History(ctx context.Context, state edge.RootStackState, pointer string) ([]edge.HistoryEntry, error) {
	subpath := "/history"
	if pointer != "" {
		subpath += "?pointer=" + url.QueryEscape(pointer)
	}
	var history []edge.HistoryEntry
	if _, err := p.storeRequest(ctx, state, http.MethodGet, subpath, nil, &history); err != nil {
		return nil, err
	}
	return history, nil
}

func (p *provider) RemovePointer(ctx context.Context, state edge.RootStackState, pointer string) (edge.PruneResult, error) {
	var result edge.PruneResult
	if _, err := p.storeRequest(ctx, state, http.MethodPost, "/remove-pointer", map[string]string{"pointer": pointer}, &result); err != nil {
		return edge.PruneResult{}, err
	}
	return result, nil
}

func (p *provider) DeletePromotionArtifacts(ctx context.Context, state edge.RootStackState, keepN int, pointer string) (edge.PruneResult, error) {
	body := map[string]any{"keepN": keepN}
	if pointer != "" {
		body["pointer"] = pointer
	}
	var result edge.PruneResult
	if _, err := p.storeRequest(ctx, state, http.MethodPost, "/prune", body, &result); err != nil {
		return edge.PruneResult{}, err
	}
	return result, nil
}

func (p *provider) initializeInstance(ctx context.Context, endpoint, slug, bootstrapCred string, present storeIdentity) (storeIdentity, error) {
	body := map[string]any{"ownerToken": present.ownerToken, "secret": present.secret, "force": false}
	var out struct {
		OwnerToken string `json:"ownerToken"`
		Secret     string `json:"secret"`
	}
	if _, err := p.storeRequestTo(ctx, endpoint, slug, bootstrapCred, http.MethodPost, "/initialize", body, &out); err != nil {
		return storeIdentity{}, err
	}
	if out.Secret == "" || out.OwnerToken == "" {
		return storeIdentity{}, fmt.Errorf("deployments store reported no identity for %q", slug)
	}
	return storeIdentity{secret: out.Secret, ownerToken: out.OwnerToken}, nil
}

func (p *provider) getVersionStamp(ctx context.Context, endpoint, slug, secret string) (string, *http.Response, error) {
	var out struct {
		Version *string `json:"version"`
	}
	res, err := p.storeRequestTo(ctx, endpoint, slug, secret, http.MethodGet, "/version-stamp", nil, &out)
	if err != nil {
		return "", res, err
	}
	if out.Version == nil {
		return "", res, nil
	}
	return *out.Version, res, nil
}

func (p *provider) putVersionStamp(ctx context.Context, endpoint, slug, secret, version string) error {
	_, err := p.storeRequestTo(ctx, endpoint, slug, secret, http.MethodPut, "/version-stamp", map[string]string{"version": version}, nil)
	return err
}

func (p *provider) storeRequest(ctx context.Context, state edge.RootStackState, method, subpath string, body, out any) (*http.Response, error) {
	return p.storeRequestTo(ctx, state[edge.RootStackKeyEndpoint], state[edge.RootStackKeySlug], state[edge.RootStackKeySecret], method, subpath, body, out)
}

func (p *provider) storeRequestTo(ctx context.Context, endpoint, slug, secret, method, subpath string, body, out any) (*http.Response, error) {
	if endpoint == "" {
		return nil, fmt.Errorf("deployments store: no endpoint; bootstrap the edge first")
	}
	if slug == "" {
		return nil, fmt.Errorf("deployments store: no project slug")
	}

	var encoded []byte
	if body != nil {
		marshalled, err := json.Marshal(body)
		if err != nil {
			return nil, fmt.Errorf("marshal deployments-store request body: %w", err)
		}
		encoded = marshalled
	}

	var res *http.Response
	var err error
	for attempt := 0; ; attempt++ {
		res, err = p.storeAttempt(ctx, endpoint, slug, secret, method, subpath, encoded, out)
		if attempt == storeMaxAttempts-1 || !storeRetryable(res, err) {
			return res, err
		}
		if waitErr := waitBeforeRetry(ctx, storeRetryDelay(res, attempt, retryJitter())); waitErr != nil {
			return res, err
		}
	}
}

func (p *provider) storeAttempt(ctx context.Context, endpoint, slug, secret, method, subpath string, encoded []byte, out any) (*http.Response, error) {
	var reader io.Reader
	if encoded != nil {
		reader = bytes.NewReader(encoded)
	}
	req, err := http.NewRequestWithContext(ctx, method, endpoint+"/"+slug+subpath, reader)
	if err != nil {
		return nil, fmt.Errorf("build deployments-store request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+secret)
	if encoded != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("call deployments store %s %s: %w", method, subpath, err)
	}
	defer res.Body.Close()

	if res.StatusCode < 200 || res.StatusCode >= 300 {
		respBody, _ := io.ReadAll(res.Body)
		return res, fmt.Errorf("deployments store %s %s: status %d: %s", method, subpath, res.StatusCode, string(respBody))
	}
	if out != nil {
		if err := json.NewDecoder(res.Body).Decode(out); err != nil {
			return res, fmt.Errorf("decode deployments store %s %s response: %w", method, subpath, err)
		}
	}
	return res, nil
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
