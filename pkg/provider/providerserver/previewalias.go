package providerserver

import (
	"context"
	"strings"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/environment"
	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/pkg/stackrecords"
)

func (h *handlers) EnsurePreviewAlias(ctx context.Context, req *contractv1.EnsurePreviewAliasRequest) (*contractv1.EnsurePreviewAliasResponse, error) {
	p, err := h.session.use()
	if err != nil {
		return nil, err
	}
	if tier := req.GetEnvironment().GetTier(); tier != environmentv1.Tier_TIER_PREVIEW {
		return nil, provider.RefusalError(refusal.Refuse(refusal.CodeInvalid,
			"only a preview is served on an alias hostname, and this call names the %s tier", tier))
	}
	env, err := envName(req.GetEnvironment())
	if err != nil {
		return nil, provider.RefusalError(err)
	}
	key, err := openOrEnsurePreviewKey(ctx, p, &h.session.previewKeys, req.GetDry())
	if err != nil {
		return nil, provider.RefusalError(err)
	}
	site, err := readPreviewSite(ctx, p, req.GetSlug(), req.GetDomains(), key)
	if err != nil {
		return nil, provider.RefusalError(err)
	}
	apps := normalizeAppNames(req.GetApps())
	if err := refuseOverlongAliases(site, env, apps); err != nil {
		return nil, provider.RefusalError(err)
	}
	var token string
	switch {
	case !req.GetDry():
		token, err = stackrecords.EnsureAliasToken(ctx, p.KeyValues(), environment.TierPreview, req.GetSlug(), env, req.GetToken())
	case key != "":
		token, err = readAliasToken(ctx, p, req.GetSlug(), env)
	}
	if err != nil {
		return nil, provider.RefusalError(err)
	}
	hosts := site.ListHosts(env, token, apps)
	if token == "" {
		return &contractv1.EnsurePreviewAliasResponse{}, nil
	}
	resp := &contractv1.EnsurePreviewAliasResponse{Hostnames: make(map[string]string, len(hosts)), Token: token}
	for _, host := range hosts {
		resp.Hostnames[host.App] = host.Hostname
	}
	return resp, nil
}

func (h *handlers) ForgetPreviewAlias(ctx context.Context, req *contractv1.ForgetPreviewAliasRequest) (*contractv1.ForgetPreviewAliasResponse, error) {
	p, err := h.session.use()
	if err != nil {
		return nil, err
	}
	env, err := envName(req.GetEnvironment())
	if err != nil {
		return nil, provider.RefusalError(err)
	}
	if err := stackrecords.ForgetUnclaimedAlias(ctx, p.KeyValues(), environment.TierPreview, req.GetSlug(), env, req.GetToken()); err != nil {
		return nil, err
	}
	return &contractv1.ForgetPreviewAliasResponse{}, nil
}

var unassignedAliasToken = strings.Repeat("a", edge.PreviewTokenLen)

func refuseOverlongAliases(site edge.PreviewSite, env string, apps []string) error {
	if err := site.RefuseOverlongLabels(site.ListHosts(env, unassignedAliasToken, apps)); err != nil {
		return refusal.Refuse(refusal.CodeInvalid, "%s", err)
	}
	return nil
}

func openOrEnsurePreviewKey(ctx context.Context, p provider.Provider, opened *openedPreviewKeys, dry bool) (edge.PreviewKey, error) {
	if dry {
		return openPreviewKey(ctx, p, opened)
	}
	return ensurePreviewKey(ctx, p, opened)
}

func readAliasToken(ctx context.Context, p provider.Provider, slug, env string) (string, error) {
	meta, err := stackrecords.ReadEnvironmentMeta(ctx, p.KeyValues(), environment.TierPreview, slug, env)
	return meta.AliasToken, err
}

func readPreviewSite(ctx context.Context, p provider.Provider, slug string, declared []string, key edge.PreviewKey) (edge.PreviewSite, error) {
	if len(declared) > 0 {
		base, err := parsePreviewBase(declared)
		if err != nil {
			return edge.PreviewSite{}, err
		}
		return edge.NewProjectPreviewSite(base, key), nil
	}
	wildcard, err := stackrecords.ReadWildcard(ctx, p.KeyValues())
	if err != nil {
		return edge.PreviewSite{}, err
	}
	if wildcard.BaseDomain == "" {
		return edge.PreviewSite{}, refuseNoPreviewDomain()
	}
	return edge.NewSharedPreviewSite(slug, wildcard.BaseDomain, key), nil
}

func normalizeAppName(app string) string {
	return strings.ToLower(strings.TrimSpace(app))
}

func normalizeAppNames(apps []string) []string {
	names := make([]string, 0, len(apps))
	for _, app := range apps {
		if name := normalizeAppName(app); name != "" {
			names = append(names, name)
		}
	}
	return names
}
