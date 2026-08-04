// Package cloudflare is the Cloudflare Workers edge: it uploads an assembled
// worker as a Workers script with its static assets, and routes it on a custom
// domain or the account's workers.dev subdomain.
package cloudflare

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"mime"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"os"
	"path"
	"strings"

	cf "github.com/cloudflare/cloudflare-go/v4"
	"github.com/cloudflare/cloudflare-go/v4/accounts"
	"github.com/cloudflare/cloudflare-go/v4/dns"
	"github.com/cloudflare/cloudflare-go/v4/option"
	"github.com/cloudflare/cloudflare-go/v4/workers"
	"github.com/cloudflare/cloudflare-go/v4/zones"

	"github.com/ocelhq/ocel/cloud/edge"
)

// envAccountID names the Cloudflare account workers are deployed into.
// envAPIToken is the token the SDK client authenticates with; it is read by the
// client itself and named here only for diagnostics.
const (
	envAccountID = "CLOUDFLARE_ACCOUNT_ID"
	envAPIToken  = "CLOUDFLARE_API_TOKEN"
)

// compatDate pins the Workers runtime compatibility date uploaded scripts are
// built against (mirrors workers/nextjs/wrangler.jsonc). compatFlags enables the
// Node.js compatibility the bundled routing code relies on.
const compatDate = "2026-07-13"

var compatFlags = []string{"nodejs_compat"}

// A worker route is only a match rule: unlike a custom domain it creates no DNS
// record, and a hostname with no proxied record never reaches Cloudflare's edge
// for the route to fire (it returns ERR_NAME_NOT_RESOLVED). So the route path
// also plants a proxied placeholder for the pattern's hostname — an AAAA to the
// IPv6 discard prefix, Cloudflare's canonical "the Worker is the origin" record.
// The content doubles as Ocel's ownership marker, so teardown removes only the
// records it planted and never one the user manages at the same name.
const (
	routeRecordContent = "100::"
	routeRecordComment = "managed by ocel — worker route placeholder"
)

// observability is the Workers observability settings every deployed worker
// ships with: logs (with per-invocation summaries) and OTel traces, both at 100%
// head sampling. It is uploaded as a field of the script metadata, the same way
// wrangler applies it, so no separate settings call is needed.
var observability = map[string]any{
	"enabled":            true,
	"head_sampling_rate": 1,
	"logs":               map[string]any{"enabled": true, "invocation_logs": true},
	"traces":             map[string]any{"enabled": true},
}

// provider is the cloudflare-go-backed edge.Provider. It performs the real
// multi-step worker upload (assets session -> asset batches -> script PUT ->
// custom-domain or workers.dev routing) and is exercised only end-to-end; the
// provider-side deploy orchestration is unit-tested against a fake through the
// edge.Provider seam.
type provider struct {
	client *cf.Client
}

// New builds the Cloudflare edge. Its API token is read from
// CLOUDFLARE_API_TOKEN by the cloudflare-go client.
func New() edge.Provider {
	return &provider{client: cf.NewClient()}
}

func (p *provider) Kind() edge.Kind { return edge.KindCloudflare }

// CodeRuntime reports the runtime a dynamically loaded worker is evaluated
// under: the same settings every uploaded script is built against, so a
// deployment's edge code never runs on a runtime the worker loading it does
// not. It implements edge.CodeLoader.
func (p *provider) CodeRuntime() (string, []string) { return compatDate, compatFlags }

// Bootstrap provisions the substrate class's R2 cache store and reports
// Cloudflare's trust posture. Cloudflare runs in its own account, outside any
// cloud provider's trust boundary, so the provider must mint static credentials
// for it — and, now that the cache lives here, Cloudflare mints one back.
func (p *provider) Bootstrap(ctx context.Context, class edge.Class) (edge.BootstrapOutput, error) {
	accountID := os.Getenv(envAccountID)
	if accountID == "" {
		return edge.BootstrapOutput{}, fmt.Errorf("%s is not set; it is required to bootstrap the Cloudflare edge", envAccountID)
	}
	out, err := newCacheStore(p.client).bootstrap(ctx, accountID, class)
	if err != nil {
		return out, err
	}
	// Each substrate provisions its own deployments-store worker so preview and
	// production never share promotion history or Durable Object state. The two
	// workers are distinct scripts (production vs preview script names), which is
	// what keeps their DO namespaces separate — the namespace is script-scoped.
	scriptName, err := storeScriptNameFor(class)
	if err != nil {
		return out, err
	}
	storeOffer, err := p.bootstrapStore(ctx, accountID, scriptName)
	if err != nil {
		return out, fmt.Errorf("bootstrap deployments-store worker: %w", err)
	}
	out.Offers = append(out.Offers, storeOffer)

	writerOffer, err := p.bootstrapISRWriter(ctx, accountID, class)
	if err != nil {
		return out, fmt.Errorf("bootstrap isr-writer worker: %w", err)
	}
	out.Offers = append(out.Offers, writerOffer)
	return out, nil
}

// bootstrapISRWriter provisions the single shared ISR writer worker for the
// substrate class and offers the address and credential a deploy needs to seed
// and reach one build's instance. It is the same shape as bootstrapStore — one
// account-level script owning a Durable Object namespace, re-uploaded and
// re-credentialed on every bootstrap — with one addition: the class's cache
// bucket is bound natively, which is the whole point. With it a deployed Lambda
// writes its ISR entries through this worker and needs no standing R2
// credentials of its own.
func (p *provider) bootstrapISRWriter(ctx context.Context, accountID string, class edge.Class) (edge.Offer, error) {
	scriptName, err := isrWriterScriptNameFor(class)
	if err != nil {
		return edge.Offer{}, err
	}
	bundles, err := edge.LoadISRWriterBundleManifest()
	if err != nil {
		return edge.Offer{}, err
	}
	path, err := bundles.Path(edge.KindCloudflare)
	if err != nil {
		return edge.Offer{}, err
	}
	worker, err := readWorkerBundle(path)
	if err != nil {
		return edge.Offer{}, err
	}

	exists, err := p.FindApp(ctx, scriptName)
	if err != nil {
		return edge.Offer{}, fmt.Errorf("check isr-writer worker: %w", err)
	}
	cred, err := mintSecret()
	if err != nil {
		return edge.Offer{}, fmt.Errorf("mint bootstrap credential: %w", err)
	}

	worker.ObjectStore = edge.ObjectStore{Binding: cacheStoreBinding, Bucket: cacheStoreName(class)}
	up := upload{accountID: accountID, scriptName: scriptName, worker: withSecret(worker, bootstrapSecretBinding, cred)}
	if err := p.putDurableObjectScript(ctx, up, isrWriterDO, !exists); err != nil {
		return edge.Offer{}, fmt.Errorf("put isr-writer worker: %w", err)
	}
	endpoint, err := p.setSubdomain(ctx, up, true)
	if err != nil {
		return edge.Offer{}, fmt.Errorf("set isr-writer worker subdomain: %w", err)
	}

	return edge.Offer{
		Kind: edge.OfferISRWriter,
		Values: map[string]string{
			edge.OfferKeyISRWriterEndpoint:      endpoint,
			edge.OfferKeyISRWriterScriptName:    scriptName,
			edge.OfferKeyISRWriterBootstrapCred: cred,
		},
	}, nil
}

// bootstrapStore provisions the single shared deployments-store worker for the
// account and offers the address, credential and script name a project's root
// stack needs to seed and reach its own instance. It re-uploads the bundle on
// every bootstrap (so store-worker updates ship) and re-mints the bootstrap
// credential, which is harmless: the credential authorizes only
// /<slug>/initialize and is read fresh from the adopted param at deploy time,
// never held long-term. The DO migration is declared only on the first
// bootstrap (when no script exists yet); redeclaring it later is rejected.
func (p *provider) bootstrapStore(ctx context.Context, accountID, scriptName string) (edge.Offer, error) {
	bundles, err := edge.LoadStoreBundleManifest()
	if err != nil {
		return edge.Offer{}, err
	}
	path, err := bundles.Path(edge.KindCloudflare)
	if err != nil {
		return edge.Offer{}, err
	}
	worker, err := readWorkerBundle(path)
	if err != nil {
		return edge.Offer{}, err
	}

	exists, err := p.FindApp(ctx, scriptName)
	if err != nil {
		return edge.Offer{}, fmt.Errorf("check deployments-store worker: %w", err)
	}
	cred, err := mintSecret()
	if err != nil {
		return edge.Offer{}, fmt.Errorf("mint bootstrap credential: %w", err)
	}

	up := upload{accountID: accountID, scriptName: scriptName, worker: withSecret(worker, bootstrapSecretBinding, cred)}
	if err := p.putDurableObjectScript(ctx, up, deploymentsStoreDO, !exists); err != nil {
		return edge.Offer{}, fmt.Errorf("put deployments-store worker: %w", err)
	}
	endpoint, err := p.setSubdomain(ctx, up, true)
	if err != nil {
		return edge.Offer{}, fmt.Errorf("set deployments-store worker subdomain: %w", err)
	}

	return edge.Offer{
		Kind: edge.OfferDeploymentsStore,
		Values: map[string]string{
			edge.OfferKeyStoreEndpoint:      endpoint,
			edge.OfferKeyStoreScriptName:    scriptName,
			edge.OfferKeyStoreBootstrapCred: cred,
		},
	}, nil
}

// readWorkerBundle reads a compiled worker entrypoint off disk into the
// edge.Worker shape the upload machinery consumes: a single main module, no
// per-deploy modules/vars/assets of its own.
func readWorkerBundle(path string) (edge.Worker, error) {
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

// FindApp reports whether a Workers script exists under name. A 404 is the
// answer "no", not a failure.
func (p *provider) FindApp(ctx context.Context, name string) (bool, error) {
	accountID := os.Getenv(envAccountID)
	if accountID == "" {
		return false, fmt.Errorf("%s is not set; it is required to query the Cloudflare edge", envAccountID)
	}
	_, err := p.client.Workers.Scripts.Settings.Get(ctx, name, workers.ScriptSettingGetParams{
		AccountID: cf.F(accountID),
	})
	var apiErr *cf.Error
	if errors.As(err, &apiErr) && apiErr.StatusCode == http.StatusNotFound {
		return false, nil
	}
	return err == nil, err
}

// VerifyCredentials proves the configured API token authenticates and can
// reach CLOUDFLARE_ACCOUNT_ID, and reports that account for the preflight
// banner. It fetches the account: an unset/invalid/expired token or a token
// without access to the account all surface here, before any deploy touches
// the edge. It implements edge.CredentialVerifier.
func (p *provider) VerifyCredentials(ctx context.Context) (edge.CredentialIdentity, error) {
	accountID := os.Getenv(envAccountID)
	if accountID == "" {
		return edge.CredentialIdentity{}, fmt.Errorf("%s is not set", envAccountID)
	}
	if os.Getenv(envAPIToken) == "" {
		return edge.CredentialIdentity{}, fmt.Errorf("%s is not set", envAPIToken)
	}
	if _, err := p.client.Accounts.Get(ctx, accounts.AccountGetParams{AccountID: cf.F(accountID)}); err != nil {
		return edge.CredentialIdentity{}, fmt.Errorf("Cloudflare rejected %s for account %s: %w", envAPIToken, accountID, err)
	}
	return edge.CredentialIdentity{Account: accountID}, nil
}

func (p *provider) DeployApp(ctx context.Context, app edge.AppDeployment) (edge.AppResult, error) {
	accountID := os.Getenv(envAccountID)
	if accountID == "" {
		return edge.AppResult{}, fmt.Errorf("%s is not set; it is required to deploy to the Cloudflare edge", envAccountID)
	}
	up := upload{accountID: accountID, scriptName: app.Name, worker: bindCodeLoader(bindObjectStore(app.Worker, app.Values))}

	assetsJWT, err := p.uploadAssets(ctx, up)
	if err != nil {
		return edge.AppResult{}, fmt.Errorf("upload assets: %w", err)
	}

	if err := p.putScript(ctx, up, assetsJWT); err != nil {
		return edge.AppResult{}, fmt.Errorf("put worker script: %w", err)
	}

	if err := p.reconcileWorkerRoutes(ctx, up, routePlan{desired: app.Domains, prune: true}, app.Warn); err != nil {
		return edge.AppResult{}, err
	}
	url, err := p.setSubdomain(ctx, up, len(app.Domains) == 0)
	if err != nil {
		return edge.AppResult{}, fmt.Errorf("set workers.dev subdomain: %w", err)
	}
	if len(app.Domains) > 0 {
		url = canonicalDomainURL(app.Domains)
	}
	return edge.AppResult{URL: url}, nil
}

// upload is one app deployment resolved against the Cloudflare account it lands
// in.
type upload struct {
	accountID  string
	scriptName string
	worker     edge.Worker
}

// uploadAssets registers the static-asset manifest, uploads the file batches the
// session asks for, and returns the completion JWT the script upload binds. When
// the worker has no static assets it returns an empty token and uploads nothing.
func (p *provider) uploadAssets(ctx context.Context, up upload) (string, error) {
	if len(up.worker.Assets) == 0 {
		return "", nil
	}

	manifest := make(map[string]workers.ScriptAssetUploadNewParamsManifest, len(up.worker.Assets))
	assetByHash := make(map[string]edge.StaticAsset, len(up.worker.Assets))
	for _, a := range up.worker.Assets {
		hash := hashAsset(a)
		manifest[a.Path] = workers.ScriptAssetUploadNewParamsManifest{
			Hash: cf.F(hash),
			Size: cf.F(int64(len(a.Content))),
		}
		assetByHash[hash] = a
	}

	session, err := p.client.Workers.Scripts.Assets.Upload.New(ctx, up.scriptName, workers.ScriptAssetUploadNewParams{
		AccountID: cf.F(up.accountID),
		Manifest:  cf.F(manifest),
	})
	if err != nil {
		return "", fmt.Errorf("create assets upload session: %w", err)
	}

	// When every file in the manifest is already present (e.g. a redeploy), the
	// session returns no buckets and its own JWT is the completion token. When
	// there are buckets, only a completed batch upload yields the completion
	// token — the session JWT merely authenticates the uploads.
	filesToUpload := 0
	for _, bucket := range session.Buckets {
		filesToUpload += len(bucket)
	}
	if filesToUpload == 0 {
		if session.JWT == "" {
			return "", fmt.Errorf("assets session returned no completion token")
		}
		return session.JWT, nil
	}

	completionJWT := ""
	for _, bucket := range session.Buckets {
		if len(bucket) == 0 {
			continue
		}
		body, contentType, err := buildAssetBatch(bucket, assetByHash)
		if err != nil {
			return "", err
		}
		res, err := p.client.Workers.Assets.Upload.New(ctx, workers.AssetUploadNewParams{
			AccountID: cf.F(up.accountID),
			Base64:    cf.F(workers.AssetUploadNewParamsBase64True),
		}, option.WithRequestBody(contentType, body), option.WithHeader("Authorization", "Bearer "+session.JWT))
		if err != nil {
			return "", fmt.Errorf("upload asset batch: %w", err)
		}
		if res.JWT != "" {
			completionJWT = res.JWT
		}
	}
	if completionJWT == "" {
		return "", fmt.Errorf("asset upload returned no completion token")
	}
	return completionJWT, nil
}

// hashAsset computes the content hash the assets upload session keys a file by:
// the SHA-256 of the base64-encoded contents concatenated with the file
// extension (no leading dot), hex-encoded and truncated to 32 characters. This
// mirrors wrangler's algorithm; a mismatch would make the session reject the
// upload.
func hashAsset(a edge.StaticAsset) string {
	ext := strings.TrimPrefix(path.Ext(a.Path), ".")
	sum := sha256.Sum256([]byte(base64.StdEncoding.EncodeToString(a.Content) + ext))
	return hex.EncodeToString(sum[:])[:32]
}

// buildAssetBatch encodes one bucket of files as the multipart/form-data body
// the assets upload endpoint expects: one part per file, named and filenamed by
// its content hash, carrying the base64-encoded contents (the ?base64=true query
// tells Cloudflare the parts are base64). An unknown extension maps to
// "application/null" — Cloudflare's sentinel for "serve without a Content-Type",
// mirroring wrangler.
func buildAssetBatch(bucket []string, assetByHash map[string]edge.StaticAsset) ([]byte, string, error) {
	buf := &bytes.Buffer{}
	w := multipart.NewWriter(buf)
	for _, hash := range bucket {
		asset := assetByHash[hash]
		contentType := mime.TypeByExtension(path.Ext(asset.Path))
		if contentType == "" {
			contentType = "application/null"
		}
		encoded := base64.StdEncoding.EncodeToString(asset.Content)
		if err := writePart(w, hash, hash, contentType, []byte(encoded)); err != nil {
			return nil, "", err
		}
	}
	if err := w.Close(); err != nil {
		return nil, "", err
	}
	return buf.Bytes(), w.FormDataContentType(), nil
}

// putScript uploads the worker as a multipart module-syntax script: a metadata
// part describing the bindings, assets, and compatibility, plus one part per
// module (the entrypoint and any siblings). The generated Update method only
// serializes metadata JSON, so the multipart body is built by hand and swapped
// in via WithRequestBody.
func (p *provider) putScript(ctx context.Context, up upload, assetsJWT string) error {
	body, contentType, err := buildScriptMultipart(up.worker, assetsJWT)
	if err != nil {
		return err
	}

	_, err = p.client.Workers.Scripts.Update(ctx, up.scriptName, workers.ScriptUpdateParams{
		AccountID: cf.F(up.accountID),
	}, option.WithRequestBody(contentType, body))
	return err
}

// buildScriptMultipart assembles the worker upload's multipart/form-data body
// and its content type.
func buildScriptMultipart(worker edge.Worker, assetsJWT string) ([]byte, string, error) {
	// Cloudflare rejects an assets binding without a completed assets upload, so
	// the binding and the assets metadata are gated on the same token: present
	// together or absent together.
	includeAssets := assetsJWT != ""
	metadata := map[string]any{
		"main_module":         worker.Main.Name,
		"compatibility_date":  compatDate,
		"compatibility_flags": compatFlags,
		"observability":       observability,
		"bindings":            scriptBindings(worker, includeAssets),
	}
	if includeAssets {
		metadata["assets"] = map[string]any{
			"jwt": assetsJWT,
			// The worker is the authoritative router: it always runs and delegates
			// to the Assets binding itself, rather than Cloudflare serving assets
			// ahead of the worker.
			"config": map[string]any{"run_worker_first": true},
		}
	}
	metadataJSON, err := json.Marshal(metadata)
	if err != nil {
		return nil, "", fmt.Errorf("marshal worker metadata: %w", err)
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

// cacheStoreBinding is the name the Next.js worker reads its ISR cache store
// under. Both worker paths bind it here: the preview worker assembled by the
// framework and the frozen generic worker loaded from its compiled bundle, which
// carries no ObjectStore of its own.
const cacheStoreBinding = "OCEL_CACHE_STORE"

// bindObjectStore points the worker's object-store binding at the bucket this
// edge provisioned for the substrate class, as bootstrap reported it and the
// provider handed it back. It supplies the binding name too: the frozen generic
// worker arrives with an empty ObjectStore, and scriptBindings emits the R2
// binding only when both name and bucket are set. A substrate bootstrapped
// before there was a cache bucket carries no such value, so the worker still
// uploads without the binding.
func bindObjectStore(worker edge.Worker, values map[string]string) edge.Worker {
	worker.ObjectStore.Binding = cacheStoreBinding
	worker.ObjectStore.Bucket = values[valueKeyCacheBucket]
	return worker
}

// codeLoaderBinding is the name the Next.js worker reads its Worker Loader
// under, the binding it evaluates a Deployment's own edge routes and middleware
// through.
const codeLoaderBinding = "LOADER"

// bindCodeLoader gives an app worker Cloudflare's Worker Loader under the name
// its code reads it from. Like the object store it is bound here rather than
// declared by the worker: the frozen generic worker arrives from its compiled
// bundle declaring nothing, and only an app worker loads deployment-owned code
// — the deployments-store worker never does.
func bindCodeLoader(worker edge.Worker) edge.Worker {
	worker.LoaderBinding = codeLoaderBinding
	return worker
}

// scriptBindings is the worker's binding set: the Assets Fetcher (only when
// assets were uploaded), the object store as an R2 bucket, the code loader, one
// service binding per entry in Services, one plain-text binding per var, and
// one secret_text binding per secret — values that must never surface in
// plaintext metadata.
func scriptBindings(worker edge.Worker, includeAssets bool) []map[string]any {
	bindings := []map[string]any{}
	if includeAssets && worker.AssetBinding != "" {
		bindings = append(bindings, map[string]any{
			"type": "assets",
			"name": worker.AssetBinding,
		})
	}
	if store := worker.ObjectStore; store.Binding != "" && store.Bucket != "" {
		bindings = append(bindings, map[string]any{
			"type":        "r2_bucket",
			"name":        store.Binding,
			"bucket_name": store.Bucket,
		})
	}
	if worker.LoaderBinding != "" {
		// The API's binding type is the singular "worker_loader"; the plural
		// "worker_loaders" is wrangler's config key and is rejected here.
		bindings = append(bindings, map[string]any{
			"type": "worker_loader",
			"name": worker.LoaderBinding,
		})
	}
	for name, service := range worker.Services {
		bindings = append(bindings, map[string]any{
			"type":    "service",
			"name":    name,
			"service": service,
		})
	}
	for name, text := range worker.Vars {
		bindings = append(bindings, map[string]any{
			"type": "plain_text",
			"name": name,
			"text": text,
		})
	}
	for name, text := range worker.Secrets {
		bindings = append(bindings, map[string]any{
			"type": "secret_text",
			"name": name,
			"text": text,
		})
	}
	return bindings
}

// writePart writes one multipart form part. A non-empty filename marks the part
// as a module file rather than a plain field.
func writePart(w *multipart.Writer, name, filename, contentType string, content []byte) error {
	header := textproto.MIMEHeader{}
	if filename != "" {
		header.Set("Content-Disposition", fmt.Sprintf("form-data; name=%q; filename=%q", name, filename))
	} else {
		header.Set("Content-Disposition", fmt.Sprintf("form-data; name=%q", name))
	}
	header.Set("Content-Type", contentType)
	part, err := w.CreatePart(header)
	if err != nil {
		return err
	}
	_, err = part.Write(content)
	return err
}

// setSubdomain returns the worker's public workers.dev URL when enabling, or ""
// when disabling.
func (p *provider) setSubdomain(ctx context.Context, up upload, enabled bool) (string, error) {
	if _, err := p.client.Workers.Scripts.Subdomain.New(ctx, up.scriptName, workers.ScriptSubdomainNewParams{
		AccountID:       cf.F(up.accountID),
		Enabled:         cf.F(enabled),
		PreviewsEnabled: cf.F(false),
	}); err != nil {
		return "", err
	}
	if !enabled {
		return "", nil
	}

	account, err := p.client.Workers.Subdomains.Get(ctx, workers.SubdomainGetParams{
		AccountID: cf.F(up.accountID),
	})
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("https://%s.%s.workers.dev", up.scriptName, account.Subdomain), nil
}

// routePlan is how one deploy wants its worker's hostnames attached. Mirrors the
// intent fields of edge.RootStackSpec, which document why each choice exists.
type routePlan struct {
	desired        []string
	prune          bool
	requiredRecord string
}

// reconcileWorkerRoutes attaches the worker to every hostname in plan.desired,
// each as a Cloudflare worker route "<hostname>/*", or to none when desired is
// empty. Every hostname — a plain production apex, a "www" host, an exact
// preview pointer host, or a "*." wildcard (multitenant production or a preview
// base) — routes the same way, so production and preview can share a base domain
// and Cloudflare's most-specific route wins per request. It is the single
// hostname path for both classes, replacing the earlier
// custom-domain-for-production split.
//
// It converges idempotently. A hostname reaches Cloudflare's edge only through a
// proxied DNS record, so it either plants its own placeholder record per hostname
// or verifies plan.requiredRecord instead. A pruning plan then drops the routes
// this deploy did not ask for, along with only Ocel's own placeholder records. It
// always detaches any custom domain still bound to this script from before the
// switch to routes — a custom domain would otherwise reject an overlapping
// route. warn, when non-nil, receives non-fatal advisories (a hostname the
// zone's Universal SSL does not cover, a user-managed record blocking a route).
func (p *provider) reconcileWorkerRoutes(ctx context.Context, up upload, plan routePlan, warn func(string)) error {
	warn = nilSafeWarn(warn)

	if err := p.detachCustomDomains(ctx, up.accountID, up.scriptName); err != nil {
		return err
	}

	if plan.requiredRecord != "" && len(plan.desired) > 0 {
		if err := p.verifyProxiedRecord(ctx, up.accountID, plan.requiredRecord); err != nil {
			return err
		}
	}

	wanted := make(map[string]bool, len(plan.desired))
	for _, host := range plan.desired {
		zoneID, zoneName, err := p.resolveZone(ctx, up.accountID, routeBaseDomain(host))
		if err != nil {
			return err
		}
		wanted[host] = true
		if !coveredByUniversalSSL(host, zoneName) {
			warn(fmt.Sprintf("%s is more than one label below %s, which the zone's Universal SSL certificate does not cover — TLS will fail there until you add a Cloudflare Advanced Certificate for it", host, zoneName))
		}
		if err := p.ensureRoute(ctx, zoneID, host+"/*", up.scriptName); err != nil {
			return err
		}
		if plan.requiredRecord != "" {
			continue
		}
		if err := p.ensureProxiedRecord(ctx, zoneID, host, warn); err != nil {
			return err
		}
	}

	if !plan.prune {
		return nil
	}
	return p.pruneStaleRoutes(ctx, up, wanted)
}

// pruneStaleRoutes deletes every route pointing at this script whose hostname is
// not in wanted, across all of the account's zones — so dropping a hostname from
// the config (even the last one in a whole zone) tears its route and Ocel's
// placeholder record down. A record the user manages is left untouched.
func (p *provider) pruneStaleRoutes(ctx context.Context, up upload, wanted map[string]bool) error {
	owned := p.client.Zones.ListAutoPaging(ctx, zones.ZoneListParams{
		Account: cf.F(zones.ZoneListParamsAccount{ID: cf.F(up.accountID)}),
	})
	var errs []error
	for owned.Next() {
		zoneID := owned.Current().ID
		routes := p.client.Workers.Routes.ListAutoPaging(ctx, workers.RouteListParams{ZoneID: cf.F(zoneID)})
		for routes.Next() {
			route := routes.Current()
			if route.Script != up.scriptName {
				continue
			}
			host := strings.TrimSuffix(route.Pattern, "/*")
			if wanted[host] {
				continue
			}
			if _, err := p.client.Workers.Routes.Delete(ctx, route.ID, workers.RouteDeleteParams{ZoneID: cf.F(zoneID)}); err != nil {
				errs = append(errs, fmt.Errorf("delete stale worker route %q: %w", route.Pattern, err))
				continue
			}
			if err := p.deleteProxiedRecord(ctx, zoneID, host); err != nil {
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

// routeBaseDomain is the zone-owning suffix of a route hostname: a "*." wildcard
// strips its wildcard label, and any other host is resolved by itself. The
// result is what resolveZone matches an account zone against.
func routeBaseDomain(host string) string {
	return strings.TrimPrefix(host, "*.")
}

// coveredByUniversalSSL reports whether host falls under the zone's Universal
// SSL certificate, which covers only the apex and hostnames exactly one label
// below it — including the first-level wildcard "*.<zone>". A name two or more
// labels deep ("*.preview.<zone>", "a.b.<zone>") needs an Advanced Certificate.
func coveredByUniversalSSL(host, zone string) bool {
	if host == zone {
		return true
	}
	sub := strings.TrimSuffix(host, "."+zone)
	if sub == host {
		return false
	}
	return !strings.Contains(sub, ".")
}

// canonicalDomainURL is the single URL a multi-hostname deploy is reported
// under: the first non-wildcard hostname in declared order, or — when every
// hostname is a wildcard (a pure multitenant deploy) — the first declared,
// verbatim. Empty for no hostnames. Mirrors workerAppURL's production branch in
// cloud/aws deploy (a separate Go module): keep the two in step.
func canonicalDomainURL(domains []string) string {
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

func nilSafeWarn(warn func(string)) func(string) {
	if warn == nil {
		return func(string) {}
	}
	return warn
}

// ensureRoute makes the zone route pattern to scriptName: it reuses an existing
// route for that pattern (repointing it at this script if a different one holds
// it) and otherwise creates it, leaving routes for other patterns alone.
func (p *provider) ensureRoute(ctx context.Context, zoneID, pattern, scriptName string) error {
	existing := p.client.Workers.Routes.ListAutoPaging(ctx, workers.RouteListParams{ZoneID: cf.F(zoneID)})
	for existing.Next() {
		route := existing.Current()
		if route.Pattern != pattern {
			continue
		}
		if route.Script == scriptName {
			return nil
		}
		if _, err := p.client.Workers.Routes.Update(ctx, route.ID, workers.RouteUpdateParams{
			ZoneID:  cf.F(zoneID),
			Pattern: cf.F(pattern),
			Script:  cf.F(scriptName),
		}); err != nil {
			return fmt.Errorf("repoint worker route %q: %w", pattern, err)
		}
		return nil
	}
	if err := existing.Err(); err != nil {
		return fmt.Errorf("list worker routes: %w", err)
	}

	if _, err := p.client.Workers.Routes.New(ctx, workers.RouteNewParams{
		ZoneID:  cf.F(zoneID),
		Pattern: cf.F(pattern),
		Script:  cf.F(scriptName),
	}); err != nil {
		return fmt.Errorf("attach worker route %q: %w", pattern, err)
	}
	return nil
}

// verifyProxiedRecord fails unless a proxied address record already exists at
// name, in the account zone that owns it. It is ensureProxiedRecord's
// counterpart for a record whose lifecycle is not one deploy's to own — a
// wildcard shared by every hostname under a preview base domain — so it only
// reports, never plants. A record that exists unproxied is as fatal as a missing
// one: an unproxied hostname never reaches Cloudflare's edge, so its worker route
// can never fire.
func (p *provider) verifyProxiedRecord(ctx context.Context, accountID, name string) error {
	zoneID, _, err := p.resolveZone(ctx, accountID, routeBaseDomain(name))
	if err != nil {
		return err
	}
	haveAddress, haveProxied, err := p.addressRecordsAt(ctx, zoneID, name)
	if err != nil {
		return err
	}
	switch {
	case !haveAddress:
		return fmt.Errorf("no DNS record for %q, which the hostnames this deploy serves resolve through — add a proxied (orange cloud) record at %q in Cloudflare and re-run", name, name)
	case !haveProxied:
		return fmt.Errorf("the DNS record for %q is not proxied through Cloudflare, so no worker route under it can ever fire — set %q to proxied (orange cloud) and re-run", name, name)
	}
	return nil
}

// addressRecordsAt reports whether the zone holds any address record (A, AAAA,
// CNAME) at hostname and whether one of them is proxied — the two facts that
// decide whether a worker route there can fire. TXT/MX and the like share the
// name but are inherently unproxiable, so they are ignored.
func (p *provider) addressRecordsAt(ctx context.Context, zoneID, hostname string) (haveAddress, haveProxied bool, err error) {
	existing := p.client.DNS.Records.ListAutoPaging(ctx, dns.RecordListParams{
		ZoneID: cf.F(zoneID),
		Name:   cf.F(dns.RecordListParamsName{Exact: cf.F(hostname)}),
	})
	for existing.Next() {
		rec := existing.Current()
		if !isAddressRecord(rec.Type) {
			continue
		}
		haveAddress = true
		if rec.Proxied {
			haveProxied = true
		}
	}
	if err := existing.Err(); err != nil {
		return false, false, fmt.Errorf("list DNS records for %q: %w", hostname, err)
	}
	return haveAddress, haveProxied, nil
}

// ensureProxiedRecord plants the proxied placeholder record for hostname so it
// resolves to Cloudflare's edge, where the route fires — a route without a
// proxied address record at its hostname never fires. It never overwrites an
// address record the user (or a prior deploy) already put there: if a proxied one
// exists the route already resolves and it is left alone; if address records
// exist but none is proxied the route cannot fire, so it warns rather than
// silently serve nothing — but still leaves them untouched.
func (p *provider) ensureProxiedRecord(ctx context.Context, zoneID, hostname string, warn func(string)) error {
	haveAddress, haveProxied, err := p.addressRecordsAt(ctx, zoneID, hostname)
	if err != nil {
		return err
	}
	if haveAddress {
		if !haveProxied {
			warn(fmt.Sprintf("%s already has a DNS record that is not proxied through Cloudflare, so the worker route will not serve it — set that record to proxied (orange cloud) for %s to go live", hostname, hostname))
		}
		return nil
	}

	if _, err := p.client.DNS.Records.New(ctx, dns.RecordNewParams{
		ZoneID: cf.F(zoneID),
		Body: dns.AAAARecordParam{
			Name:    cf.F(hostname),
			Type:    cf.F(dns.AAAARecordTypeAAAA),
			Content: cf.F(routeRecordContent),
			Proxied: cf.F(true),
			TTL:     cf.F(dns.TTL(1)),
			Comment: cf.F(routeRecordComment),
		},
	}); err != nil {
		return fmt.Errorf("plant proxied DNS record for %q: %w", hostname, err)
	}
	return nil
}

// isAddressRecord reports whether a DNS record type is one that resolves a
// hostname to an address Cloudflare can proxy — A, AAAA, or CNAME. Only these
// determine whether a worker route's hostname reaches the edge.
func isAddressRecord(t dns.RecordResponseType) bool {
	switch t {
	case dns.RecordResponseTypeA, dns.RecordResponseTypeAAAA, dns.RecordResponseTypeCNAME:
		return true
	default:
		return false
	}
}

// resolveZone finds the account zone whose name is the longest suffix of
// hostname (e.g. "acme.com" for "app.acme.com"), returning its id and name. A
// hostname with no owning zone in the account is a hard error: the deploy
// cannot serve it.
func (p *provider) resolveZone(ctx context.Context, accountID, hostname string) (id, name string, err error) {
	owned := p.client.Zones.ListAutoPaging(ctx, zones.ZoneListParams{
		Account: cf.F(zones.ZoneListParamsAccount{ID: cf.F(accountID)}),
	})
	for owned.Next() {
		z := owned.Current()
		if zoneOwns(hostname, z.Name) && len(z.Name) > len(name) {
			id, name = z.ID, z.Name
		}
	}
	if err := owned.Err(); err != nil {
		return "", "", fmt.Errorf("list zones: %w", err)
	}
	if id == "" {
		return "", "", fmt.Errorf("no Cloudflare zone in this account owns %q — add its zone to the account whose CLOUDFLARE_API_TOKEN you provided", hostname)
	}
	return id, name, nil
}

func zoneOwns(hostname, zone string) bool {
	return hostname == zone || strings.HasSuffix(hostname, "."+zone)
}
