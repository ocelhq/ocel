package deploy

import (
	"fmt"

	deploymentsv1 "github.com/ocelhq/ocel/pkg/proto/deployments/v1"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

const (
	envPreviewGlobal = "OCEL_PREVIEW_GLOBAL"

	storeServiceBinding = "DEPLOYMENTS"
)

func declaresPreviewDomain(manifest *deploymentsv1.Manifest) bool {
	key := domainClassKeyFor(deploymentsv1.Environment_CLASS_PREVIEW)
	if len(manifest.GetDomains()[key].GetHostnames()) > 0 {
		return true
	}
	for _, app := range manifestApps(manifest) {
		if len(app.GetDomains()[key].GetHostnames()) > 0 {
			return true
		}
	}
	return false
}

func SharedPreviewEntrySpec(cfg Config, baseDomain string, warn func(string)) (edge.SharedEntrySpec, error) {
	if baseDomain == "" {
		return edge.SharedEntrySpec{}, fmt.Errorf("a preview domain is required")
	}
	generic, err := sharedWorker(cfg)
	if err != nil {
		return edge.SharedEntrySpec{}, err
	}
	if cfg.StoreScriptName == "" {
		return edge.SharedEntrySpec{}, fmt.Errorf("no deployments-store worker found for the preview substrate; re-run `ocel bootstrap --preview` to provision it")
	}
	generic = withService(generic, storeServiceBinding, cfg.StoreScriptName)
	generic = withVar(generic, envPreview, "1")
	generic = withVar(generic, envPreviewGlobal, "1")
	generic = withVar(generic, envPreviewBaseDomain, baseDomain)

	return edge.SharedEntrySpec{
		Version:    rootStackVersion,
		ScriptName: edge.SharedPreviewEntryScript,
		Generic:    generic,
		BaseDomain: baseDomain,
		GrammarMin: edge.PreviewGrammarMin,
		GrammarMax: edge.PreviewGrammarMax,
		Warn:       warn,
	}, nil
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

func PreviewLabelProblem(slug string, hostnames []string) error {
	return edge.PreviewLabelProblem(slug, hostnames)
}
