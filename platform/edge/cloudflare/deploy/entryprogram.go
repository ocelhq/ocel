package cloudflare

import (
	"encoding/json"
	"fmt"

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

const (
	serveEntrypoint  = "Serve"
	purgeQueueObject = "PurgeQueue"
)

const originClientCertificateBinding = "OCEL_ORIGIN_CLIENT_CERTIFICATE"

type entryProgram struct {
	name            string
	worker          workerScript
	pruneWorkerStem string
	store           edge.ReleasesStore
}

func (p *cloudflare) buildEntryProgram(tier environment.Tier, slug string, program *edge.Program) (entryProgram, error) {
	if program == nil {
		return entryProgram{}, fmt.Errorf("the %s edge runs the entry worker, and %s was handed no program for it", Kind, describeProgramScope(slug))
	}
	if slug != "" && p.namespace == "" {
		return entryProgram{}, fmt.Errorf("an edge worker is named for the namespace that installed its bootstrap, and this edge was opened with none; a name without it reaches whatever another namespace deployed for %s", slug)
	}
	worker, err := newEntryWorker(program.Origin)
	if err != nil {
		return entryProgram{}, err
	}
	built := entryProgram{store: program.ReleasesStore}
	if slug == "" {
		if program.ReleasesStore.Name == "" {
			return entryProgram{}, refusal.Refuse(refusal.CodeNotReady,
				"no releases-store worker found for the preview bootstrap, and the shared preview entry reads every release record through it; re-run `%s` to provision it",
				provider.BootstrapCommand(environment.TierPreview))
		}
		worker = withService(worker, genericStoreBinding, program.ReleasesStore.Name)
		worker = withVar(worker, envPreview, "1")
		worker = withVar(worker, envPreviewGlobal, "1")
		worker = withVar(worker, envPreviewBaseDomain, program.PreviewBaseDomain)
		built.name = previewEntryScript
		built.worker, err = addPreviewKey(worker, slug, program)
		return built, err
	}
	if tier == environment.TierPreview {
		built.name = previewWorkerName(p.namespace, slug)
		built.pruneWorkerStem = previewWorkerStem(p.namespace, slug)
		built.worker, err = addPreviewKey(addPreviewVariables(worker, program.PreviewBaseDomain), slug, program)
		return built, err
	}
	built.name = rootWorkerName(p.namespace, slug, program.Env)
	built.worker = worker
	return built, nil
}

func newEntryWorker(origin edge.OriginReach) (workerScript, error) {
	if len(entryBundle) == 0 {
		return workerScript{}, fmt.Errorf("the %s edge was built with no entry bundle for its worker to run", Kind)
	}
	if err := origin.RefuseIncomplete(); err != nil {
		return workerScript{}, fmt.Errorf("the %s entry worker cannot reach its origin: %w", Kind, err)
	}
	reach, err := json.Marshal(origin)
	if err != nil {
		return workerScript{}, fmt.Errorf("encode how the %s entry worker reaches its origin: %w", Kind, err)
	}
	worker := workerScript{
		Main:              mainModule(entryBundle),
		Variables:         map[string]string{},
		Secrets:           map[string]string{edge.OriginReachVar: string(reach)},
		CachedEntrypoints: []string{serveEntrypoint},
		DurableObjects:    []string{purgeQueueObject},
	}
	if origin.Reach.Kind == edge.ReachClientCertificate {
		worker.ClientCertificates = map[string]string{originClientCertificateBinding: origin.Reach.Certificate}
	}
	return withPurgeBudget(worker, workersCachePurgeBudget)
}

func addPreviewVariables(worker workerScript, baseDomain string) workerScript {
	worker = withVar(worker, envPreview, "1")
	if baseDomain != "" {
		worker = withVar(worker, envPreviewBaseDomain, baseDomain)
	}
	return worker
}

func addPreviewKey(worker workerScript, slug string, program *edge.Program) (workerScript, error) {
	if program.PreviewKey != "" {
		return withSecret(worker, edge.PreviewKeyVar, string(program.PreviewKey)), nil
	}
	if worker.Variables[envPreviewBaseDomain] != "" {
		return workerScript{}, fmt.Errorf("the preview worker for %s serves hostnames under %s but was given no key to verify them with, so it would answer none; deploy again so ocel hands it the preview key", describeProgramScope(slug), program.PreviewBaseDomain)
	}
	return worker, nil
}

func describeProgramScope(slug string) string {
	if slug == "" {
		return "every project"
	}
	return slug
}
