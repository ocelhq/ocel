package deploy

import (
	"fmt"

	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

const (
	envPreviewGlobal = "OCEL_PREVIEW_GLOBAL"

	storeServiceBinding = "DEPLOYMENTS"
)

func declaresPreviewDomain(manifest *contractv1.Manifest) bool {
	return len(DeclaredHostnames(manifest, environmentv1.Tier_TIER_PREVIEW)) > 0
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
	if !w.Edge.Facts().RunsCode {
		return spec, nil
	}
	generic, err := sharedWorker(w.Edge, w.Worker)
	if err != nil {
		return edge.PreviewWildcardSpec{}, err
	}
	if w.StoreScriptName == "" {
		return edge.PreviewWildcardSpec{}, fmt.Errorf("no deployments-store worker found for the preview bootstrap; re-run `ocel bootstrap --preview` to provision it")
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

func MarkGlobalPreview(state edge.StackState, cfg Config, manifest *contractv1.Manifest) edge.StackState {
	if cfg.Tier != environmentv1.Tier_TIER_PREVIEW {
		return state
	}
	if !servesOnGlobalPreviewDomain(cfg, manifest) {
		state.GlobalPreview = ""
		return state
	}
	state.GlobalPreview = cfg.GlobalPreviewDomain
	return state
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
