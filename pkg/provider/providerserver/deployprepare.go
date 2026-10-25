package providerserver

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"strconv"
	"time"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/progress"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/records"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/pkg/router"
	"github.com/ocelhq/ocel/pkg/stackrecords"
)

func (r *deployRun) prepare(ctx context.Context, progress progress.Log) error {
	if _, err := r.readCurrentInfra(ctx); err != nil {
		return err
	}
	if err := r.refuseOtherLifecycle(ctx); err != nil {
		return err
	}
	if err := r.checkInlineBindings(ctx, progress); err != nil {
		return err
	}
	if err := r.refuseUnreadyBootstrap(ctx, progress); err != nil {
		return err
	}
	if err := r.resolveServingDomains(ctx); err != nil {
		return err
	}
	if err := r.readInlineRecords(ctx); err != nil {
		return err
	}
	if err := r.admitBindings(ctx, progress); err != nil {
		return err
	}
	appRouters, err := pairApps(r.provider.Facts(), r.front.Kind(), r.spec.Apps)
	if err != nil {
		return err
	}
	r.appRouters = appRouters
	if r.state.Apps == nil {
		r.state.Apps = map[string]router.Kind{}
	}
	maps.Copy(r.state.Apps, appRouters)
	if err := refuseContainersBuiltFromFunctions(r.provider.Facts(), r.spec.Apps); err != nil {
		return err
	}
	if err := r.checkNeeds(ctx); err != nil {
		return err
	}
	if err := r.refuseUnsupportedDeclarations(ctx); err != nil {
		return err
	}
	if err := r.refuseContainerValues(ctx); err != nil {
		return err
	}
	if err := r.refuseUnconsentedDeploy(ctx); err != nil {
		return err
	}
	if err := r.mode.previewHosts(ctx, r, progress); err != nil {
		return err
	}
	if err := r.enforceProtection(ctx, progress); err != nil {
		return err
	}
	if err := r.mode.recordProject(ctx, r); err != nil {
		return err
	}
	if err := r.preflight(ctx, progress); err != nil {
		return err
	}
	return r.mode.recordLifecycle(ctx, r)
}

func (r *deployRun) refuseOtherLifecycle(ctx context.Context) error {
	if r.spec.Tier != environment.TierPreview {
		return nil
	}
	meta, err := stackrecords.ReadEnvironmentMeta(ctx, r.provider.KeyValues(), r.spec.Tier, r.spec.Slug, r.spec.Env)
	if err != nil {
		return err
	}
	return meta.RefuseOtherLifecycle(r.spec.Env, r.lifecycle)
}

func (r *deployRun) ensureLifecycle(ctx context.Context) error {
	if r.spec.Tier != environment.TierPreview {
		return nil
	}
	return stackrecords.EnsureLifecycle(ctx, r.provider.KeyValues(), r.spec.Tier, r.spec.Slug, r.spec.Env, r.lifecycle, r.aliasToken)
}

func (r *deployRun) refuseUnreadyBootstrap(ctx context.Context, progress progress.Log) error {
	_, err := r.readiness.EnsureReady(ctx, r.spec.Tier, r.mode.wantedBy(r.spec.Slug), r.parts, progress)
	return err
}

func (r *deployRun) resolveServingDomains(ctx context.Context) error {
	hosts := r.hostnames()
	if r.spec.Tier == environment.TierPreview {
		wildcard, err := stackrecords.ReadWildcard(ctx, r.provider.KeyValues())
		if err != nil {
			return err
		}
		r.wildcard = wildcard
		if len(hosts) > 0 {
			base, err := parsePreviewBase(hosts)
			if err != nil {
				return err
			}
			r.previewOn = base
			return nil
		}
		if wildcard.BaseDomain != "" {
			r.previewOn = wildcard.BaseDomain
			return nil
		}
		if r.edgeRouter().Facts().AddressesItself {
			return nil
		}
		return refuseNoPreviewDomain()
	}
	addressesItself := r.edgeRouter().Facts().AddressesItself
	if !addressesItself {
		if err := r.refuseAppsWithoutHostname(); err != nil {
			return err
		}
	}
	if len(hosts) == 0 {
		if addressesItself {
			return nil
		}
		return refusal.Refuse(refusal.CodeNotReady,
			"no domains.production declared on the project or any app, so this deploy has nowhere to serve: declare one, and the deploy that reads it attaches it")
	}
	configured, err := r.configuredHosts()
	if err != nil {
		return err
	}
	r.configured = configured
	writer, err := dnsFor(r.provider, r.front, r.selection)
	if err != nil {
		return err
	}
	r.installDNSCutover(writer, r.selection.GetDns().GetZone())
	r.cutover.manual = failOnManualRecords(r.sender, r.spans.Hostnames)
	return nil
}

func (r *deployRun) refuseAppsWithoutHostname() error {
	var unserved []string
	for slot, hosts := range r.listServedHostnames() {
		entry := r.spec.Apps[slot]
		if len(hosts) == 0 && !provider.IsWorkerApp(entry.App, entry.Workers) {
			unserved = append(unserved, strconv.Quote(entry.App))
		}
	}
	if len(unserved) == 0 {
		return nil
	}
	named := namedList("app", "apps", unserved)
	var projectServes string
	if len(tierHostnames(r.manifest.GetDomains(), encodeTier(r.spec.Tier))) > 0 {
		projectServes = fmt.Sprintf(" (the project's domains.production serves app %q alone)", r.spec.Apps[0].App)
	}
	return refusal.Refuse(refusal.CodeNotReady,
		"this deploy serves %[1]s on no hostname, so nothing would route to %[1]s: "+
			"declare domains.production on %[1]s with a hostname no other app is served on%[2]s, "+
			"and the deploy that reads it attaches it",
		named, projectServes)
}

func refuseNoPreviewDomain() error {
	return refusal.Refuse(refusal.CodeNotReady,
		"this project declares no domains.preview wildcard and no global preview domain is in use, so a preview deploy has nowhere to serve: "+
			"declare a project-level domains.preview wildcard, or run `ocel domain use '*.preview.example.com' --preview` to serve every project's previews on one wildcard")
}

func (r *deployRun) readSignedPreviewHosts(ctx context.Context, progress progress.Log) error {
	if r.spec.Tier != environment.TierPreview {
		return nil
	}
	key, err := openPreviewKey(ctx, r.provider, r.previewKeys)
	if err != nil {
		return err
	}
	r.previewKey = key
	site, names := r.previewSite(), r.listNonEmptyNormalizedAppNames()
	if err := refuseOverlongAliases(site, r.spec.Env, names); err != nil {
		return err
	}
	return r.readPreviewHosts(ctx, progress, site, names)
}

func (r *deployRun) ensureSignedPreviewHosts(ctx context.Context) error {
	if r.spec.Tier != environment.TierPreview {
		return nil
	}
	key, err := ensurePreviewKey(ctx, r.provider, r.previewKeys)
	if err != nil {
		return err
	}
	r.previewKey = key
	site, names := r.previewSite(), r.listNonEmptyNormalizedAppNames()
	if err := refuseOverlongAliases(site, r.spec.Env, names); err != nil {
		return err
	}
	alias, err := r.ensureBuiltAlias(ctx)
	if err != nil {
		return err
	}
	deployment, err := edge.NewPreviewToken()
	if err != nil {
		return err
	}
	r.aliasToken = alias
	r.aliases, r.deployment = site.ListHosts(r.spec.Env, alias, names), site.ListHosts(r.spec.Env, deployment, names)
	return nil
}

func (r *deployRun) ensureBuiltAlias(ctx context.Context) (string, error) {
	if r.builtAlias != "" {
		return r.builtAlias, stackrecords.RecordBuiltAliasToken(ctx, r.provider.KeyValues(), environment.TierPreview, r.spec.Slug, r.spec.Env, r.builtAlias)
	}
	candidate, err := edge.NewPreviewToken()
	if err != nil {
		return "", err
	}
	return stackrecords.EnsureAliasToken(ctx, r.provider.KeyValues(), environment.TierPreview, r.spec.Slug, r.spec.Env, candidate)
}

func (r *deployRun) readPreviewHosts(ctx context.Context, progress progress.Log, site edge.PreviewSite, names []string) error {
	var alias string
	if r.previewKey != "" {
		var err error
		if alias, err = readAliasToken(ctx, r.provider, r.spec.Slug, r.spec.Env); err != nil {
			return err
		}
	}
	if alias == "" {
		progress.Say(fmt.Sprintf("Preview %s has never deployed, so its hostname is assigned on its first deploy", r.spec.Env))
		return nil
	}
	r.aliases = site.ListHosts(r.spec.Env, alias, names)
	return nil
}

func (r *deployRun) rememberProject(ctx context.Context) error {
	recorded, err := recordedEdge(r.front.Kind(), r.selection)
	if err != nil {
		return err
	}
	updatedAt := time.Now().Unix()
	var base []string
	read := false
	return records.Change(ctx, r.provider.KeyValues(), stackrecords.ProjectKey(r.spec.Tier, r.spec.Slug), func(entry records.Entry) ([]byte, bool, error) {
		current, err := stackrecords.DecodeProject(entry)
		if err != nil {
			return nil, false, err
		}
		if !read {
			base, read = current.Parts, true
		}
		parts := slices.Clone(r.parts)
		for _, part := range current.Parts {
			if !slices.Contains(base, part) && !slices.Contains(parts, part) {
				parts = append(parts, part)
			}
		}
		value, err := json.Marshal(stackrecords.Project{Parts: parts, Edge: recorded, Registry: recordedRegistry(r.projectRegistry), UpdatedAt: updatedAt})
		return value, true, err
	})
}

func (r *deployRun) checkNeeds(ctx context.Context) error {
	check := EdgeNeedCheck{
		Edge:          r.front,
		Router:        r.readPairedRouter,
		Root:          r.artifactRoot,
		AllowDegraded: r.allowDegraded,
		Degraded: func(app string, need edge.Need, detail string) {
			r.sender.send(degradedEvent(app, need, detail))
		},
		Warn: func(subject, message string) {
			r.sender.send(checkWarning(subject, message))
		},
	}
	verdicts, err := check.Run(ctx, r.manifest)
	if err != nil {
		return err
	}
	r.needs = verdicts
	return nil
}

func (r *deployRun) preflight(ctx context.Context, progress progress.Log) error {
	hooks := r.provider.Hooks().Preflight
	if hooks == nil {
		return nil
	}
	pre, err := r.deployPreflight(ctx, progress)
	if err != nil {
		return err
	}
	return r.mode.preflight(ctx, hooks, pre)
}

func (r *deployRun) planPreflight(ctx context.Context) (provider.Plan, error) {
	hooks := r.provider.Hooks().Preflight
	if hooks == nil {
		return provider.Plan{}, nil
	}
	pre, err := r.deployPreflight(ctx, progress.Discard())
	if err != nil {
		return provider.Plan{}, err
	}
	return hooks.Plan(ctx, pre)
}

func (r *deployRun) deployPreflight(ctx context.Context, progress progress.Log) (provider.DeployPreflight, error) {
	resources, err := manifestResources(r.manifest)
	if err != nil {
		return provider.DeployPreflight{}, err
	}
	grants, err := r.publishedBindings().Published(ctx)
	if err != nil {
		return provider.DeployPreflight{}, err
	}
	apps, err := r.usage(resources, grants)
	if err != nil {
		return provider.DeployPreflight{}, err
	}
	return provider.DeployPreflight{
		Deploy:            r.spec,
		PreviewBaseDomain: r.previewOn,
		Edge:              r.front.Kind(),
		Resources:         resources,
		Grants:            grants,
		Apps:              apps,
		Progress:          progress,
		WrittenBy:         r.readiness.WrittenBy,
		Uses:              r.uses,
	}, nil
}

func (r *deployRun) refuseUnsupportedDeclarations(ctx context.Context) error {
	if err := RefuseUnsupportedTopicsTasksAndWorkers(r.provider.Facts(), r.manifest); err != nil {
		return err
	}
	if err := RefuseUnsupportedKVStores(r.provider.Facts(), r.manifest); err != nil {
		return err
	}
	if err := RefuseUnsupportedRealtime(r.provider.Facts(), r.manifest); err != nil {
		return err
	}
	resources, err := manifestResources(r.manifest)
	if err != nil {
		return err
	}
	grants, err := r.publishedBindings().Published(ctx)
	if err != nil {
		return err
	}
	if err := RefuseUnservedProxiedBindings(r.provider.Facts().Vendor, r.provider.Facts().Bindings, proxied, resources, grants); err != nil {
		return refusal.Refuse(refusal.CodeInvalid, "%s", err)
	}
	if facts := r.provider.Facts(); !facts.RendersTransforms {
		return provider.RefuseTransforms(facts.Vendor, r.transforms)
	}
	return nil
}

func (r *deployRun) usage(resources []provider.Resource, published []provider.Binding) ([]provider.AppUsage, error) {
	apps := make([]provider.AppUsage, 0, len(r.spec.Apps))
	for _, entry := range r.spec.Apps {
		used, err := r.boundNamesUsedBy(entry.App)
		if err != nil {
			return nil, err
		}
		usage := provider.AppUsage{App: entry.App, Resources: usedResources(used, resources)}
		for _, binding := range published {
			if used[binding.Name] {
				usage.Grants = append(usage.Grants, binding)
			}
		}
		apps = append(apps, usage)
	}
	return apps, nil
}

func usedResources(used map[string]bool, resources []provider.Resource) []provider.Resource {
	var out []provider.Resource
	for _, resource := range resources {
		if used[boundName(resource)] {
			out = append(out, resource)
		}
	}
	return out
}

func boundName(resource provider.Resource) string {
	if resource.Binding != "" {
		return resource.Binding
	}
	return resource.Name
}
