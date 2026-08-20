package deploy

import (
	"fmt"
	"os"
	"strconv"
	"strings"

	deploymentsv1 "github.com/ocelhq/ocel/pkg/proto/deployments/v1"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

type WorkerFacts struct {
	Region             string
	StateTable         string
	AssetBucket        string
	ImageOptimizerURL  string
	RevalidateQueueURL string
	EdgeAccessKeyID    string
	EdgeSecretKey      string
}

func sharedWorker(e edge.Edge, f WorkerFacts) (edge.Worker, error) {
	worker, err := genericWorkerBundle(e)
	if err != nil {
		return edge.Worker{}, err
	}
	vars := map[string]string{
		edge.OriginBodyLimitVar:    strconv.Itoa(lambdaOriginBodyLimitBytes),
		edge.OriginBodyEncodingVar: edge.OriginBodyEncodingBase64,
	}
	for name, value := range map[string]string{
		edge.AWSRegionVar:          f.Region,
		edge.StateTableVar:         f.StateTable,
		edge.AssetBucketVar:        f.AssetBucket,
		edge.ImageOptimizerURLVar:  f.ImageOptimizerURL,
		edge.RevalidateQueueURLVar: f.RevalidateQueueURL,
	} {
		if value != "" {
			vars[name] = value
		}
	}
	if f.EdgeAccessKeyID != "" && f.EdgeSecretKey != "" {
		vars[edge.EdgeAccessKeyIDVar] = f.EdgeAccessKeyID
		worker.Secrets = map[string]string{edge.EdgeSecretKeyVar: f.EdgeSecretKey}
	}
	worker.Vars = vars
	return worker, nil
}

const (
	envPreview           = "OCEL_PREVIEW"
	envPreviewBaseDomain = "OCEL_PREVIEW_BASE_DOMAIN"
	envPreviewApps       = "OCEL_PREVIEW_APPS"
)

func withPreviewVars(worker edge.Worker, baseDomain string, apps []*deploymentsv1.ManifestApp) edge.Worker {
	worker = withVar(worker, envPreview, "1")
	worker = withVar(worker, envPreviewApps, previewAppNames(apps))
	if baseDomain != "" {
		worker = withVar(worker, envPreviewBaseDomain, baseDomain)
	}
	return worker
}

func previewAppNames(apps []*deploymentsv1.ManifestApp) string {
	names := make([]string, 0, len(apps))
	for _, app := range apps {
		if name := strings.ToLower(strings.TrimSpace(app.GetName())); name != "" {
			names = append(names, name)
		}
	}
	return strings.Join(names, ",")
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

func genericWorkerBundle(e edge.Edge) (edge.Worker, error) {
	bundles, err := edge.LoadBundleManifest()
	if err != nil {
		return edge.Worker{}, err
	}
	path, err := bundles.Path(e.Kind())
	if err != nil {
		return edge.Worker{}, err
	}
	return loadWorkerBundle(path)
}

func loadWorkerBundle(path string) (edge.Worker, error) {
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
