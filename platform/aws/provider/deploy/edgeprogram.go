package deploy

import (
	"fmt"

	"github.com/ocelhq/ocel/pkg/providerkit/provider"
	"github.com/ocelhq/ocel/pkg/providerkit/refusal"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

type EdgeProgram struct {
	Class             edge.Class
	Kind              edge.Kind
	Namespace         string
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

func (p EdgeProgram) Build() (provider.EdgeProgram, error) {
	if p.Slug != "" && p.Namespace == "" {
		return provider.EdgeProgram{}, fmt.Errorf("an edge worker is named for the namespace that installed its bootstrap, and this program names none; a name without it reaches whatever another namespace deployed for %s", p.Slug)
	}
	generic, err := sharedWorker(p.Kind, p.Worker)
	if err != nil {
		return provider.EdgeProgram{}, err
	}
	spec := &edge.ProgramSpec{
		StoreScriptName:     p.StoreScriptName,
		StoreEndpoint:       p.StoreEndpoint,
		BootstrapCred:       p.StoreBootstrapCred,
		ISRWriterScriptName: p.ISRWriterScriptName,
	}
	if p.Slug == "" {
		if p.StoreScriptName == "" {
			return provider.EdgeProgram{}, refusal.Refuse(refusal.CodeNotReady,
				"no deployments-store worker found for the preview bootstrap, and the shared preview entry reads every deployment through it; re-run `%s` to provision it",
				provider.BootstrapCommand(edge.ClassPreview))
		}
		generic = withService(generic, storeServiceBinding, p.StoreScriptName)
		generic = withVar(generic, envPreview, "1")
		generic = withVar(generic, envPreviewGlobal, "1")
		generic = withVar(generic, envPreviewBaseDomain, p.PreviewBaseDomain)
		spec.Worker = generic
		return provider.EdgeProgram{Spec: spec, Values: p.Values}, nil
	}
	if p.Class == edge.ClassPreview {
		spec.Name = previewWorkerName(p.Namespace, p.Slug)
		spec.PruneWorkerStem = previewWorkerStem(p.Namespace, p.Slug)
		spec.Worker = withPreviewVars(generic, p.PreviewBaseDomain, p.Apps)
		return provider.EdgeProgram{Spec: spec, Values: p.Values}, nil
	}
	spec.Name = rootWorkerName(p.Namespace, p.Slug, p.Env)
	spec.Worker = generic
	return provider.EdgeProgram{Spec: spec, Values: p.Values}, nil
}
