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
	"sync"

	cf "github.com/cloudflare/cloudflare-go/v4"
	"github.com/cloudflare/cloudflare-go/v4/accounts"
	"github.com/cloudflare/cloudflare-go/v4/option"
	"github.com/cloudflare/cloudflare-go/v4/shared"
	"github.com/cloudflare/cloudflare-go/v4/workers"

	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

const Kind edge.Kind = "cloudflare"

const (
	envAccountID = "CLOUDFLARE_ACCOUNT_ID"
	envAPIToken  = "CLOUDFLARE_API_TOKEN"
)

const compatDate = "2026-07-13"

var compatFlags = []string{"nodejs_compat"}

const envObservability = "OCEL_EDGE_OBSERVABILITY"

func observability() map[string]any {
	if strings.EqualFold(os.Getenv(envObservability), "off") {
		return map[string]any{"enabled": false}
	}
	return map[string]any{
		"enabled":            true,
		"head_sampling_rate": 1,
		"logs":               map[string]any{"enabled": true, "invocation_logs": true},
		"traces":             map[string]any{"enabled": true},
	}
}

type provider struct {
	client *cf.Client

	zoneMu    sync.Mutex
	zonesSeen map[string][]zoneRef

	entryMu      sync.Mutex
	entryWorkers map[string][]string
}

var (
	_ edge.Edge         = (*provider)(nil)
	_ edge.Programmable = (*provider)(nil)
)

func New() edge.Edge {
	return &provider{client: cf.NewClient(option.WithMaxRetries(clientMaxRetries))}
}

func NewAt(baseURL string) edge.Edge {
	return &provider{client: cf.NewClient(option.WithMaxRetries(clientMaxRetries), option.WithBaseURL(baseURL))}
}

const clientMaxRetries = 5

func (p *provider) Kind() edge.Kind { return Kind }

func (p *provider) Facts() edge.Facts {
	return edge.Facts{
		RunsCode:            true,
		ServesUnbound:       true,
		SignsOriginForwards: true,
		CredentialScope:     os.Getenv(envAccountID),
	}
}

func (p *provider) Supported() []edge.Need {
	return edge.AllNeeds()
}

func (p *provider) FlipBound() edge.FlipBound {
	return edge.FlipBound{}
}

func (p *provider) ProjectSurfaces(scope edge.ProjectScope) []edge.Surface {
	surfaces := []edge.Surface{{
		Kind:   "edge workers",
		Name:   scope.Slug,
		Action: edge.SurfaceDelete,
		Reason: "every per-app worker this project deployed, and the routes that reach them",
	}}
	if len(scope.Hostnames) > 0 {
		surfaces = append(surfaces, edge.Surface{
			Kind:   "worker routes",
			Name:   strings.Join(scope.Hostnames, ", "),
			Action: edge.SurfaceDelete,
			Reason: "the hostnames this project is served on stop resolving to a worker",
		})
	}
	return append(surfaces, edge.Surface{
		Kind:   "deployments store",
		Name:   scope.Slug,
		Action: edge.SurfaceDelete,
		Reason: "the store instance holding every deployment and pointer this project promoted",
	})
}

func (p *provider) PreviewWildcardSurfaces(wildcard string) (edge.Surface, edge.Surface) {
	removed := edge.Surface{
		Kind:   "preview entry worker",
		Name:   wildcard,
		Action: edge.SurfaceDelete,
		Reason: "the shared entry worker holding this wildcard, and the route that reaches it",
	}
	kept := edge.Surface{
		Kind:   "preview bootstrap",
		Name:   string(edge.ClassPreview),
		Action: edge.SurfaceKeep,
		Reason: "bootstrap-scoped: `ocel bootstrap destroy preview` removes what it stood up",
	}
	return removed, kept
}

func (p *provider) SharedPreviewSurface() edge.Surface {
	return edge.Surface{
		Kind:   "shared preview entry worker",
		Name:   edge.PreviewEntryOwner,
		Action: edge.SurfaceKeep,
		Reason: "bootstrap-scoped: it fronts every project's previews",
	}
}

func (p *provider) CodeRuntime() (string, []string) { return compatDate, compatFlags }

func (p *provider) Adoption(_ context.Context, class edge.Class) (edge.Adoption, error) {
	name, ok := cacheStoreNameByClass[class]
	if !ok {
		return edge.Adoption{}, fmt.Errorf("cloudflare: unknown class %q", class)
	}
	workers, err := bootstrapWorkers(class)
	if err != nil {
		return edge.Adoption{}, err
	}
	adoption := edge.Adoption{
		Values: adoptedValues(name),
		Offers: []edge.OfferKind{edge.OfferCacheStore},
	}
	for _, worker := range workers {
		adoption.Offers = append(adoption.Offers, worker.offer)
	}
	return adoption, nil
}

func (p *provider) Bootstrap(ctx context.Context, class edge.Class) (edge.BootstrapOutput, error) {
	accountID, err := bootstrapCredentials()
	if err != nil {
		return edge.BootstrapOutput{}, err
	}
	state, err := p.readState(ctx, accountID, class)
	if err != nil {
		return edge.BootstrapOutput{}, err
	}
	out, err := newCacheStore(p.client).bootstrap(ctx, accountID, state.store)
	if err != nil {
		return out, err
	}
	for _, settling := range state.workers {
		offer, err := p.settleWorker(ctx, accountID, settling)
		if err != nil {
			return out, fmt.Errorf("bootstrap %s: %w", settling.what, err)
		}
		out.Offers = append(out.Offers, offer)
	}
	return out, nil
}

func (p *provider) Teardown(ctx context.Context, class edge.Class) error {
	accountID := os.Getenv(envAccountID)
	if accountID == "" {
		return fmt.Errorf("%s is not set; it is required to tear the Cloudflare edge down", envAccountID)
	}
	storeScript, err := storeScriptNameFor(class)
	if err != nil {
		return err
	}
	writerScript, err := isrWriterScriptNameFor(class)
	if err != nil {
		return err
	}
	var errs []error
	for _, name := range []string{storeScript, writerScript} {
		if err := p.deleteScript(ctx, accountID, name); err != nil {
			errs = append(errs, fmt.Errorf("delete worker %q: %w", name, err))
		}
	}
	if err := newCacheStore(p.client).teardown(ctx, accountID, class); err != nil {
		errs = append(errs, err)
	}
	return errors.Join(errs...)
}

type bootstrapWorker struct {
	scriptName string
	worker     edge.Worker
	do         durableObjectWorker
	what       string
	offer      edge.OfferKind
	keys       offerKeys
}

type offerKeys struct {
	endpoint   string
	scriptName string
	cred       string
}

func bootstrapWorkers(class edge.Class) ([]bootstrapWorker, error) {
	storeScript, err := storeScriptNameFor(class)
	if err != nil {
		return nil, err
	}
	storeWorker, err := storeWorkerBundle()
	if err != nil {
		return nil, err
	}
	writerScript, err := isrWriterScriptNameFor(class)
	if err != nil {
		return nil, err
	}
	writerWorker, err := isrWriterBundle()
	if err != nil {
		return nil, err
	}
	writerWorker.ObjectStore = edge.ObjectStore{Binding: cacheStoreBinding, Bucket: cacheStoreName(class)}

	return []bootstrapWorker{
		{
			scriptName: storeScript,
			worker:     storeWorker,
			do:         deploymentsStoreWorker,
			what:       "deployments-store worker",
			offer:      edge.OfferDeploymentsStore,
			keys: offerKeys{
				endpoint:   edge.OfferKeyStoreEndpoint,
				scriptName: edge.OfferKeyStoreScriptName,
				cred:       edge.OfferKeyStoreBootstrapCred,
			},
		},
		{
			scriptName: writerScript,
			worker:     writerWorker,
			do:         isrWriterWorker,
			what:       "isr-writer worker",
			offer:      edge.OfferISRWriter,
			keys: offerKeys{
				endpoint:   edge.OfferKeyISRWriterEndpoint,
				scriptName: edge.OfferKeyISRWriterScriptName,
				cred:       edge.OfferKeyISRWriterBootstrapCred,
			},
		},
	}, nil
}

func (p *provider) settleWorker(ctx context.Context, accountID string, state workerState) (edge.Offer, error) {
	b := state.bootstrapWorker
	up := upload{accountID: accountID, scriptName: b.scriptName, worker: b.worker}
	var cred string
	var inherited []string
	if state.secretHeld {
		inherited = []string{bootstrapSecretBinding}
	} else {
		minted, err := mintSecret()
		if err != nil {
			return edge.Offer{}, fmt.Errorf("mint bootstrap credential: %w", err)
		}
		cred = minted
		up.worker = withSecret(b.worker, bootstrapSecretBinding, cred)
	}

	if !state.settled() || cred != "" {
		if err := p.putDurableObjectScript(ctx, up, b.do, state.classes, inherited); err != nil {
			return edge.Offer{}, fmt.Errorf("put %s: %w", b.what, err)
		}
	}

	endpoint, err := p.settleSubdomain(ctx, up, state.subdomainOn, b.what)
	if err != nil {
		return edge.Offer{}, err
	}

	values := map[string]string{
		b.keys.endpoint:   endpoint,
		b.keys.scriptName: b.scriptName,
	}
	if cred != "" {
		values[b.keys.cred] = cred
	}
	return edge.Offer{Kind: b.offer, Values: values}, nil
}

func (p *provider) settleSubdomain(ctx context.Context, up upload, on bool, what string) (string, error) {
	if !on {
		endpoint, err := p.setSubdomain(ctx, up, true)
		if err != nil {
			return "", fmt.Errorf("set %s subdomain: %w", what, err)
		}
		return endpoint, nil
	}
	endpoint, err := p.subdomainURL(ctx, up.accountID, up.scriptName)
	if err != nil {
		return "", fmt.Errorf("read %s subdomain: %w", what, err)
	}
	return endpoint, nil
}

func storeWorkerBundle() (edge.Worker, error) {
	bundles, err := edge.LoadStoreBundleManifest()
	if err != nil {
		return edge.Worker{}, err
	}
	return bundleWorker(bundles)
}

func isrWriterBundle() (edge.Worker, error) {
	bundles, err := edge.LoadISRWriterBundleManifest()
	if err != nil {
		return edge.Worker{}, err
	}
	return bundleWorker(bundles)
}

func bundleWorker(bundles edge.KindBundleManifest) (edge.Worker, error) {
	path, err := bundles.Path(Kind)
	if err != nil {
		return edge.Worker{}, err
	}
	return readWorkerBundle(path)
}

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

func (p *provider) VerifyCredentials(ctx context.Context) (edge.CredentialIdentity, error) {
	accountID := os.Getenv(envAccountID)
	if accountID == "" {
		return edge.CredentialIdentity{}, fmt.Errorf("%s is not set", envAccountID)
	}
	if os.Getenv(envAPIToken) == "" {
		return edge.CredentialIdentity{}, fmt.Errorf("%s is not set", envAPIToken)
	}
	if _, err := p.client.Accounts.Get(ctx, accounts.AccountGetParams{AccountID: cf.F(accountID)}); err != nil {
		return edge.CredentialIdentity{}, fmt.Errorf("%s was rejected by Cloudflare for account %s: %w", envAPIToken, accountID, err)
	}
	plan, entitlement := p.workersPlan(ctx, accountID)
	return edge.CredentialIdentity{Account: accountID, Plan: plan, CodeEntitlement: entitlement}, nil
}

const workersPaidPlan = "Workers Paid"

const workersFreePlan = "Workers Free"

func (p *provider) workersPlan(ctx context.Context, accountID string) (string, edge.Entitlement) {
	page, err := p.client.Accounts.Subscriptions.Get(ctx, accounts.SubscriptionGetParams{AccountID: cf.F(accountID)})
	if err != nil {
		fmt.Fprintf(os.Stderr,
			"ocel cloudflare edge: could not read the subscriptions of account %s: %v\n"+
				"%s must carry the \"Billing Read\" permission (Account scope) to tell whether the plan runs code at the edge. "+
				"Without it this deploy proceeds, and an account on the Workers Free plan is rejected by Cloudflare when the "+
				"worker is uploaded, after the deploy has begun changing your infrastructure\n",
			accountID, err, envAPIToken)
		return "", edge.EntitlementUnknown
	}
	for _, sub := range page.Result {
		if runsWorkerCode(sub.RatePlan) {
			name := sub.RatePlan.PublicName
			if name == "" {
				name = workersPaidPlan
			}
			return name, edge.EntitlementGranted
		}
	}
	return workersFreePlan, edge.EntitlementWithheld
}

func runsWorkerCode(plan shared.RatePlan) bool {
	if plan.IsContract || plan.ID == shared.RatePlanIDEnterprise || plan.ID == shared.RatePlanIDPartnersEnterprise {
		return true
	}
	id := strings.ToLower(string(plan.ID))
	return strings.Contains(id, "workers") && !strings.Contains(id, "free")
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

type upload struct {
	accountID  string
	scriptName string
	worker     edge.Worker
}

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

func hashAsset(a edge.StaticAsset) string {
	ext := strings.TrimPrefix(path.Ext(a.Path), ".")
	sum := sha256.Sum256([]byte(base64.StdEncoding.EncodeToString(a.Content) + ext))
	return hex.EncodeToString(sum[:])[:32]
}

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

func buildScriptMultipart(worker edge.Worker, assetsJWT string) ([]byte, string, error) {
	includeAssets := assetsJWT != ""
	metadata := map[string]any{
		"main_module":         worker.Main.Name,
		"compatibility_date":  compatDate,
		"compatibility_flags": compatFlags,
		"observability":       observability(),
		"bindings":            scriptBindings(worker, includeAssets),
	}
	if includeAssets {
		metadata["assets"] = map[string]any{
			"jwt":    assetsJWT,
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

const cacheStoreBinding = "OCEL_CACHE_STORE"

func bindObjectStore(worker edge.Worker, values map[string]string) edge.Worker {
	worker.ObjectStore.Binding = cacheStoreBinding
	worker.ObjectStore.Bucket = values[valueKeyCacheBucket]
	return worker
}

const codeLoaderBinding = "LOADER"

func bindCodeLoader(worker edge.Worker) edge.Worker {
	worker.LoaderBinding = codeLoaderBinding
	return worker
}

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
	return p.subdomainURL(ctx, up.accountID, up.scriptName)
}

func (p *provider) subdomainURL(ctx context.Context, accountID, scriptName string) (string, error) {
	account, err := p.client.Workers.Subdomains.Get(ctx, workers.SubdomainGetParams{
		AccountID: cf.F(accountID),
	})
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("https://%s.%s.workers.dev", scriptName, account.Subdomain), nil
}
