package cloudflare

import (
	"fmt"
	"maps"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/refusal"
)

const (
	envPreview           = "OCEL_PREVIEW"
	envPreviewGlobal     = "OCEL_PREVIEW_GLOBAL"
	envPreviewBaseDomain = "OCEL_PREVIEW_BASE_DOMAIN"
)

type OriginBindings struct {
	Variables map[string]string
	Secrets   map[string]string

	ClientCertificate string

	RefreshesThroughQueue bool
}

type EntryProgram struct {
	Tier              environment.Tier
	Entry             edge.WorkerModule
	Namespace         string
	Slug              string
	Env               string
	PreviewBaseDomain string
	PreviewKey        edge.PreviewKey
	Origin            OriginBindings
	Values            map[string]string

	StoreScriptName          string
	StoreEndpoint            string
	StoreBootstrapCredential string
	ISRWriterScriptName      string
}

func (p EntryProgram) Build() (provider.EdgeProgram, error) {
	if p.Slug != "" && p.Namespace == "" {
		return provider.EdgeProgram{}, fmt.Errorf("an edge worker is named for the namespace that installed its bootstrap, and this program names none; a name without it reaches whatever another namespace deployed for %s", p.Slug)
	}
	worker, err := newEntryWorker(p.Entry, p.Origin)
	if err != nil {
		return provider.EdgeProgram{}, err
	}
	if p.Origin.RefreshesThroughQueue {
		queue, err := refreshQueueNameFor(p.Namespace, p.Tier)
		if err != nil {
			return provider.EdgeProgram{}, err
		}
		worker.Queues = map[string]string{refreshQueueBinding: queue}
	}
	spec := &edge.ProgramSpec{
		StoreScriptName:     p.StoreScriptName,
		StoreEndpoint:       p.StoreEndpoint,
		BootstrapCredential: p.StoreBootstrapCredential,
		ISRWriterScriptName: p.ISRWriterScriptName,
	}
	if p.Slug == "" {
		if p.StoreScriptName == "" {
			return provider.EdgeProgram{}, refusal.Refuse(refusal.CodeNotReady,
				"no releases-store worker found for the preview bootstrap, and the shared preview entry reads every deployment through it; re-run `%s` to provision it",
				provider.BootstrapCommand(environment.TierPreview))
		}
		worker = withService(worker, genericStoreBinding, p.StoreScriptName)
		worker = withVar(worker, envPreview, "1")
		worker = withVar(worker, envPreviewGlobal, "1")
		worker = withVar(worker, envPreviewBaseDomain, p.PreviewBaseDomain)
		if spec.Worker, err = p.addPreviewKey(worker); err != nil {
			return provider.EdgeProgram{}, err
		}
		return provider.EdgeProgram{Spec: spec, Values: p.Values}, nil
	}
	if p.Tier == environment.TierPreview {
		spec.Name = previewWorkerName(p.Namespace, p.Slug)
		spec.PruneWorkerStem = previewWorkerStem(p.Namespace, p.Slug)
		if spec.Worker, err = p.addPreviewKey(addPreviewVariables(worker, p.PreviewBaseDomain)); err != nil {
			return provider.EdgeProgram{}, err
		}
		return provider.EdgeProgram{Spec: spec, Values: p.Values}, nil
	}
	spec.Name = rootWorkerName(p.Namespace, p.Slug, p.Env)
	spec.Worker = worker
	return provider.EdgeProgram{Spec: spec, Values: p.Values}, nil
}

func newEntryWorker(entry edge.WorkerModule, origin OriginBindings) (edge.Worker, error) {
	if len(entry.Content) == 0 {
		return edge.Worker{}, fmt.Errorf("the %s edge names no entry module for its worker to run", Kind)
	}
	variables := map[string]string{}
	maps.Copy(variables, origin.Variables)
	worker := edge.Worker{Main: entry, Variables: variables}
	if len(origin.Secrets) > 0 {
		worker.Secrets = maps.Clone(origin.Secrets)
	}
	if origin.ClientCertificate != "" {
		if origin.Variables[edge.EdgeAccessKeyIDVar] != "" || origin.Secrets[edge.EdgeSecretKeyVar] != "" {
			return edge.Worker{}, fmt.Errorf("the origin hands the %s edge both AWS signing keys and a client certificate; an origin is reached one way, so hand it one", Kind)
		}
		worker.ClientCertificates = map[string]string{edge.OriginClientCertificateBinding: origin.ClientCertificate}
	}
	return worker, nil
}

func addPreviewVariables(worker edge.Worker, baseDomain string) edge.Worker {
	worker = withVar(worker, envPreview, "1")
	if baseDomain != "" {
		worker = withVar(worker, envPreviewBaseDomain, baseDomain)
	}
	return worker
}

func (p EntryProgram) addPreviewKey(worker edge.Worker) (edge.Worker, error) {
	if p.PreviewKey != "" {
		return withSecret(worker, edge.PreviewKeyVar, string(p.PreviewKey)), nil
	}
	if worker.Variables[envPreviewBaseDomain] != "" {
		return edge.Worker{}, fmt.Errorf("the preview worker for %s serves hostnames under %s but was given no key to verify them with, so it would answer none; deploy again so ocel hands it the preview key", p.describePreviewScope(), p.PreviewBaseDomain)
	}
	return worker, nil
}

func (p EntryProgram) describePreviewScope() string {
	if p.Slug == "" {
		return "every project"
	}
	return p.Slug
}
