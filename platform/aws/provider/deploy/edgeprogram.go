package deploy

import (
	"github.com/ocelhq/ocel/pkg/providerkit"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

type EdgeProgram struct {
	Class             providerkit.Class
	Kind              edge.Kind
	Slug              string
	Env               string
	PreviewBaseDomain string
	Apps              []string

	Worker WorkerFacts
	Values map[string]string

	StoreScriptName     string
	StoreEndpoint       string
	StoreBootstrapCred  string
	ISRWriterScriptName string
}

func (p EdgeProgram) Build() (providerkit.EdgeProgram, error) {
	generic, err := sharedWorker(p.Kind, p.Worker)
	if err != nil {
		return providerkit.EdgeProgram{}, err
	}
	spec := &edge.ProgramSpec{
		StoreScriptName:     p.StoreScriptName,
		StoreEndpoint:       p.StoreEndpoint,
		BootstrapCred:       p.StoreBootstrapCred,
		ISRWriterScriptName: p.ISRWriterScriptName,
	}
	if p.Slug == "" {
		if p.StoreScriptName == "" {
			return providerkit.EdgeProgram{}, providerkit.Refuse(providerkit.CodeNotReady,
				"no deployments-store worker found for the preview bootstrap, and the shared preview entry reads every deployment through it; re-run `%s` to provision it",
				providerkit.BootstrapCommand(providerkit.ClassPreview))
		}
		generic = withService(generic, storeServiceBinding, p.StoreScriptName)
		generic = withVar(generic, envPreview, "1")
		generic = withVar(generic, envPreviewGlobal, "1")
		generic = withVar(generic, envPreviewBaseDomain, p.PreviewBaseDomain)
		spec.Worker = generic
		return providerkit.EdgeProgram{Spec: spec, Values: p.Values}, nil
	}
	if p.Class == providerkit.ClassPreview {
		spec.Name = previewWorkerName(p.Slug)
		spec.PruneWorkerStem = previewWorkerStem(p.Slug)
		spec.Worker = withPreviewVars(generic, p.PreviewBaseDomain, p.Apps)
		return providerkit.EdgeProgram{Spec: spec, Values: p.Values}, nil
	}
	spec.Name = rootWorkerName(p.Slug, p.Env)
	spec.Worker = generic
	return providerkit.EdgeProgram{Spec: spec, Values: p.Values}, nil
}
