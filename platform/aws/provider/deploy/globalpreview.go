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
	return len(DeclaredHostnames(manifest, deploymentsv1.Environment_CLASS_PREVIEW)) > 0
}

type PreviewWildcard struct {
	Edge                edge.Edge
	Values              map[string]string
	Worker              WorkerFacts
	StoreScriptName     string
	ISRWriterScriptName string
}

func PreviewWildcardSpecFor(w PreviewWildcard, baseDomain string, warn func(string)) (edge.PreviewWildcardSpec, error) {
	if baseDomain == "" {
		return edge.PreviewWildcardSpec{}, fmt.Errorf("a preview domain is required")
	}
	spec := edge.PreviewWildcardSpec{
		Version:    stackVersion,
		BaseDomain: baseDomain,
		GrammarMin: edge.PreviewGrammarMin,
		GrammarMax: edge.PreviewGrammarMax,
		Values:     w.Values,
		Warn:       warn,
	}
	if _, programmable := w.Edge.(edge.Programmable); !programmable {
		return spec, nil
	}
	generic, err := sharedWorker(w.Edge, w.Worker)
	if err != nil {
		return edge.PreviewWildcardSpec{}, err
	}
	if w.StoreScriptName == "" {
		return edge.PreviewWildcardSpec{}, fmt.Errorf("no deployments-store worker found for the preview substrate; re-run `ocel bootstrap --preview` to provision it")
	}
	generic = withService(generic, storeServiceBinding, w.StoreScriptName)
	generic = withVar(generic, envPreview, "1")
	generic = withVar(generic, envPreviewGlobal, "1")
	generic = withVar(generic, envPreviewBaseDomain, baseDomain)

	spec.Program = &edge.ProgramSpec{
		Worker:              generic,
		StoreScriptName:     w.StoreScriptName,
		ISRWriterScriptName: w.ISRWriterScriptName,
	}
	return spec, nil
}

func MarkGlobalPreview(state edge.StackState, cfg Config, manifest *deploymentsv1.Manifest) edge.StackState {
	if cfg.Class != deploymentsv1.Environment_CLASS_PREVIEW {
		return state
	}
	if !servesOnGlobalPreviewDomain(cfg, manifest) {
		if len(state) == 0 {
			return state
		}
		marked := maps.Clone(state)
		delete(marked, edge.StackKeyGlobalPreview)
		return marked
	}
	marked := edge.StackState{}
	maps.Copy(marked, state)
	marked[edge.StackKeyGlobalPreview] = cfg.GlobalPreviewDomain
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
