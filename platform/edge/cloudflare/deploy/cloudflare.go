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
	"maps"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"os"
	"path"
	"slices"
	"strings"
	"sync"
	"time"

	cf "github.com/cloudflare/cloudflare-go/v4"
	"github.com/cloudflare/cloudflare-go/v4/option"
	"github.com/cloudflare/cloudflare-go/v4/r2"
	"github.com/cloudflare/cloudflare-go/v4/workers"

	"github.com/ocelhq/ocel/pkg/contenttype"
	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/processenv"
	"github.com/ocelhq/ocel/platform/edge/cloudflare/deploy/bundles"
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

type cloudflare struct {
	client    *cf.Client
	store     *http.Client
	namespace string
	options   Options
	objects   func(endpoint string, creds r2.TemporaryCredentialNewResponse) objectAPI

	workerClientCertificate bool
	trustClientCertificate  ClientCertificateTrust
	skipChecks              bool

	zoneMu    sync.Mutex
	zonesSeen map[string][]zoneRef

	entryMu      sync.Mutex
	entryWorkers map[string][]string
}

func New(namespace string, options Options) edge.Edge { return newCloudflare(namespace, options) }

type ClientCertificateTrust func(ctx context.Context, tier environment.Tier, offer edge.Offer) error

func NewWithWorkerClientCertificate(namespace string, options Options, trust ClientCertificateTrust) edge.Edge {
	front := newCloudflare(namespace, options)
	front.workerClientCertificate = true
	front.trustClientCertificate = trust
	return front
}

func newCloudflare(namespace string, options Options) *cloudflare {
	return &cloudflare{client: cf.NewClient(option.WithMaxRetries(clientMaxRetries)), namespace: namespace, options: options, skipChecks: processenv.SkipChecks()}
}

func NewAt(namespace, baseURL string) edge.Edge {
	return &cloudflare{client: cf.NewClient(option.WithMaxRetries(clientMaxRetries), option.WithBaseURL(baseURL)), namespace: namespace, skipChecks: processenv.SkipChecks()}
}

const clientMaxRetries = 5

func (p *cloudflare) Kind() edge.Kind { return Kind }

func readAccountID() string { return os.Getenv(envAccountID) }

func requireAccountID(doing string) (string, error) {
	accountID := readAccountID()
	if accountID == "" {
		return "", fmt.Errorf("%s is not set; it is required to %s", envAccountID, doing)
	}
	return accountID, nil
}

func (p *cloudflare) cacheStore() cacheStore {
	store := newCacheStore(p.client, p.namespace)
	if p.objects != nil {
		store.objects = p.objects
	}
	return store
}

func (p *cloudflare) Facts() edge.Facts {
	return edge.Facts{
		Supported:          edge.AllNeeds(),
		Compatibility:      edge.Compatibility{Date: compatDate, Flags: slices.Clone(compatFlags)},
		RunsCode:           true,
		Entry:              workerModule(entryBundle),
		ServesUnbound:      true,
		ProxiesRecords:     true,
		ProxiedRecordNote:  "Turn the Cloudflare proxy (orange cloud) on for these records: each value is a placeholder Cloudflare answers behind.",
		OriginFacingRanges: listOriginFacingRanges(),
		TunnelsToOrigin:    p.options.Tunnel,
		CredentialScope:    readAccountID(),
	}
}

func (p *cloudflare) Hooks() edge.Hooks {
	return edge.Hooks{
		PlanBootstrap:                 p.planBootstrap,
		PlanRemoveBootstrap:           p.planRemoveBootstrap,
		PlanAdoption:                  p.adoption,
		DescribeBootstrap:             p.describeBootstrap,
		VerifyCredentials:             p.verifyCredentials,
		CheckCodeEntitlement:          p.codeEntitlement,
		DescribeCredentialPermissions: p.describeCredentialPermissions,
		ClientCertificates: &edge.ClientCertificateHooks{
			Ensure:  p.ensureClientCertificates,
			Present: p.presentClientCertificates,
		},
	}
}

const recordTTL = 5 * time.Second

func (p *cloudflare) ProjectRemovals(scope edge.ProjectScope) []edge.PlanGroup {
	changes := []edge.PlanChange{{
		Kind:   kindWorker,
		Name:   scope.Slug,
		Action: edge.PlanDelete,
		Reason: "every per-app worker this project deployed, and the routes that reach them",
	}}
	for _, hostname := range scope.Hostnames {
		changes = append(changes, edge.PlanChange{
			Kind:   kindWorkerRoute,
			Name:   hostname,
			Action: edge.PlanDelete,
		})
	}
	changes = append(changes, edge.PlanChange{
		Kind:   kindDurableObject,
		Name:   scope.Slug,
		Action: edge.PlanDelete,
		Reason: "every deployment and pointer this project promoted",
	})
	return []edge.PlanGroup{{
		Kind:    edge.EdgeGroupKind,
		Name:    edge.EdgeGroupName(Kind),
		Action:  edge.PlanDelete,
		Changes: changes,
	}}
}

func (p *cloudflare) PreviewWildcardRemovals(wildcard string) (edge.PlanGroup, edge.PlanGroup) {
	removed := edge.PlanGroup{
		Kind:   edge.EdgeGroupKind,
		Name:   edge.EdgeGroupName(Kind),
		Action: edge.PlanDelete,
		Changes: []edge.PlanChange{
			{Kind: kindWorkerRoute, Name: wildcard, Action: edge.PlanDelete},
			{Kind: kindWorker, Name: previewEntryScript, Action: edge.PlanDelete,
				Reason: "the shared entry worker this wildcard is owned by"},
		},
	}
	kept := edge.PlanGroup{
		Kind:   edge.EdgeGroupKind,
		Name:   edge.EdgeGroupName(Kind),
		Action: edge.PlanKeep,
		Reason: "bootstrap-scoped: `ocel bootstrap destroy preview` removes what bootstrap provisioned",
	}
	return removed, kept
}

func (p *cloudflare) SharedPreviewRemoval() edge.PlanGroup {
	return edge.PlanGroup{
		Kind:   edge.EdgeGroupKind,
		Name:   edge.EdgeGroupName(Kind),
		Action: edge.PlanKeep,
		Reason: "bootstrap-scoped: " + edge.PreviewEntryOwner + " fronts every project's previews",
	}
}

func (p *cloudflare) Bootstrap(ctx context.Context, tier environment.Tier) (edge.BootstrapOutput, error) {
	accountID, err := bootstrapCredentials()
	if err != nil {
		return edge.BootstrapOutput{}, err
	}
	state, err := p.readState(ctx, accountID, tier)
	if err != nil {
		return edge.BootstrapOutput{}, err
	}
	out, err := p.cacheStore().bootstrap(ctx, accountID, state.store)
	if err != nil {
		return out, err
	}
	for _, worker := range state.workers {
		offer, err := p.ensureWorker(ctx, accountID, worker)
		if err != nil {
			return out, fmt.Errorf("bootstrap %s: %w", worker.what, err)
		}
		out.Offers = append(out.Offers, offer)
	}
	if p.workerClientCertificate {
		offer, err := p.workerClientCertificates().ensure(ctx, accountID, state.certificates)
		if err != nil {
			return out, fmt.Errorf("bootstrap the worker client certificate: %w", err)
		}
		out.Offers = append(out.Offers, offer)
		if p.trustClientCertificate != nil {
			if err := p.trustClientCertificate(ctx, tier, offer); err != nil {
				return out, fmt.Errorf("make the origin trust the worker client certificate's CA before the refresher presents it: %w", err)
			}
		}
		if err := p.ensureRefreshQueue(ctx, accountID, state.refresh, offer.Values[edge.OfferKeyClientCertificateID]); err != nil {
			return out, fmt.Errorf("bootstrap the refresh queue: %w", err)
		}
	}
	return out, nil
}

func (p *cloudflare) Teardown(ctx context.Context, tier environment.Tier) error {
	accountID, err := requireAccountID("tear the Cloudflare edge down")
	if err != nil {
		return err
	}
	storeScript, err := storeScriptNameFor(p.namespace, tier)
	if err != nil {
		return err
	}
	writerScript, err := isrWriterScriptNameFor(p.namespace, tier)
	if err != nil {
		return err
	}
	var errs []error
	for _, name := range []string{storeScript, writerScript} {
		if err := p.deleteScript(ctx, accountID, name); err != nil {
			errs = append(errs, fmt.Errorf("delete worker %q: %w", name, err))
		}
	}
	if p.workerClientCertificate {
		if err := p.tearDownRefreshQueue(ctx, accountID, tier); err != nil {
			errs = append(errs, err)
		}
	}
	if err := p.cacheStore().teardown(ctx, accountID, tier); err != nil {
		errs = append(errs, err)
	}
	if p.workerClientCertificate {
		if err := p.workerClientCertificates().teardown(ctx, accountID, tier); err != nil {
			errs = append(errs, err)
		}
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

func bootstrapWorkers(namespace string, tier environment.Tier) ([]bootstrapWorker, error) {
	storeScript, err := storeScriptNameFor(namespace, tier)
	if err != nil {
		return nil, err
	}
	storeWorker := edge.Worker{Main: workerModule(releasesStoreBundle)}
	writerScript, err := isrWriterScriptNameFor(namespace, tier)
	if err != nil {
		return nil, err
	}
	writerWorker := edge.Worker{Main: workerModule(isrWriterBundle)}
	bucket, err := cacheStoreNameFor(namespace, tier)
	if err != nil {
		return nil, err
	}
	writerWorker.ObjectStore = edge.ObjectStore{Binding: cacheStoreBinding, Bucket: bucket}

	return []bootstrapWorker{
		{
			scriptName: storeScript,
			worker:     storeWorker,
			do:         releasesStoreWorker,
			what:       "releases-store worker",
			offer:      edge.OfferReleasesStore,
			keys: offerKeys{
				endpoint:   edge.OfferKeyStoreEndpoint,
				scriptName: edge.OfferKeyStoreScriptName,
				cred:       edge.OfferKeyStoreBootstrapCredential,
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
				cred:       edge.OfferKeyISRWriterBootstrapCredential,
			},
		},
	}, nil
}

func (p *cloudflare) ensureWorker(ctx context.Context, accountID string, state workerState) (edge.Offer, error) {
	b := state.bootstrapWorker
	up := upload{accountID: accountID, scriptName: b.scriptName, worker: b.worker}
	var cred string
	if !state.secretPresent {
		minted, err := mintSecret()
		if err != nil {
			return edge.Offer{}, fmt.Errorf("mint bootstrap credential: %w", err)
		}
		cred = minted
	}

	switch {
	case !state.upToDate():
		var inherited []string
		if cred == "" {
			inherited = []string{bootstrapSecretBinding}
		} else {
			up.worker = withSecret(b.worker, bootstrapSecretBinding, cred)
		}
		if err := p.putDurableObjectScript(ctx, up, b.do, state.classes, inherited); err != nil {
			return edge.Offer{}, fmt.Errorf("put %s: %w", b.what, err)
		}
	case cred != "":
		if err := p.putBootstrapSecret(ctx, accountID, b.scriptName, cred); err != nil {
			return edge.Offer{}, fmt.Errorf("set the bootstrap credential of %s: %w", b.what, err)
		}
	}

	endpoint, err := p.ensureSubdomain(ctx, up, state.subdomainOn, b.what)
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

func (p *cloudflare) ensureSubdomain(ctx context.Context, up upload, on bool, what string) (string, error) {
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

var (
	entryBundle         = bundles.Entry()
	releasesStoreBundle = bundles.ReleasesStore()
	isrWriterBundle     = bundles.ISRWriter()
	refresherBundle     = bundles.Refresher()
)

func workerModule(content []byte) edge.WorkerModule {
	return edge.WorkerModule{
		Name:        "index.js",
		ContentType: "application/javascript+module",
		Content:     content,
	}
}

type upload struct {
	accountID  string
	scriptName string
	worker     edge.Worker
}

func (p *cloudflare) uploadAssets(ctx context.Context, up upload) (string, error) {
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
		contentType := contenttype.Infer(asset.Path)
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

func (p *cloudflare) putScript(ctx context.Context, up upload, assetsJWT string) error {
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
	for name, text := range worker.Variables {
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
	for _, name := range slices.Sorted(maps.Keys(worker.ClientCertificates)) {
		bindings = append(bindings, map[string]any{
			"type":           "mtls_certificate",
			"name":           name,
			"certificate_id": worker.ClientCertificates[name],
		})
	}
	for _, name := range slices.Sorted(maps.Keys(worker.Queues)) {
		bindings = append(bindings, map[string]any{
			"type":       "queue",
			"name":       name,
			"queue_name": worker.Queues[name],
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

func (p *cloudflare) setSubdomain(ctx context.Context, up upload, enabled bool) (string, error) {
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

func (p *cloudflare) subdomainURL(ctx context.Context, accountID, scriptName string) (string, error) {
	account, err := p.client.Workers.Subdomains.Get(ctx, workers.SubdomainGetParams{
		AccountID: cf.F(accountID),
	})
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("https://%s.%s.workers.dev", scriptName, account.Subdomain), nil
}
