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

	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

const (
	envAccountID = "CLOUDFLARE_ACCOUNT_ID"
	envAPIToken  = "CLOUDFLARE_API_TOKEN"
)

const compatDate = "2026-07-13"

var compatFlags = []string{"nodejs_compat"}

const (
	routeRecordContent = "100::"
	routeRecordComment = "managed by ocel — worker route placeholder"
)

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
}

var _ edge.RootStack = (*provider)(nil)

func New() edge.Provider {
	return &provider{client: cf.NewClient(option.WithMaxRetries(clientMaxRetries))}
}

const clientMaxRetries = 5

func (p *provider) Kind() edge.Kind { return edge.KindCloudflare }

func (p *provider) CodeRuntime() (string, []string) { return compatDate, compatFlags }

func (p *provider) Bootstrap(ctx context.Context, class edge.Class) (edge.BootstrapOutput, error) {
	accountID := os.Getenv(envAccountID)
	if accountID == "" {
		return edge.BootstrapOutput{}, fmt.Errorf("%s is not set; it is required to bootstrap the Cloudflare edge", envAccountID)
	}
	out, err := newCacheStore(p.client).bootstrap(ctx, accountID, class)
	if err != nil {
		return out, err
	}
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

	deployed, err := p.deployedClasses(ctx, scriptName)
	if err != nil {
		return edge.Offer{}, fmt.Errorf("read isr-writer worker Durable Object classes: %w", err)
	}
	cred, err := mintSecret()
	if err != nil {
		return edge.Offer{}, fmt.Errorf("mint bootstrap credential: %w", err)
	}

	worker.ObjectStore = edge.ObjectStore{Binding: cacheStoreBinding, Bucket: cacheStoreName(class)}
	up := upload{accountID: accountID, scriptName: scriptName, worker: withSecret(worker, bootstrapSecretBinding, cred)}
	if err := p.putDurableObjectScript(ctx, up, isrWriterWorker, deployed); err != nil {
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

	deployed, err := p.deployedClasses(ctx, scriptName)
	if err != nil {
		return edge.Offer{}, fmt.Errorf("read deployments-store worker Durable Object classes: %w", err)
	}
	cred, err := mintSecret()
	if err != nil {
		return edge.Offer{}, fmt.Errorf("mint bootstrap credential: %w", err)
	}

	up := upload{accountID: accountID, scriptName: scriptName, worker: withSecret(worker, bootstrapSecretBinding, cred)}
	if err := p.putDurableObjectScript(ctx, up, deploymentsStoreWorker, deployed); err != nil {
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

func (p *provider) deployedClasses(ctx context.Context, name string) ([]string, error) {
	accountID := os.Getenv(envAccountID)
	if accountID == "" {
		return nil, fmt.Errorf("%s is not set; it is required to query the Cloudflare edge", envAccountID)
	}
	settings, err := p.client.Workers.Scripts.ScriptAndVersionSettings.Get(ctx, name, workers.ScriptScriptAndVersionSettingGetParams{
		AccountID: cf.F(accountID),
	})
	var apiErr *cf.Error
	if errors.As(err, &apiErr) && apiErr.StatusCode == http.StatusNotFound {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var classes []string
	for _, binding := range settings.Bindings {
		if binding.Type == durableObjectBindingType && binding.ClassName != "" && binding.ScriptName == "" {
			classes = append(classes, binding.ClassName)
		}
	}
	return classes, nil
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

	account, err := p.client.Workers.Subdomains.Get(ctx, workers.SubdomainGetParams{
		AccountID: cf.F(up.accountID),
	})
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("https://%s.%s.workers.dev", up.scriptName, account.Subdomain), nil
}

type routePlan struct {
	desired        []string
	prune          bool
	pruneStem      string
	requiredRecord string
}

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
		if err := p.ensureRoute(ctx, zoneID, RoutePattern(host), up.scriptName); err != nil {
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
	return p.pruneStaleRoutes(ctx, up, plan.pruneStem, wanted)
}

func (p *provider) pruneStaleRoutes(ctx context.Context, up upload, stem string, wanted map[string]bool) error {
	owned := p.client.Zones.ListAutoPaging(ctx, zones.ZoneListParams{
		Account: cf.F(zones.ZoneListParamsAccount{ID: cf.F(up.accountID)}),
	})
	var errs []error
	for owned.Next() {
		zoneID := owned.Current().ID
		routes := p.client.Workers.Routes.ListAutoPaging(ctx, workers.RouteListParams{ZoneID: cf.F(zoneID)})
		for routes.Next() {
			route := routes.Current()
			if route.Script != up.scriptName && !edge.NameUnderStem(stem, route.Script) {
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

func routeBaseDomain(host string) string {
	return strings.TrimPrefix(host, "*.")
}

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

func RoutePattern(hostname string) string {
	return hostname + "/*"
}

func (p *provider) RouteOwner(ctx context.Context, pattern string) (string, error) {
	accountID := os.Getenv(envAccountID)
	if accountID == "" {
		return "", fmt.Errorf("%s is not set; it is required to read Cloudflare worker routes", envAccountID)
	}
	zoneID, _, err := p.resolveZone(ctx, accountID, routeBaseDomain(strings.TrimSuffix(pattern, "/*")))
	if err != nil {
		return "", err
	}
	routes := p.client.Workers.Routes.ListAutoPaging(ctx, workers.RouteListParams{ZoneID: cf.F(zoneID)})
	for routes.Next() {
		if route := routes.Current(); route.Pattern == pattern {
			return route.Script, nil
		}
	}
	if err := routes.Err(); err != nil {
		return "", fmt.Errorf("list worker routes in zone %s: %w", zoneID, err)
	}
	return "", nil
}

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

func isAddressRecord(t dns.RecordResponseType) bool {
	switch t {
	case dns.RecordResponseTypeA, dns.RecordResponseTypeAAAA, dns.RecordResponseTypeCNAME:
		return true
	default:
		return false
	}
}

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
