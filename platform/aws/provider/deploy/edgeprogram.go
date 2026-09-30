package deploy

import (
	"fmt"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/refusal"
)

type EdgeProgram struct {
	Tier              environment.Tier
	Kind              edge.Kind
	Entry             edge.WorkerModule
	Namespace         string
	Slug              string
	Env               string
	PreviewBaseDomain string
	PreviewKey        edge.PreviewKey

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
	generic, err := sharedWorker(p.Kind, p.Entry, p.Worker)
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
				provider.BootstrapCommand(environment.TierPreview))
		}
		generic = withService(generic, storeServiceBinding, p.StoreScriptName)
		generic = withVar(generic, envPreview, "1")
		generic = withVar(generic, envPreviewGlobal, "1")
		generic = withVar(generic, envPreviewBaseDomain, p.PreviewBaseDomain)
		if spec.Worker, err = p.addPreviewKey(generic); err != nil {
			return provider.EdgeProgram{}, err
		}
		return provider.EdgeProgram{Spec: spec, Values: p.Values}, nil
	}
	if p.Tier == environment.TierPreview {
		spec.Name = previewWorkerName(p.Namespace, p.Slug)
		spec.PruneWorkerStem = previewWorkerStem(p.Namespace, p.Slug)
		if spec.Worker, err = p.addPreviewKey(addPreviewVariables(generic, p.PreviewBaseDomain)); err != nil {
			return provider.EdgeProgram{}, err
		}
		return provider.EdgeProgram{Spec: spec, Values: p.Values}, nil
	}
	spec.Name = rootWorkerName(p.Namespace, p.Slug, p.Env)
	spec.Worker = generic
	return provider.EdgeProgram{Spec: spec, Values: p.Values}, nil
}

func (p EdgeProgram) addPreviewKey(worker edge.Worker) (edge.Worker, error) {
	if p.PreviewKey != "" {
		return addSecret(worker, edge.PreviewKeyVar, string(p.PreviewKey)), nil
	}
	if worker.Variables[envPreviewBaseDomain] != "" {
		return edge.Worker{}, fmt.Errorf("the preview worker for %s serves hostnames under %s but was given no key to verify them with, so it would answer none; deploy again so ocel hands it the preview key", p.describePreviewScope(), p.PreviewBaseDomain)
	}
	return worker, nil
}

func (p EdgeProgram) describePreviewScope() string {
	if p.Slug == "" {
		return "every project"
	}
	return p.Slug
}
