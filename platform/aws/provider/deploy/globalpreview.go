package deploy

import (
	"fmt"
	"maps"

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
		Version:             rootStackVersion,
		ScriptName:          edge.SharedPreviewEntryScript,
		Generic:             generic,
		BaseDomain:          baseDomain,
		GrammarMin:          edge.PreviewGrammarMin,
		GrammarMax:          edge.PreviewGrammarMax,
		ISRWriterScriptName: cfg.ISRWriterScriptName,
		Values:              cfg.EdgeValues,
		Warn:                warn,
	}, nil
}

func MarkGlobalPreview(state edge.RootStackState, cfg Config, manifest *deploymentsv1.Manifest) edge.RootStackState {
	if len(state) == 0 || cfg.Class != deploymentsv1.Environment_CLASS_PREVIEW {
		return state
	}
	marked := maps.Clone(state)
	if servesOnGlobalPreviewDomain(cfg, manifest) {
		marked[edge.RootStackKeyGlobalPreview] = cfg.GlobalPreviewDomain
	} else {
		delete(marked, edge.RootStackKeyGlobalPreview)
	}
	return marked
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
