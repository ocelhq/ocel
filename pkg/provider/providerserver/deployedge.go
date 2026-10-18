package providerserver

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/progress"
	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/refusal"
)

type hostingMode int

const (
	hostingProduction hostingMode = iota
	hostingGlobalPreview
	hostingProjectPreview
)

func (r *deployRun) hostingMode() hostingMode {
	if r.spec.Tier != environment.TierPreview {
		return hostingProduction
	}
	if r.wildcard.BaseDomain != "" && len(r.hostnames()) == 0 {
		return hostingGlobalPreview
	}
	return hostingProjectPreview
}

func (r *deployRun) reconcileEdge(ctx context.Context, progress progress.Log) error {
	spec := edge.StackSpec{
		Version:     stackVersion,
		Tier:        r.spec.Tier,
		Slug:        r.spec.Slug,
		PruneRoutes: true,
		Say:         progress.Say,
		Warn:        progress.Warn,
	}
	var base string
	switch r.hostingMode() {
	case hostingProduction:
		spec.Domains, spec.ServedElsewhere, spec.DomainApps = r.listEdgeRoutedDomains()
	case hostingGlobalPreview:
		spec.PruneOnly = true
		spec.GlobalPreview = r.globalPreview()
	default:
		if len(r.spec.Apps) == 0 {
			spec.PruneOnly = true
			break
		}
		base = r.previewOn
		spec.Domains = []string{edge.PreviewWildcard(base)}
	}
	program, err := edgeProgramFor(ctx, r.provider, r.front, edgeProgramScope{
		tier:              r.spec.Tier,
		env:               r.spec.Env,
		previewBaseDomain: base,
		previewKey:        r.previewKey,
	})
	if err != nil {
		return err
	}
	spec.Program = program
	disabled, err := r.disableGate(ctx)
	if err != nil {
		return err
	}
	written, password, err := r.readGateInForce(ctx)
	if err != nil {
		return err
	}
	spec.Gate = gateForEdge(r.front, written, password)
	stack, err := r.front.Reconcile(ctx, spec, r.state.Edge)
	if err != nil {
		if disabled {
			return errors.Join(err, r.restoreGate(ctx))
		}
		return err
	}
	r.setEdgeStack(stack)
	if err := r.settleGate(ctx, written, progress); err != nil {
		return err
	}
	if err := recordInvalidationTarget(ctx, r.provider.KeyValues(), r.spec.Tier, r.front.Kind(), r.spec.Slug, r.state.Edge.PurgeURL, stack.State().PurgeURL); err != nil {
		return err
	}
	if err := r.checkpoint(ctx); err != nil {
		return err
	}
	return r.forwardPreviews(ctx, progress)
}

func (r *deployRun) forwardPreviews(ctx context.Context, progress progress.Log) error {
	if routerOriginBehind(r.front, r.edgeRouter()) == nil || r.front.Facts().RunsCode {
		return nil
	}
	switch r.hostingMode() {
	case hostingGlobalPreview:
		shared := &wildcards{provider: r.provider, keyValues: r.provider.KeyValues(), recorded: r.wildcard, sel: r.selection}
		return shared.refreshEntryClaim(ctx, r.front, progress)
	case hostingProjectPreview:
		if r.previewOn == "" || len(r.spec.Apps) == 0 {
			return nil
		}
	default:
		return nil
	}
	target := ConfiguredHost{Hostname: edge.PreviewWildcard(r.previewOn)}
	forwarding := &hostnames{edgeSession: r.edgeSession}
	hostState := r.state.Host(target.Hostname)
	if slices.Contains(r.edgeStack().State().Bound, target.Hostname) {
		_, err := forwarding.refreshOriginClaim(ctx, target, &hostState, progress)
		return err
	}
	return forwarding.bindOrigin(ctx, target, &hostState, progress)
}

func (r *deployRun) attachHostnames(ctx context.Context) error {
	if r.hostingMode() != hostingProduction {
		return nil
	}
	return r.spanEvents.run(r.spans.Hostnames, func(u *spanRun) error {
		var attached, missed []string
		err := u.phase(func(progress progress.Log) error {
			attaching := &hostnames{edgeSession: r.edgeSession}
			skip := func(host, note string) {
				progress.Warn(note)
				r.pending = append(r.pending, note)
				missed = append(missed, host)
			}
			for _, host := range r.configured {
				if r.state.Ready(host.Hostname, r.front.Kind(), r.readConfiguredRouter(host.App)) {
					hostState := r.state.Host(host.Hostname)
					if err := attaching.releasePreviousRouter(ctx, host.Hostname, &hostState, r.readConfiguredRouter(host.App), progress); err != nil {
						return err
					}
					if _, err := attaching.refreshOriginClaim(ctx, host, &hostState, progress); err != nil {
						return err
					}
					attached = append(attached, host.Hostname)
					continue
				}
				if bound, served := r.state.Host(host.Hostname).ServedEdge(); served && bound != r.front.Kind() && !r.answersThroughFront(ctx, attaching, host) {
					skip(host.Hostname, fmt.Sprintf(
						"%s is still served through %s, not through %s this deploy promoted to: `ocel domain add` moves it, in the order that keeps it answering",
						host.Hostname, describeFront(bound), describeFront(r.front.Kind())))
					continue
				}
				_, err := attaching.attachHostname(ctx, host, progress)
				if waits, ok := provider.ResumableMessage(err); ok {
					skip(host.Hostname, fmt.Sprintf("%s is not served yet: %s", host.Hostname, waits))
					continue
				}
				if err != nil {
					return err
				}
				attached = append(attached, host.Hostname)
			}
			return nil
		})
		if err == nil && len(missed) > 0 {
			u.recordPartial(describeAttached(attached, missed))
		}
		return err
	})
}

func (r *deployRun) answersThroughFront(ctx context.Context, attaching *hostnames, host ConfiguredHost) bool {
	if !r.front.Facts().RunsCode || r.readConfiguredRouter(host.App) != r.edgeKind {
		return false
	}
	return attaching.probe(ctx, host.Hostname, r.edgeKind).OK
}

func describeAttached(attached, missed []string) string {
	if len(attached) == 0 {
		return "Did not attach " + namedList("production hostname", "production hostnames", missed)
	}
	return "Attached " + namedList("production hostname", "production hostnames", attached) + " but not " + joinNames(missed)
}

func (r *deployRun) configuredHosts() ([]ConfiguredHost, error) {
	owners := r.domainApps()
	hosts := r.hostnames()
	declared := make([]*contractv1.ConfiguredHostname, 0, len(hosts))
	for _, host := range hosts {
		declared = append(declared, &contractv1.ConfiguredHostname{Hostname: host, App: owners[strings.ToLower(host)]})
	}
	return productionHosts(declared)
}

func parsePreviewBase(hosts []string) (string, error) {
	var base string
	for _, host := range hosts {
		resolved, wildcard := strings.CutPrefix(host, "*.")
		if !wildcard {
			return "", refusal.Refuse(refusal.CodeInvalid,
				"this project declares the preview domain %q, which is not a `*.` wildcard: every preview is served on its own subdomain of it, so declare %q instead",
				host, edge.PreviewWildcard(host))
		}
		if base != "" && resolved != base {
			return "", refusal.Refuse(refusal.CodeInvalid,
				"this project declares more than one preview domain (%q and %q): a preview domain is claimed by the whole project, "+
					"which serves every app from one wildcard, so declare a single project-level domains.preview",
				edge.PreviewWildcard(base), host)
		}
		base = resolved
	}
	return base, nil
}

func (r *deployRun) globalPreview() string {
	if r.hostingMode() != hostingGlobalPreview {
		return ""
	}
	return r.wildcard.BaseDomain
}

func (r *deployRun) listNonEmptyNormalizedAppNames() []string {
	names := make([]string, 0, len(r.spec.Apps))
	for _, entry := range r.spec.Apps {
		names = append(names, entry.App)
	}
	return normalizeAppNames(names)
}

func (r *deployRun) domainApps() map[string]string {
	served := r.listServedHostnames()
	owners := make(map[string]string, len(served))
	for slot, hosts := range served {
		name := normalizeAppName(r.spec.Apps[slot].App)
		if name == "" {
			continue
		}
		for _, host := range hosts {
			owners[strings.ToLower(host)] = name
		}
	}
	if len(owners) == 0 {
		return nil
	}
	return owners
}

func (r *deployRun) hostnames() []string {
	tier := encodeTier(r.spec.Tier)
	seen := map[string]bool{}
	hosts := unseen(tierHostnames(r.manifest.GetDomains(), tier), seen)
	for _, app := range r.manifest.GetApps() {
		hosts = append(hosts, unseen(tierHostnames(app.GetDomains(), tier), seen)...)
	}
	return hosts
}

func tierHostnames(declared []*contractv1.TierDomains, tier environmentv1.Tier) []string {
	var hosts []string
	for _, domains := range declared {
		if domains.GetTier() != tier {
			continue
		}
		hosts = append(hosts, domains.GetHostnames()...)
	}
	return hosts
}

func (r *deployRun) previewSite() edge.PreviewSite {
	if r.hostingMode() == hostingGlobalPreview {
		return edge.NewSharedPreviewSite(r.spec.Slug, r.previewOn, r.previewKey)
	}
	return edge.NewProjectPreviewSite(r.previewOn, r.previewKey)
}

func findAppHost(hosts []edge.PreviewHost, app string) edge.PreviewHost {
	name := normalizeAppName(app)
	for _, host := range hosts {
		if host.App == name {
			return host
		}
	}
	return edge.PreviewHost{}
}

func (r *deployRun) listServedHostnames() [][]string {
	if r.spec.Tier == environment.TierPreview {
		served := make([][]string, len(r.spec.Apps))
		for slot, entry := range r.spec.Apps {
			if host := findAppHost(r.aliases, entry.App); host.Hostname != "" {
				served[slot] = []string{host.Hostname}
			}
		}
		return served
	}
	tier := encodeTier(r.spec.Tier)
	own := make([][]string, len(r.spec.Apps))
	for slot, entry := range r.spec.Apps {
		own[slot] = tierHostnames(entry.Manifest.GetDomains(), tier)
	}
	return edge.AttributeHostnames(tierHostnames(r.manifest.GetDomains(), tier), own)
}

func (r *deployRun) checkpoint(ctx context.Context) error {
	r.state.Kind = r.front.Kind()
	r.state.Edge = r.edgeStack().State()
	r.pairRouters()
	if r.spec.Tier == environment.TierPreview {
		r.state.Edge.GlobalPreview = r.globalPreview()
	}
	return r.store.write(ctx, r.state)
}

func unseen(hosts []string, seen map[string]bool) []string {
	var out []string
	for _, host := range hosts {
		if seen[host] {
			continue
		}
		seen[host] = true
		out = append(out, host)
	}
	return out
}
