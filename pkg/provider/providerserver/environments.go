package providerserver

import (
	"context"
	"fmt"

	connect "connectrpc.com/connect"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/keyvalue"
	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/progress"
	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"
	progressv1 "github.com/ocelhq/ocel/pkg/proto/common/progress/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/pkg/router"
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

func encodeLifecycle(lifecycle stackrecords.Lifecycle) environmentv1.Lifecycle {
	switch lifecycle {
	case stackrecords.LifecycleEphemeral:
		return environmentv1.Lifecycle_LIFECYCLE_EPHEMERAL
	case stackrecords.LifecyclePersistent:
		return environmentv1.Lifecycle_LIFECYCLE_PERSISTENT
	default:
		return environmentv1.Lifecycle_LIFECYCLE_UNSPECIFIED
	}
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
		resp.Environments = append(resp.Environments, encodePreviewEnvironment(environment))
	}
	return resp, nil
}

func encodePreviewEnvironment(environment stackrecords.Environment) *contractv1.PreviewEnvironment {
	return &contractv1.PreviewEnvironment{
		Identity:  environment.Identity,
		Lifecycle: encodeLifecycle(environment.Lifecycle),
		Label:     environment.Label,
		CreatedAt: environment.CreatedAt,
		AliasUrls: formatHostURLs(edge.ListPreviewHostnames(environment.Aliases)),
	}
}

func formatHostURLs(hostnames []string) []string {
	urls := make([]string, 0, len(hostnames))
	for _, hostname := range hostnames {
		urls = append(urls, "https://"+hostname)
	}
	return urls
}

func readPreviewRemoval(ctx context.Context, store keyvalue.Store, slug, pointer string) (router.PointerRemoval, error) {
	meta, err := stackrecords.ReadEnvironmentMeta(ctx, store, environment.TierPreview, slug, pointer)
	if err != nil {
		return router.PointerRemoval{}, err
	}
	return router.PointerRemoval{Pointer: pointer, Hosts: meta.ListPublishedAliases()}, nil
}

func (h *handlers) GetEnvironment(ctx context.Context, req *contractv1.GetEnvironmentRequest) (*contractv1.GetEnvironmentResponse, error) {
	p, err := h.session.use()
	if err != nil {
		return nil, err
	}
	if tier := req.GetEnvironment().GetTier(); tier != environmentv1.Tier_TIER_PREVIEW {
		return nil, provider.RefusalError(refusal.Refuse(refusal.CodeInvalid,
			"only a preview environment is recorded with a lifecycle to read, and this call names the %s tier", tier))
	}
	identity, err := envName(req.GetEnvironment())
	if err != nil {
		return nil, provider.RefusalError(err)
	}
	meta, err := stackrecords.ReadEnvironmentMeta(ctx, p.KeyValues(), environment.TierPreview, req.GetSlug(), identity)
	if err != nil {
		return nil, provider.RefusalError(err)
	}
	if meta.Lifecycle == "" {
		return &contractv1.GetEnvironmentResponse{}, nil
	}
	return &contractv1.GetEnvironmentResponse{Environment: encodePreviewEnvironment(stackrecords.Environment{
		Identity:  identity,
		Lifecycle: meta.Lifecycle,
		Label:     meta.Label,
		CreatedAt: meta.CreatedAt,
		Aliases:   meta.Aliases,
	})}, nil
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
		if err := refuseUnconfirmedLifecycle(ctx, session.provider.KeyValues(), req.GetSlug(), pointer, req.GetEnvironment().GetLifecycle()); err != nil {
			return err
		}
		removal, err := readPreviewRemoval(ctx, session.provider.KeyValues(), req.GetSlug(), pointer)
		if err != nil {
			return err
		}
		progress.Say(fmt.Sprintf("Removing the routing pointer of %s", environmentPhrase(environment.TierPreview, pointer)))
		removed, err := session.removePointer(ctx, removal, progress)
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

func refuseUnconfirmedLifecycle(ctx context.Context, store keyvalue.Store, slug, preview string, confirmed environmentv1.Lifecycle) error {
	meta, err := stackrecords.ReadEnvironmentMeta(ctx, store, environment.TierPreview, slug, preview)
	if err != nil {
		return err
	}
	if recorded := encodeLifecycle(meta.Lifecycle); recorded != confirmed {
		return refusal.Refuse(refusal.CodeBusy,
			"preview %s is %s, but this removal was confirmed while it was %s, so nothing was torn down: "+
				"run `ocel preview rm %s` again to confirm what it is now",
			preview, describeLifecycle(recorded), describeLifecycle(confirmed), preview)
	}
	return nil
}

func describeLifecycle(lifecycle environmentv1.Lifecycle) string {
	switch lifecycle {
	case environmentv1.Lifecycle_LIFECYCLE_EPHEMERAL:
		return "ephemeral"
	case environmentv1.Lifecycle_LIFECYCLE_PERSISTENT:
		return "persistent"
	default:
		return "not deployed"
	}
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
