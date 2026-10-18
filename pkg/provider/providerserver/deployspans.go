package providerserver

import (
	"fmt"
	"strings"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/progress"
	progressv1 "github.com/ocelhq/ocel/pkg/proto/common/progress/v1"
	"github.com/ocelhq/ocel/pkg/provider"
)

type deploySpans struct {
	Environment Span
	Infra       Span
	Apps        map[string]Span
	Edge        Span
	Hostnames   Span
	Promotion   Span
}

func (r *deployRun) newSpans() deploySpans {
	env, kind := environmentSubject(r.spec.Tier, r.spec.Env), string(r.front.Kind())
	where := environmentPhrase(r.spec.Tier, r.spec.Env)
	verbs := r.mode.verbs()
	routes, infra, app, onto := verbs.routes, verbs.infra, verbs.app, verbs.onto
	s := deploySpans{
		Environment: RootSpan(naming.SpanEnvironment, env,
			progress.Checking.Title("the bootstrap, domains and bindings for "+r.spec.Slug), progressv1.Phase_PHASE_PROVISION),
		Infra: RootSpan(r.spec.Infra.String(), env, infra.Title(r.describeInfra()), progressv1.Phase_PHASE_PROVISION),
		Edge:  RootSpan(naming.SpanEdge, kind, routes.Title(fmt.Sprintf("the routes for %s in %s", r.spec.Slug, where)), progressv1.Phase_PHASE_PROVISION),
		Hostnames: RootSpan(naming.SpanHostnames, kind,
			progress.Attaching.Title(namedList("production hostname", "production hostnames", r.hostnames())), progressv1.Phase_PHASE_PROVISION),
		Promotion: RootSpan(naming.SpanPromotion, env,
			progress.Switching.Title("traffic to promotion "+r.spec.PromotionID), progressv1.Phase_PHASE_PROMOTE),
		Apps: make(map[string]Span, len(r.spec.Apps)),
	}
	for _, entry := range r.spec.Apps {
		s.Apps[entry.App] = RootSpan(entry.Stack.String(), entry.App,
			app.Title(fmt.Sprintf("the %s %s %s", appNoun(entry), onto, where)), progressv1.Phase_PHASE_DEPLOY)
	}
	return s
}

func (r *deployRun) describeInfra() string {
	names := r.manifestResourceNames()
	if len(names) == 0 {
		return "stack " + r.spec.Infra.String()
	}
	return namedList("shared resource", "shared resources", names)
}

func (r *deployRun) manifestResourceNames() []string {
	names := make([]string, 0, len(r.manifest.GetResources()))
	for _, resource := range r.manifest.GetResources() {
		names = append(names, resourceName(resource))
	}
	return names
}

func appNoun(entry provider.AppEntry) string {
	if entry.Compute() == "" {
		return "app"
	}
	return string(entry.Compute()) + " app"
}

func environmentSubject(tier environment.Tier, env string) string {
	if tier == environment.TierPreview {
		return env
	}
	return string(environment.TierProduction)
}

func environmentPhrase(tier environment.Tier, env string) string {
	if tier == environment.TierPreview {
		return "preview " + env
	}
	return string(environment.TierProduction)
}

func namedList(one, many string, names []string) string {
	switch len(names) {
	case 0:
		return "no " + many
	case 1:
		return one + " " + names[0]
	default:
		return many + " " + joinNames(names)
	}
}

func joinNames(names []string) string {
	const shown = 3
	switch n := len(names); {
	case n <= 1:
		return strings.Join(names, "")
	case n <= shown+1:
		return strings.Join(names[:n-1], ", ") + " and " + names[n-1]
	default:
		return fmt.Sprintf("%s and %d more", strings.Join(names[:shown], ", "), n-shown)
	}
}
