package cloudflare

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"mime/multipart"
	"net/textproto"
	"path"
	"slices"
	"strings"

	cf "github.com/cloudflare/cloudflare-go/v4"
	"github.com/cloudflare/cloudflare-go/v4/option"
	"github.com/cloudflare/cloudflare-go/v4/workers"
	"github.com/ocelhq/ocel/pkg/contenttype"
	"github.com/ocelhq/ocel/platform/edge/cloudflare/deploy/bundles"
)

var (
	entryBundle         = bundles.Entry()
	releasesStoreBundle = bundles.ReleasesStore()
)

func mainModule(content []byte) workerModule {
	return workerModule{
		Name:        "index.js",
		ContentType: "application/javascript+module",
		Content:     content,
	}
}

type upload struct {
	accountID  string
	scriptName string
	worker     workerScript
}

func (p *cloudflare) uploadAssets(ctx context.Context, up upload) (string, error) {
	if len(up.worker.Assets) == 0 {
		return "", nil
	}

	manifest := make(map[string]workers.ScriptAssetUploadNewParamsManifest, len(up.worker.Assets))
	assetByHash := make(map[string]workerAsset, len(up.worker.Assets))
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

func hashAsset(a workerAsset) string {
	ext := strings.TrimPrefix(path.Ext(a.Path), ".")
	sum := sha256.Sum256([]byte(base64.StdEncoding.EncodeToString(a.Content) + ext))
	return hex.EncodeToString(sum[:])[:32]
}

func buildAssetBatch(bucket []string, assetByHash map[string]workerAsset) ([]byte, string, error) {
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

func buildScriptMultipart(worker workerScript, assetsJWT string) ([]byte, string, error) {
	includeAssets := assetsJWT != ""
	metadata := map[string]any{"bindings": scriptBindings(worker, includeAssets)}
	if len(worker.CachedEntrypoints) > 0 {
		metadata["cache_options"] = map[string]any{"enabled": false, "cross_version_cache": true}
	}
	if exports := buildWorkerExports(worker); len(exports) > 0 {
		metadata["exports"] = exports
	}
	if includeAssets {
		metadata["assets"] = map[string]any{
			"jwt":    assetsJWT,
			"config": map[string]any{"run_worker_first": true},
		}
	}
	return encodeScript(worker, metadata)
}

func encodeScript(worker workerScript, metadata map[string]any) ([]byte, string, error) {
	metadata["main_module"] = worker.Main.Name
	metadata["compatibility_date"] = compatDate
	metadata["compatibility_flags"] = compatFlags
	metadata["observability"] = observability()
	metadataJSON, err := json.Marshal(metadata)
	if err != nil {
		return nil, "", fmt.Errorf("marshal %s worker metadata: %w", worker.Main.Name, err)
	}

	buf := &bytes.Buffer{}
	w := multipart.NewWriter(buf)
	if err := writePart(w, "metadata", "", "application/json", metadataJSON); err != nil {
		return nil, "", err
	}
	for _, mod := range append([]workerModule{worker.Main}, worker.Modules...) {
		if err := writePart(w, mod.Name, mod.Name, mod.ContentType, mod.Content); err != nil {
			return nil, "", err
		}
	}
	if err := w.Close(); err != nil {
		return nil, "", err
	}
	return buf.Bytes(), w.FormDataContentType(), nil
}

func buildWorkerExports(worker workerScript) map[string]any {
	exports := make(map[string]any, len(worker.CachedEntrypoints)+len(worker.DurableObjects))
	for _, name := range worker.CachedEntrypoints {
		exports[name] = map[string]any{"type": "worker", "cache": map[string]any{"enabled": true}}
	}
	for _, name := range worker.DurableObjects {
		exports[name] = map[string]any{"type": "durable-object", "storage": "sqlite"}
	}
	return exports
}

const codeLoaderBinding = "LOADER"

func bindCodeLoader(worker workerScript) workerScript {
	worker.LoaderBinding = codeLoaderBinding
	return worker
}

func scriptBindings(worker workerScript, includeAssets bool) []map[string]any {
	bindings := []map[string]any{}
	if includeAssets && worker.AssetBinding != "" {
		bindings = append(bindings, map[string]any{
			"type": "assets",
			"name": worker.AssetBinding,
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

func buildDurableObjectScriptMultipart(worker workerScript, do durableObjectWorker, deployedClasses, inherited []string) ([]byte, string, error) {
	bindings := scriptBindings(worker, false)
	for _, class := range do.classes {
		bindings = append(bindings, map[string]any{
			"type":       durableObjectBindingType,
			"name":       class.binding,
			"class_name": class.className,
		})
	}
	for _, name := range inherited {
		bindings = append(bindings, map[string]any{
			"type": inheritedBindingType,
			"name": name,
		})
	}
	metadata := map[string]any{"bindings": bindings}
	migrations, err := pendingMigrations(do.migrations, deployedClasses)
	if err != nil {
		return nil, "", err
	}
	if migrations != nil {
		metadata["migrations"] = migrations
	}
	return encodeScript(worker, metadata)
}

func pendingMigrations(log []migrationStep, deployedClasses []string) (map[string]any, error) {
	for _, class := range deployedClasses {
		if !slices.ContainsFunc(log, func(step migrationStep) bool { return slices.Contains(step.sqliteClasses, class) }) {
			return nil, fmt.Errorf("deployed script declares Durable Object class %q, which this build's migration log does not create", class)
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
