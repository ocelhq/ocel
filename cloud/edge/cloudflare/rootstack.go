// Root-stack reconcile and deployments-store operations (ADR 0001/0002). The
// deployments-store worker is a single shared worker provisioned once at
// bootstrap (see bootstrapStore in cloudflare.go); reconcile here deploys only
// a project's generic worker, service-bound to that shared store and carrying
// the project slug, and seeds the project's own store instance. The store
// operations are authenticated HTTP calls to the shared worker's fetch()
// surface, routed per project by slug (workers/deployments-store/src/index.ts).
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
	"strings"

	cf "github.com/cloudflare/cloudflare-go/v4"
	"github.com/cloudflare/cloudflare-go/v4/dns"
	"github.com/cloudflare/cloudflare-go/v4/option"
	"github.com/cloudflare/cloudflare-go/v4/workers"
	"github.com/cloudflare/cloudflare-go/v4/zones"

	"github.com/ocelhq/ocel/cloud/edge"
)

// bootstrapSecretBinding is the env name the shared deployments-store worker
// reads its account-level bootstrap credential from
// (workers/deployments-store/src/env.ts Env.BOOTSTRAP_SECRET).
const bootstrapSecretBinding = "BOOTSTRAP_SECRET"

// secretBytes is the byte length of a freshly minted credential (the bootstrap
// credential, a project secret, or an owner token), hex-encoded on the wire.
const secretBytes = 32

// The account-level worker script names, one pair per worker and one name per
// substrate class: production provisions the first, preview the second. They
// are distinct scripts so their Durable Object namespaces (which are
// script-scoped) never collide, letting the two substrates coexist in one
// account. Each is provisioned once at bootstrap.
const (
	sharedStoreScriptName  = "ocel-deployments-store"
	previewStoreScriptName = "ocel-deployments-store-preview"

	isrWriterScriptName        = "ocel-isr-writer"
	previewISRWriterScriptName = "ocel-isr-writer-preview"
)

// storeScriptNameFor returns the deployments-store worker script name for a
// substrate class.
func storeScriptNameFor(class edge.Class) (string, error) {
	return accountScriptNameFor("deployments store", class, sharedStoreScriptName, previewStoreScriptName)
}

// isrWriterScriptNameFor returns the ISR writer worker script name for a
// substrate class.
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

// durableObjectClass is one account-level worker's own Durable Object class, a
// binding putScript's generic edge.Worker-driven binding set has no concept of:
// it names a class the script itself exports rather than an external resource,
// and its migration is declared exactly once, on the bootstrap that first
// creates the class.
type durableObjectClass struct {
	binding      string
	className    string
	migrationTag string
}

// The two account-level Durable Object classes, each mirroring its worker's
// wrangler.jsonc.
var (
	deploymentsStoreDO = durableObjectClass{
		binding:      "DEPLOYMENTS_DO",
		className:    "DeploymentsStore",
		migrationTag: "v1",
	}
	isrWriterDO = durableObjectClass{
		binding:      "ISR_WRITER_DO",
		className:    "IsrDeploy",
		migrationTag: "v1",
	}
)

// genericStoreBinding is the env name the frozen generic worker reads its
// service binding to the shared deployments-store worker from
// (workers/nextjs/src/index.ts Env.DEPLOYMENTS), through which it resolves the
// active Deployment at request time.
const genericStoreBinding = "DEPLOYMENTS"

// genericSlugBinding is the env name the frozen generic worker reads the
// project slug from (workers/nextjs/src/index.ts Env.OCEL_SLUG), which it
// passes on every resolve RPC to address the project's own store instance.
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

	// A route is per hostname, not per project version, so an up-to-date root
	// stack still has route work: without it a second preview pointer would
	// never get a route.
	genericUp := upload{accountID: accountID, scriptName: spec.GenericName}
	if !upToDate {
		genericUp.worker = bindCodeLoader(bindObjectStore(
			withVar(withService(spec.Generic, genericStoreBinding, spec.StoreScriptName), genericSlugBinding, slug),
			spec.Values,
		))
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

// storeIdentity is a project's credentials for its own deployments-store
// instance: the owner token that proves the instance is this project's, and the
// secret every op but /initialize authenticates with.
type storeIdentity struct {
	secret     string
	ownerToken string
}

// ensureInstance brings the project's store instance to one this reconcile can
// write to, and reports whether the root stack already carries spec.Version.
//
// A project's first reconcile has no secret; a renamed project's prior names a
// different slug. Either way it mints fresh ownership and seeds a new instance —
// the slug is the project's durable identity, so renaming it forks a new project
// (fresh history), leaving the old instance orphaned.
//
// An instance that rejects the secret in state no longer holds it: a teardown
// wiped its storage and then failed before the state naming it could be
// forgotten. Re-seeding with the owner token in state is the recovery the store
// is built for — it rotates the secret for a matching owner and refuses a
// different one — so a wiped instance costs the project its promotion history,
// not its ability to ever deploy or tear down again. A state old enough to carry
// no owner token has nothing to prove the instance is this project's, so it can
// only seed a new one and let the store refuse a slug someone else owns.
func (p *provider) ensureInstance(ctx context.Context, spec edge.RootStackSpec, prior edge.RootStackState) (storeIdentity, bool, error) {
	id := storeIdentity{
		secret:     prior[edge.RootStackKeySecret],
		ownerToken: prior[edge.RootStackKeyOwnerToken],
	}
	reseed := false

	if id.secret != "" && prior[edge.RootStackKeySlug] == spec.Slug {
		current, res, err := p.getVersionStamp(ctx, spec.StoreEndpoint, spec.Slug, id.secret)
		switch {
		case err == nil:
			return id, current == spec.Version, nil
		case !unauthorized(res):
			return storeIdentity{}, false, fmt.Errorf("read root-stack version stamp: %w", err)
		case id.ownerToken != "":
			reseed = true
		}
	}

	if !reseed {
		minted, err := mintIdentity()
		if err != nil {
			return storeIdentity{}, false, err
		}
		id = minted
	}
	if err := p.initializeInstance(ctx, spec.StoreEndpoint, spec.Slug, spec.BootstrapCred, id.ownerToken, id.secret); err != nil {
		return storeIdentity{}, false, fmt.Errorf("initialize project store instance: %w", err)
	}
	return id, false, nil
}

// mintIdentity mints a project a fresh identity for a store instance it is
// about to seed.
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

// unauthorized reports whether a deployments-store call was rejected for its
// credential rather than failing outright — the instance holds no secret, or
// not the one presented.
func unauthorized(res *http.Response) bool {
	return res != nil && res.StatusCode == http.StatusUnauthorized
}

// DestroyRootStack deletes every worker in names — a project's generic
// worker(s) — first undoing each one's hostname attachments: detaching its
// custom-domain binding(s), and deleting the placeholder DNS records the route
// path planted (script deletion drops the routes themselves, but not those
// records). Records the user manages are left untouched. The shared
// deployments-store worker is never among them (it outlives any single project;
// a project's store data is reclaimed by DestroyInstance). It is best-effort: a
// failure on one worker does not stop the others, and every failure is joined
// into the returned error so the host can report exactly what remains.
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

// RemoveRoute deletes the worker route "<hostname>/*" off the named script,
// resolving the hostname's owning zone the way the deploy path does and touching
// no DNS record. A route that is already gone is not an error, so a re-run
// resumes (edge.RootStack).
func (p *provider) RemoveRoute(ctx context.Context, worker, hostname string) error {
	accountID := os.Getenv(envAccountID)
	if accountID == "" {
		return fmt.Errorf("%s is not set; it is required to remove a worker route", envAccountID)
	}
	zoneID, _, err := p.resolveZone(ctx, accountID, routeBaseDomain(hostname))
	if err != nil {
		return err
	}

	pattern := hostname + "/*"
	routes := p.client.Workers.Routes.ListAutoPaging(ctx, workers.RouteListParams{ZoneID: cf.F(zoneID)})
	for routes.Next() {
		route := routes.Current()
		if route.Script != worker || route.Pattern != pattern {
			continue
		}
		if _, err := p.client.Workers.Routes.Delete(ctx, route.ID, workers.RouteDeleteParams{ZoneID: cf.F(zoneID)}); err != nil {
			return fmt.Errorf("delete worker route %q: %w", pattern, err)
		}
	}
	if err := routes.Err(); err != nil {
		return fmt.Errorf("list worker routes in zone %s: %w", zoneID, err)
	}
	return nil
}

// ListDeployedWorkers returns the account's worker script names beginning with
// prefix. It pages the account's full script list and filters by name — the
// only project scoping a bare list offers — so the caller's prefix carries
// whatever collision guarantees the naming gives (edge.RootStack).
func (p *provider) ListDeployedWorkers(ctx context.Context, prefix string) ([]string, error) {
	accountID := os.Getenv(envAccountID)
	if accountID == "" {
		return nil, fmt.Errorf("%s is not set; it is required to list deployed workers", envAccountID)
	}
	var names []string
	iter := p.client.Workers.Scripts.ListAutoPaging(ctx, workers.ScriptListParams{AccountID: cf.F(accountID)})
	for iter.Next() {
		if name := iter.Current().ID; strings.HasPrefix(name, prefix) {
			names = append(names, name)
		}
	}
	if err := iter.Err(); err != nil {
		return nil, fmt.Errorf("list workers: %w", err)
	}
	return names, nil
}

// detachRouteRecords deletes the proxied placeholder DNS records the route path
// planted for a worker script. Worker routes are zone-scoped and dropped when
// the script is deleted, but the records that make their wildcard hostnames
// resolve are not — so, before the script goes, this finds every route bound to
// the script across the account's zones and deletes the Ocel-owned placeholder
// (a proxied AAAA to the discard prefix) for each route's hostname. A record the
// user manages at the same name is left in place.
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

// deleteProxiedRecord removes the Ocel-owned placeholder record at hostname: the
// proxied AAAA to the discard prefix that ensureProxiedRecord plants. It matches
// on that content so a record the user manages at the same name is never
// deleted.
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

// DestroyInstance wipes the project's own instance in the shared
// deployments-store worker, authenticated with the project secret in state.
// A project that never deployed to production (no secret in state) is a no-op,
// which also makes `ocel destroy` safe to re-run: the per-project state is
// deleted after this succeeds, so a re-run reads empty state and skips.
//
// An instance that rejects the secret is reported destroyed. Wiping an instance
// deletes the very secret this call authenticates with, so a rejected credential
// cannot be told apart from an instance a previous run already wiped — and
// either way there is nothing left here for this project to destroy. Treating it
// as a failure is what would strand a teardown that failed after the wipe: the
// state naming the instance is only forgotten once the teardown reports the
// instance gone, so every re-run would fail on the same already-done step.
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

// detachCustomDomains removes every custom-domain binding attached to a worker
// script, unbinding the worker from those hostnames without deleting the DNS
// records the account owns for them (Workers.Domains.Delete detaches the
// route only).
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

// deleteScript removes a worker script, forcing deletion through any bindings
// it owns. A script that is already gone is treated as success, so
// DestroyRootStack is safe to re-run.
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

// putDurableObjectScript uploads an account-level worker that owns a Durable
// Object class of its own (bootstrapStore and bootstrapISRWriter in
// cloudflare.go): like putScript, but it additionally binds that class and,
// only when migrate is true (the bootstrap that first creates it), declares its
// one SQLite-backed migration. Redeclaring that migration on a later bootstrap
// would be at best redundant and at worst rejected, so every bootstrap after
// the first omits it.
func (p *provider) putDurableObjectScript(ctx context.Context, up upload, do durableObjectClass, migrate bool) error {
	body, contentType, err := buildDurableObjectScriptMultipart(up.worker, do, migrate)
	if err != nil {
		return err
	}
	_, err = p.client.Workers.Scripts.Update(ctx, up.scriptName, workers.ScriptUpdateParams{
		AccountID: cf.F(up.accountID),
	}, option.WithRequestBody(contentType, body))
	return err
}

// buildDurableObjectScriptMultipart is buildScriptMultipart's counterpart for
// the account-level workers: the same module/binding shape, plus the worker's
// own Durable Object binding and, when migrate is true, its migration
// declaration.
func buildDurableObjectScriptMultipart(worker edge.Worker, do durableObjectClass, migrate bool) ([]byte, string, error) {
	bindings := append(scriptBindings(worker, false), map[string]any{
		"type":       "durable_object_namespace",
		"name":       do.binding,
		"class_name": do.className,
	})
	metadata := map[string]any{
		"main_module":         worker.Main.Name,
		"compatibility_date":  compatDate,
		"compatibility_flags": compatFlags,
		"observability":       observability,
		"bindings":            bindings,
	}
	if migrate {
		metadata["migrations"] = map[string]any{
			"tag":                do.migrationTag,
			"new_sqlite_classes": []string{do.className},
		}
	}
	metadataJSON, err := json.Marshal(metadata)
	if err != nil {
		return nil, "", fmt.Errorf("marshal %s worker metadata: %w", do.className, err)
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

func (p *provider) PutStaged(ctx context.Context, state edge.RootStackState, record edge.DeploymentRecord) error {
	_, err := p.storeRequest(ctx, state, http.MethodPut, "/staged", record, nil)
	return err
}

// promoteBody matches the store's `Promotion & { pointer?: string }` /promote
// body. An empty pointer is omitted so the store applies its reserved
// production default.
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

func (p *provider) RemovePointer(ctx context.Context, state edge.RootStackState, pointer string) (edge.PointerRemoval, error) {
	var result edge.PointerRemoval
	if _, err := p.storeRequest(ctx, state, http.MethodPost, "/remove-pointer", map[string]string{"pointer": pointer}, &result); err != nil {
		return edge.PointerRemoval{}, err
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

// initializeInstance seeds the project's own instance in the shared
// deployments-store worker with its owner token and secret, authenticated with
// the account-level bootstrap credential. force is false: the deploy host
// never silently adopts an instance already owned by a different project (a
// slug collision), which the store surfaces as a 409.
func (p *provider) initializeInstance(ctx context.Context, endpoint, slug, bootstrapCred, ownerToken, secret string) error {
	body := map[string]any{"ownerToken": ownerToken, "secret": secret, "force": false}
	_, err := p.storeRequestTo(ctx, endpoint, slug, bootstrapCred, http.MethodPost, "/initialize", body, nil)
	return err
}

// getVersionStamp reads the version the instance's root stack last deployed. It
// returns the response alongside the error so a caller can tell a rejected
// credential (unauthorized) from a store it could not reach at all.
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

// storeRequest issues an authenticated call against the project's own instance
// in the shared deployments-store worker, addressed by the endpoint, slug and
// secret in state.
func (p *provider) storeRequest(ctx context.Context, state edge.RootStackState, method, subpath string, body, out any) (*http.Response, error) {
	return p.storeRequestTo(ctx, state[edge.RootStackKeyEndpoint], state[edge.RootStackKeySlug], state[edge.RootStackKeySecret], method, subpath, body, out)
}

// storeRequestTo issues one authenticated HTTP call to the shared
// deployments-store worker's fetch() surface, routed to one project's instance
// by slug (/<slug>/<subpath>), matching workers/deployments-store/src/index.ts:
// a Bearer credential, a JSON body when body is non-nil, and a JSON response
// decoded into out when out is non-nil. A non-2xx status is an error naming the
// path and status.
func (p *provider) storeRequestTo(ctx context.Context, endpoint, slug, secret, method, subpath string, body, out any) (*http.Response, error) {
	if endpoint == "" {
		return nil, fmt.Errorf("deployments store: no endpoint; bootstrap the edge first")
	}
	if slug == "" {
		return nil, fmt.Errorf("deployments store: no project slug")
	}

	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return nil, fmt.Errorf("marshal deployments-store request body: %w", err)
		}
		reader = bytes.NewReader(encoded)
	}

	req, err := http.NewRequestWithContext(ctx, method, endpoint+"/"+slug+subpath, reader)
	if err != nil {
		return nil, fmt.Errorf("build deployments-store request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+secret)
	if body != nil {
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

// mintSecret generates a fresh random credential (the account-level bootstrap
// credential, a per-project secret, or an owner token), hex-encoded so it is
// safe to carry as a plain HTTP header value.
func mintSecret() (string, error) {
	buf := make([]byte, secretBytes)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}

// withSecret returns worker with one additional secret_text binding, leaving
// the caller's Worker untouched.
func withSecret(worker edge.Worker, name, value string) edge.Worker {
	secrets := make(map[string]string, len(worker.Secrets)+1)
	for k, v := range worker.Secrets {
		secrets[k] = v
	}
	secrets[name] = value
	worker.Secrets = secrets
	return worker
}

// withService returns worker with one additional service binding, leaving
// the caller's Worker untouched.
func withService(worker edge.Worker, name, service string) edge.Worker {
	services := make(map[string]string, len(worker.Services)+1)
	for k, v := range worker.Services {
		services[k] = v
	}
	services[name] = service
	worker.Services = services
	return worker
}

// withVar returns worker with one additional plain-text var binding, leaving
// the caller's Worker untouched.
func withVar(worker edge.Worker, name, value string) edge.Worker {
	vars := make(map[string]string, len(worker.Vars)+1)
	for k, v := range worker.Vars {
		vars[k] = v
	}
	vars[name] = value
	worker.Vars = vars
	return worker
}
