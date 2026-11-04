package providerhost

import (
	"context"
	"fmt"
	"maps"
	"net/http"
	"regexp"

	"connectrpc.com/connect"

	"github.com/ocelhq/ocel/pkg/endpoint"
	"github.com/ocelhq/ocel/pkg/environment"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/proto/provider/contract/v1/contractv1connect"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/providerserver"
	"github.com/ocelhq/ocel/pkg/refusal"
)

var slugPattern = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)

func sessionlessKey(tenant, slug string, tier environment.Tier) string {
	return "sessionless|" + tenant + "|" + slug + "|" + string(tier)
}

func (h *Host) enterSessionless(r *http.Request, caller endpoint.Caller, procedure string) (*session, error) {
	if procedure == contractv1connect.ProviderServiceConfigureProcedure {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("a call with no %s configures nothing: name the project with %s and %s", HeaderSession, HeaderSlug, HeaderTier))
	}
	slug, tier, err := projectOf(r.Header)
	if err != nil {
		return nil, err
	}
	tenant := caller.Tenant()
	id, served := "base", environment.Tier("")
	if slug != "" {
		id, served = slug+"."+string(tier), tier
	}
	key := sessionlessKey(tenant, slug, tier)
	enter := func() (*session, error) {
		return h.enter(r.Context(), key, func() (*session, error) {
			if err := h.makeRoom(); err != nil {
				return nil, err
			}
			return h.open(caller, key, id, tenant, false, slug, served)
		}, func(ctx context.Context, s *session) error {
			return h.configureFromDeploy(ctx, caller, s, slug, tier)
		})
	}
	s, err := enter()
	if err != nil || slug == "" {
		return s, err
	}
	revision, found, err := providerserver.ReadDeployedRevision(r.Context(), s.records, slug, tier)
	if err != nil {
		h.leave(s)
		return nil, connect.NewError(connect.CodeUnavailable, fmt.Errorf("read the settings %s last deployed with: %w", slug, err))
	}
	if found && revision == s.deployedRevision {
		return s, nil
	}
	h.leave(s)
	h.endCurrent(s)
	return enter()
}

func projectOf(header http.Header) (string, environment.Tier, error) {
	slug, tier := header.Get(HeaderSlug), environment.Tier(header.Get(HeaderTier))
	switch {
	case slug == "" && tier == "":
		return "", environment.TierProduction, nil
	case slug == "" || !slugPattern.MatchString(slug) || len(slug) > 63:
		return "", "", connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("%s names no project slug", HeaderSlug))
	case tier != environment.TierProduction && tier != environment.TierPreview:
		return "", "", connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("%s names neither %s nor %s", HeaderTier, environment.TierProduction, environment.TierPreview))
	}
	return slug, tier, nil
}

func (h *Host) configureFromDeploy(ctx context.Context, caller endpoint.Caller, s *session, slug string, tier environment.Tier) error {
	base, err := h.config.Base(ctx, caller)
	if err != nil {
		return provider.RefusalError(err)
	}
	target, err := h.config.New(ctx, caller, s.id, base)
	if err != nil {
		return provider.RefusalError(err)
	}
	s.tier, s.records, s.cipher = tier, target.KeyValues(), target.Cipher()
	settings := base
	var edge *contractv1.EdgeSelection
	if slug != "" {
		recorded, found, err := providerserver.ReadDeployedSettings(ctx, target.KeyValues(), slug, tier)
		if err != nil {
			return connect.NewError(connect.CodeUnavailable, fmt.Errorf("read the settings %s last deployed with: %w", slug, err))
		}
		if !found {
			return provider.RefusalError(refusal.Refuse(refusal.CodeNeedsRunner,
				"%s has no %s deploy recorded here, so this runs as a job on a runner", slug, tier))
		}
		deployed := recorded.Settings
		s.deployedRevision = recorded.Revision
		settings.Options = maps.Clone(base.Options)
		if settings.Options == nil {
			settings.Options = provider.Options{}
		}
		maps.Copy(settings.Options, deployed.GetOptions().AsMap())
		settings.Transforms = deployed.GetTransforms()
		settings.Slug = slug
		edge = deployed.GetEdge()
	}
	if _, err := h.configureInDirectory(ctx, s, settings, edge); err != nil {
		return err
	}
	h.mu.Lock()
	s.configured = true
	h.mu.Unlock()
	return nil
}
