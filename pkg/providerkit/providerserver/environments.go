package providerserver

import (
	"context"
	"fmt"

	connect "connectrpc.com/connect"

	"github.com/ocelhq/ocel/pkg/naming"
	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"
	progressv1 "github.com/ocelhq/ocel/pkg/proto/common/progress/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/providerkit/envvars"
	"github.com/ocelhq/ocel/pkg/providerkit/provider"
	"github.com/ocelhq/ocel/pkg/providerkit/records"
	"github.com/ocelhq/ocel/pkg/providerkit/refusal"
	"github.com/ocelhq/ocel/pkg/providerkit/stackrecords"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

func envName(env *environmentv1.Environment) (string, error) {
	class, err := classOf(env.GetTier())
	if err != nil {
		return "", err
	}
	if class == edge.ClassProduction {
		return stackrecords.ProductionEnv, nil
	}
	identity := env.GetIdentity()
	if identity == "" {
		return "", refusal.Refuse(refusal.CodeInvalid, "a preview environment is addressed by its identity, and this call names none")
	}
	if err := naming.Validate("preview name", identity); err != nil {
		return "", refusal.Refuse(refusal.CodeInvalid, "%s", err.Error())
	}
	if identity == stackrecords.ProductionEnv {
		return "", refusal.Refuse(refusal.CodeInvalid, "%q names production, so it is not a preview environment's identity", identity)
	}
	return identity, nil
}

func lifecycleOf(persisted bool) environmentv1.Lifecycle {
	if persisted {
		return environmentv1.Lifecycle_LIFECYCLE_PERSISTENT
	}
	return environmentv1.Lifecycle_LIFECYCLE_EPHEMERAL
}

func (h *handlers) ListEnvironments(ctx context.Context, req *contractv1.ListEnvironmentsRequest) (*contractv1.ListEnvironmentsResponse, error) {
	p, err := h.session.use()
	if err != nil {
		return nil, err
	}
	environments, err := stackrecords.PreviewEnvironments(ctx, p.Records(), req.GetSlug())
	if err != nil {
		return nil, provider.RefusalError(err)
	}
	resp := &contractv1.ListEnvironmentsResponse{Environments: make([]*contractv1.PreviewEnvironment, 0, len(environments))}
	for _, environment := range environments {
		resp.Environments = append(resp.Environments, &contractv1.PreviewEnvironment{
			Identity:  environment.Identity,
			Lifecycle: lifecycleOf(environment.Persisted),
			Label:     environment.Label,
			CreatedAt: environment.CreatedAt,
		})
	}
	return resp, nil
}

func (h *handlers) RemoveEnvironment(ctx context.Context, req *contractv1.RemoveEnvironmentRequest, stream *connect.ServerStream[progressv1.OperationEvent]) error {
	return streamed(ctx, stream, naming.UnitEnvironment, environmentUnitTitle, progressv1.Phase_PHASE_DELETING, func(_ *eventStream, progress edge.Progress) error {
		pointer, err := envName(req.GetEnvironment())
		if err != nil {
			return err
		}
		if pointer == stackrecords.ProductionEnv {
			return refusal.Refuse(refusal.CodeInvalid,
				"production is not an environment to remove; `ocel destroy production` removes the project's production footprint")
		}
		session, err := h.openEdgeSession(ctx, edge.ClassPreview, req.GetSlug(), req.GetEdge())
		if err != nil {
			return err
		}
		progress.Say(fmt.Sprintf("Removing preview pointer %q from the store", pointer))
		removed, err := session.stack.RemovePointer(ctx, pointer, progress)
		if err != nil {
			return err
		}
		if err := session.checkpoint(ctx); err != nil {
			return err
		}
		if err := ReclaimPreview(ctx, session.provider, req.GetSlug(), pointer, removed, progress); err != nil {
			return err
		}
		if err := removeOcelOwnedBindings(ctx, session.provider, req.GetSlug(), pointer); err != nil {
			return err
		}
		if err := records.Forget(ctx, session.provider.Records(), stackrecords.EnvironmentRecord(edge.ClassPreview, req.GetSlug(), pointer)); err != nil {
			return err
		}
		for _, line := range pruneLines(removed) {
			progress.Say(line)
		}
		return nil
	})
}

func removeOcelOwnedBindings(ctx context.Context, p provider.Provider, slug, environment string) error {
	store := envvars.Store{Records: p.Records(), Cipher: p.Cipher()}
	scope := envvars.Scope{Project: slug, Class: edge.ClassPreview}
	held, err := store.ListBindings(ctx, scope, environment)
	if err != nil {
		return fmt.Errorf("read the records kept for preview %s: %w", environment, err)
	}
	var kept []string
	for _, record := range held {
		if record.Environment != environment {
			continue
		}
		if record.Owner == envvars.OwnerOcel || record.Owner == naming.InlineRecordOwner {
			kept = append(kept, record.Name)
		}
	}
	if len(kept) == 0 {
		return nil
	}
	if _, err := store.RemoveBindings(ctx, scope, environment, kept); err != nil {
		return fmt.Errorf("remove the records kept for preview %s: %w", environment, err)
	}
	return nil
}
