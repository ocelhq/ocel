package deploy

import (
	"fmt"
	"strings"

	"github.com/ocelhq/ocel/pkg/edge"
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

const (
	envPreview           = "OCEL_PREVIEW"
	envPreviewGlobal     = "OCEL_PREVIEW_GLOBAL"
	envPreviewBaseDomain = "OCEL_PREVIEW_BASE_DOMAIN"
	envPreviewApps       = "OCEL_PREVIEW_APPS"

	storeServiceBinding = "DEPLOYMENTS"
)

func sharedWorker(kind edge.Kind, entry edge.WorkerModule, f WorkerFacts) (edge.Worker, error) {
	if len(entry.Content) == 0 {
		return edge.Worker{}, fmt.Errorf("the %s edge names no entry module for its worker to run", kind)
	}
	worker := edge.Worker{Main: entry}
	vars := map[string]string{}
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

func withPreviewVars(worker edge.Worker, baseDomain string, apps []string) edge.Worker {
	worker = withVar(worker, envPreview, "1")
	worker = withVar(worker, envPreviewApps, previewAppNames(apps))
	if baseDomain != "" {
		worker = withVar(worker, envPreviewBaseDomain, baseDomain)
	}
	return worker
}

func previewAppNames(apps []string) string {
	names := make([]string, 0, len(apps))
	for _, app := range apps {
		if name := strings.ToLower(strings.TrimSpace(app)); name != "" {
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

func withService(worker edge.Worker, name, service string) edge.Worker {
	services := make(map[string]string, len(worker.Services)+1)
	for k, v := range worker.Services {
		services[k] = v
	}
	services[name] = service
	worker.Services = services
	return worker
}
