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
	"os"

	cf "github.com/cloudflare/cloudflare-go/v4"
	"github.com/cloudflare/cloudflare-go/v4/option"
	"github.com/cloudflare/cloudflare-go/v4/workers"

	"github.com/ocelhq/ocel/cloud/edge"
)

// bootstrapSecretBinding is the env name the shared deployments-store worker
// reads its account-level bootstrap credential from
// (workers/deployments-store/src/env.ts Env.BOOTSTRAP_SECRET).
const bootstrapSecretBinding = "BOOTSTRAP_SECRET"

// secretBytes is the byte length of a freshly minted credential (the bootstrap
// credential, a project secret, or an owner token), hex-encoded on the wire.
const secretBytes = 32

// sharedStoreScriptName / previewStoreScriptName are the deployments-store
// worker script names, one per substrate class: production provisions
// sharedStoreScriptName, preview provisions previewStoreScriptName. They are
// distinct scripts so their Durable Object namespaces (which are script-scoped)
// never collide, letting the two substrates coexist in one account. Each is
// provisioned once at bootstrap and service-bound by that substrate's projects.
const (
	sharedStoreScriptName  = "ocel-deployments-store"
	previewStoreScriptName = "ocel-deployments-store-preview"
)

// storeScriptNameFor returns the deployments-store worker script name for a
// substrate class.
func storeScriptNameFor(class edge.Class) (string, error) {
	switch class {
	case edge.ClassProduction:
		return sharedStoreScriptName, nil
	case edge.ClassPreview:
		return previewStoreScriptName, nil
	default:
		return "", fmt.Errorf("deployments store: unknown substrate class %q", class)
	}
}

// The shared deployments-store worker's own Durable Object binding (mirroring
// its wrangler.jsonc: workers/deployments-store/wrangler.jsonc), which
// putScript's generic edge.Worker-driven binding set has no concept of — the
// worker binds its own DeploymentsStore class under this name and declares the
// class's one migration exactly once, on the bootstrap that first creates it.
const (
	doBindingName  = "DEPLOYMENTS_DO"
	doClassName    = "DeploymentsStore"
	doMigrationTag = "v1"
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
	secret := prior[edge.RootStackKeySecret]
	ownerToken := prior[edge.RootStackKeyOwnerToken]
	// A project's first reconcile has no secret; a renamed project's prior
	// names a different slug. Either way we mint fresh ownership and seed a new
	// instance — the slug is the project's durable identity, so renaming it
	// forks a new project (fresh history), leaving the old instance orphaned.
	fresh := secret == "" || prior[edge.RootStackKeySlug] != slug

	if fresh {
		mintedSecret, err := mintSecret()
		if err != nil {
			return nil, fmt.Errorf("mint project store secret: %w", err)
		}
		mintedOwner, err := mintSecret()
		if err != nil {
			return nil, fmt.Errorf("mint project store owner token: %w", err)
		}
		secret, ownerToken = mintedSecret, mintedOwner
		if err := p.initializeInstance(ctx, endpoint, slug, spec.BootstrapCred, ownerToken, secret); err != nil {
			return nil, fmt.Errorf("initialize project store instance: %w", err)
		}
	} else {
		current, err := p.getVersionStamp(ctx, endpoint, slug, secret)
		if err != nil {
			return nil, fmt.Errorf("read root-stack version stamp: %w", err)
		}
		if current == spec.Version {
			return prior, nil
		}
	}

	generic := bindObjectStore(
		withVar(withService(spec.Generic, genericStoreBinding, spec.StoreScriptName), genericSlugBinding, slug),
		spec.Values,
	)
	genericUp := upload{accountID: accountID, scriptName: spec.GenericName, worker: generic}
	assetsJWT, err := p.uploadAssets(ctx, genericUp)
	if err != nil {
		return nil, fmt.Errorf("upload generic worker assets: %w", err)
	}
	if err := p.putScript(ctx, genericUp, assetsJWT); err != nil {
		return nil, fmt.Errorf("put generic worker: %w", err)
	}
	if err := p.reconcileCustomDomains(ctx, genericUp, spec.Domain); err != nil {
		return nil, err
	}
	if _, err := p.setSubdomain(ctx, genericUp, spec.Domain == ""); err != nil {
		return nil, fmt.Errorf("set generic worker subdomain: %w", err)
	}

	if err := p.putVersionStamp(ctx, endpoint, slug, secret, spec.Version); err != nil {
		return nil, fmt.Errorf("set root-stack version stamp: %w", err)
	}

	return edge.RootStackState{
		edge.RootStackKeySlug:       slug,
		edge.RootStackKeyEndpoint:   endpoint,
		edge.RootStackKeySecret:     secret,
		edge.RootStackKeyOwnerToken: ownerToken,
	}, nil
}

// DestroyRootStack deletes every worker in workers — a project's generic
// worker(s) — detaching each one's custom-domain binding(s) first, leaving the
// user's DNS untouched. The shared deployments-store worker is never among
// them (it outlives any single project; a project's store data is reclaimed by
// DestroyInstance). It is best-effort: a failure on one worker does not stop
// the others, and every failure is joined into the returned error so the host
// can report exactly what remains.
func (p *provider) DestroyRootStack(ctx context.Context, workers []string) error {
	accountID := os.Getenv(envAccountID)
	if accountID == "" {
		return fmt.Errorf("%s is not set; it is required to destroy the Cloudflare root stack", envAccountID)
	}

	var errs []error
	for _, name := range workers {
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

// DestroyInstance wipes the project's own instance in the shared
// deployments-store worker, authenticated with the project secret in state.
// A project that never deployed to production (no secret in state) is a no-op,
// which also makes `ocel destroy` safe to re-run: the per-project state is
// deleted after this succeeds, so a re-run reads empty state and skips.
func (p *provider) DestroyInstance(ctx context.Context, state edge.RootStackState) error {
	if state[edge.RootStackKeySecret] == "" {
		return nil
	}
	_, err := p.storeRequest(ctx, state, http.MethodPost, "/destroy", nil, nil)
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

// putStoreScript uploads the shared deployments-store worker (bootstrapStore in
// cloudflare.go): like putScript, but it additionally binds the worker's own
// DeploymentsStore Durable Object class — a binding no plain edge.Worker
// carries, since it names a class the script itself exports rather than an
// external resource — and, only when migrate is true (the bootstrap that first
// creates the class), declares its one SQLite-backed migration. Redeclaring
// that migration on a later bootstrap would be at best redundant and at worst
// rejected, so every bootstrap after the first omits it.
func (p *provider) putStoreScript(ctx context.Context, up upload, migrate bool) error {
	body, contentType, err := buildStoreScriptMultipart(up.worker, migrate)
	if err != nil {
		return err
	}
	_, err = p.client.Workers.Scripts.Update(ctx, up.scriptName, workers.ScriptUpdateParams{
		AccountID: cf.F(up.accountID),
	}, option.WithRequestBody(contentType, body))
	return err
}

// buildStoreScriptMultipart is buildScriptMultipart's counterpart for the
// shared deployments-store worker: the same module/binding shape, plus the
// DeploymentsStore Durable Object binding and, when migrate is true, its
// migration declaration.
func buildStoreScriptMultipart(worker edge.Worker, migrate bool) ([]byte, string, error) {
	bindings := append(scriptBindings(worker, false), map[string]any{
		"type":       "durable_object_namespace",
		"name":       doBindingName,
		"class_name": doClassName,
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
			"tag":                doMigrationTag,
			"new_sqlite_classes": []string{doClassName},
		}
	}
	metadataJSON, err := json.Marshal(metadata)
	if err != nil {
		return nil, "", fmt.Errorf("marshal deployments-store worker metadata: %w", err)
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

func (p *provider) Promote(ctx context.Context, state edge.RootStackState, promotion edge.Promotion) error {
	_, err := p.storeRequest(ctx, state, http.MethodPost, "/promote", promotion, nil)
	return err
}

func (p *provider) History(ctx context.Context, state edge.RootStackState) ([]edge.HistoryEntry, error) {
	var history []edge.HistoryEntry
	if _, err := p.storeRequest(ctx, state, http.MethodGet, "/history", nil, &history); err != nil {
		return nil, err
	}
	return history, nil
}

func (p *provider) DeletePromotionArtifacts(ctx context.Context, state edge.RootStackState, keepN int) (edge.PruneResult, error) {
	var result edge.PruneResult
	if _, err := p.storeRequest(ctx, state, http.MethodPost, "/prune", map[string]int{"keepN": keepN}, &result); err != nil {
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

func (p *provider) getVersionStamp(ctx context.Context, endpoint, slug, secret string) (string, error) {
	var out struct {
		Version *string `json:"version"`
	}
	if _, err := p.storeRequestTo(ctx, endpoint, slug, secret, http.MethodGet, "/version-stamp", nil, &out); err != nil {
		return "", err
	}
	if out.Version == nil {
		return "", nil
	}
	return *out.Version, nil
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
