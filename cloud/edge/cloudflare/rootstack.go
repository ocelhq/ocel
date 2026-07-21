// Root-stack reconcile and deployments-store operations (ADR 0001/0002): the
// generic + DO worker upload reuses the same script-upload machinery
// cloudflare.go's DeployApp already exercises; the store operations are
// authenticated HTTP calls to the DO worker's fetch() surface
// (workers/deployments-store/src/index.ts).
package cloudflare

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
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

// writeSecretBinding is the env name the deployments-store worker reads its
// project write-secret from (workers/deployments-store/src/env.ts Env.WRITE_SECRET).
const writeSecretBinding = "WRITE_SECRET"

// writeSecretBytes is the byte length of a freshly minted project
// write-secret, hex-encoded on the wire.
const writeSecretBytes = 32

// The deployments-store worker's own Durable Object binding (mirroring its
// wrangler.jsonc: workers/deployments-store/wrangler.jsonc), which putScript's
// generic edge.Worker-driven binding set has no concept of — every deployment
// of this specific worker binds its own DeploymentsStore class under this
// name, and declares the class's one migration exactly once, on the deploy
// that first creates it.
const (
	doBindingName  = "DEPLOYMENTS_DO"
	doClassName    = "DeploymentsStore"
	doMigrationTag = "v1"
)

// genericStoreBinding is the env name the frozen generic worker reads its
// service binding to the deployments-store worker from
// (workers/nextjs/src/index.ts Env.DEPLOYMENTS), through which it resolves
// the active Deployment at request time.
const genericStoreBinding = "DEPLOYMENTS"

func (p *provider) ReconcileRootStack(ctx context.Context, spec edge.RootStackSpec, prior edge.RootStackState) (edge.RootStackState, error) {
	accountID := os.Getenv(envAccountID)
	if accountID == "" {
		return nil, fmt.Errorf("%s is not set; it is required to reconcile the Cloudflare root stack", envAccountID)
	}

	secret := prior[edge.RootStackKeyWriteSecret]
	endpoint := prior[edge.RootStackKeyEndpoint]
	fresh := secret == ""
	if fresh {
		minted, err := mintWriteSecret()
		if err != nil {
			return nil, fmt.Errorf("mint root-stack write secret: %w", err)
		}
		secret = minted
	}

	if !fresh {
		current, err := p.getVersionStamp(ctx, endpoint, secret)
		if err != nil {
			return nil, fmt.Errorf("read root-stack version stamp: %w", err)
		}
		if current == spec.Version {
			return prior, nil
		}
	}

	storeUp := upload{accountID: accountID, scriptName: spec.StoreName, worker: withSecret(spec.Store, writeSecretBinding, secret)}
	if err := p.putStoreScript(ctx, storeUp, fresh); err != nil {
		return nil, fmt.Errorf("put deployments-store worker: %w", err)
	}
	newEndpoint, err := p.setSubdomain(ctx, storeUp, true)
	if err != nil {
		return nil, fmt.Errorf("set deployments-store worker subdomain: %w", err)
	}
	endpoint = newEndpoint

	genericUp := upload{accountID: accountID, scriptName: spec.GenericName, worker: bindObjectStore(withService(spec.Generic, genericStoreBinding, spec.StoreName), spec.Values)}
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

	if err := p.putVersionStamp(ctx, endpoint, secret, spec.Version); err != nil {
		return nil, fmt.Errorf("set root-stack version stamp: %w", err)
	}

	return edge.RootStackState{
		edge.RootStackKeyEndpoint:    endpoint,
		edge.RootStackKeyWriteSecret: secret,
	}, nil
}

// putStoreScript uploads the deployments-store worker: like putScript, but it
// additionally binds the worker's own DeploymentsStore Durable Object class —
// a binding no plain edge.Worker carries, since it names a class the script
// itself exports rather than an external resource — and, only when migrate is
// true (the deploy that first creates the class), declares its one
// SQLite-backed migration. Redeclaring that migration on a later deploy would
// be at best redundant and at worst rejected, so every reconcile after the
// first omits it.
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
// deployments-store worker: the same module/binding shape, plus the
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

func (p *provider) getVersionStamp(ctx context.Context, endpoint, secret string) (string, error) {
	var out struct {
		Version *string `json:"version"`
	}
	if _, err := p.storeRequestTo(ctx, endpoint, secret, http.MethodGet, "/version-stamp", nil, &out); err != nil {
		return "", err
	}
	if out.Version == nil {
		return "", nil
	}
	return *out.Version, nil
}

func (p *provider) putVersionStamp(ctx context.Context, endpoint, secret, version string) error {
	_, err := p.storeRequestTo(ctx, endpoint, secret, http.MethodPut, "/version-stamp", map[string]string{"version": version}, nil)
	return err
}

// storeRequest issues an authenticated call against state's deployments-store
// endpoint and write secret.
func (p *provider) storeRequest(ctx context.Context, state edge.RootStackState, method, path string, body, out any) (*http.Response, error) {
	return p.storeRequestTo(ctx, state[edge.RootStackKeyEndpoint], state[edge.RootStackKeyWriteSecret], method, path, body, out)
}

// storeRequestTo issues one authenticated HTTP call to the deployments-store
// worker's fetch() surface, matching workers/deployments-store/src/index.ts:
// a Bearer write-secret, a JSON body when body is non-nil, and a JSON
// response decoded into out when out is non-nil. A non-2xx status is an
// error naming the path and status.
func (p *provider) storeRequestTo(ctx context.Context, endpoint, secret, method, path string, body, out any) (*http.Response, error) {
	if endpoint == "" {
		return nil, fmt.Errorf("deployments store: no endpoint in root-stack state; reconcile the root stack first")
	}

	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return nil, fmt.Errorf("marshal deployments-store request body: %w", err)
		}
		reader = bytes.NewReader(encoded)
	}

	req, err := http.NewRequestWithContext(ctx, method, endpoint+path, reader)
	if err != nil {
		return nil, fmt.Errorf("build deployments-store request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+secret)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("call deployments store %s %s: %w", method, path, err)
	}
	defer res.Body.Close()

	if res.StatusCode < 200 || res.StatusCode >= 300 {
		respBody, _ := io.ReadAll(res.Body)
		return res, fmt.Errorf("deployments store %s %s: status %d: %s", method, path, res.StatusCode, string(respBody))
	}
	if out != nil {
		if err := json.NewDecoder(res.Body).Decode(out); err != nil {
			return res, fmt.Errorf("decode deployments store %s %s response: %w", method, path, err)
		}
	}
	return res, nil
}

// mintWriteSecret generates the project write-secret minted once at root-stack
// creation, hex-encoded so it is safe to carry as a plain HTTP header value.
func mintWriteSecret() (string, error) {
	buf := make([]byte, writeSecretBytes)
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
