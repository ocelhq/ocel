package providerserver

import (
	"context"
	"fmt"

	connect "connectrpc.com/connect"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/keyvalue"
	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/progress"
	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"
	progressv1 "github.com/ocelhq/ocel/pkg/proto/common/progress/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/pkg/stackrecords"
	"github.com/ocelhq/ocel/pkg/variablestore"
)

func envName(env *environmentv1.Environment) (string, error) {
	tier, err := decodeTier(env.GetTier())
	if err != nil {
		return "", err
	}
	if tier == environment.TierProduction {
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
	environments, err := stackrecords.PreviewEnvironments(ctx, p.KeyValues(), req.GetSlug())
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
	root := RootSpan(naming.SpanEnvironment, req.GetEnvironment().GetIdentity(),
		progress.Removing.Title("the preview environment of "+req.GetSlug()), progressv1.Phase_PHASE_DESTROY)
	return streamed(ctx, stream, root, func(_ *eventStream, progress progress.Log) error {
		pointer, err := envName(req.GetEnvironment())
		if err != nil {
			return err
		}
		if pointer == stackrecords.ProductionEnv {
			return refusal.Refuse(refusal.CodeInvalid,
				"production is not an environment to remove; `ocel destroy production` removes the project's production footprint")
		}
		session, err := h.openEdgeSession(ctx, environment.TierPreview, req.GetSlug(), req.GetEdge())
		if err != nil {
			return err
		}
		progress.Say(fmt.Sprintf("Removing the routing pointer of %s", environmentPhrase(environment.TierPreview, pointer)))
		removed, err := session.removePointer(ctx, pointer, progress)
		if err != nil {
			return err
		}
		if err := session.releasePointerHostnames(ctx, pointer, progress); err != nil {
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
		if err := keyvalue.Forget(ctx, session.provider.KeyValues(), stackrecords.EnvironmentKey(environment.TierPreview, req.GetSlug(), pointer)); err != nil {
			return err
		}
		for _, line := range pruneLines(removed) {
			progress.Say(line)
		}
		return nil
	})
}

func removeOcelOwnedBindings(ctx context.Context, p provider.Provider, slug, preview string) error {
	store := variablestore.Store{KeyValues: p.KeyValues(), Cipher: p.Cipher()}
	scope := variablestore.Scope{Project: slug, Tier: environment.TierPreview}
	published, err := store.ListBindings(ctx, scope, preview)
	if err != nil {
		return fmt.Errorf("read the records kept for preview %s: %w", preview, err)
	}
	var kept []string
	for _, record := range published {
		if record.Environment != preview {
			continue
		}
		if record.Owner == variablestore.OwnerOcel || record.Owner == naming.InlineRecordOwner {
			kept = append(kept, record.Name)
		}
	}
	if len(kept) == 0 {
		return nil
	}
	if _, err := store.RemoveBindings(ctx, scope, preview, kept); err != nil {
		return fmt.Errorf("remove the records kept for preview %s: %w", preview, err)
	}
	return nil
}
