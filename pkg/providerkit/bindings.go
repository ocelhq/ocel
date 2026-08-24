package providerkit

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/ocelhq/ocel/pkg/providerkit/values"
)

func (r *deployRun) admitLinks(ctx context.Context) error {
	resources, err := manifestResources(r.manifest)
	if err != nil {
		return err
	}
	links, err := r.reader().Published(ctx)
	if err != nil {
		return err
	}
	published := make(map[string]Link, len(links))
	names := make([]string, 0, len(links))
	for _, link := range links {
		published[link.Name] = link
		names = append(names, link.Name)
	}
	r.warnShadowed(resources, names)

	var missing []string
	for _, resource := range resources {
		if !resource.Linked {
			continue
		}
		if _, bound := published[resource.Declared]; !bound {
			missing = append(missing, resource.Declared)
		}
	}
	if len(missing) > 0 {
		return r.refuseUnpublished(ctx, missing, names)
	}
	for _, resource := range resources {
		if !resource.Linked {
			continue
		}
		if err := ReadableAs(published[resource.Declared], resource.Type); err != nil {
			return err
		}
	}
	return nil
}

func ReadableAs(link Link, declared LinkType) error {
	switch {
	case link.Type == LinkCustom:
		return Refuse(CodeInvalid,
			"`links` binds %q, and the record published under that name is a custom one: "+
				"a custom link is read by transforms; it is external by definition and never provisioned, so it is not bound here. "+
				"Drop %q from `links` and read it from a transform as `links.%s.<property>`",
			link.Name, link.Name, link.Name)
	case link.Type != declared:
		return Refuse(CodeInvalid,
			"`links` binds %q as a %s, and the record published under that name is a %s. "+
				"Every app that uses %q would fail at its first cold start, so this deploy stops here. "+
				"Declare it as what was published, or republish it as a %s",
			link.Name, declared, link.Type, link.Name, declared)
	case link.Source != "" && CrossesMembrane(declared):
		return Refuse(CodeInvalid,
			"`links` binds %q to a %s record published by %s, and ocel's %s client cannot serve one it did not provision. "+
				"Hand the app its name as an env var (`ocel env set`) instead",
			link.Name, declared, link.Source, declared)
	}
	return nil
}

func (r *deployRun) refuseUnpublished(ctx context.Context, missing, published []string) error {
	elsewhere := r.publishingClasses(ctx, missing)
	coordinate := describeCoordinate(string(r.plan.Class), r.plan.linkEnvironment())

	var b strings.Builder
	fmt.Fprintf(&b,
		"`links` binds %s, and nothing has published a record under %s to %s. "+
			"Ocel never runs your infrastructure tool for you: run it, then deploy again",
		quoteAll(missing), thatName(len(missing)), coordinate)
	for _, name := range missing {
		if classes := elsewhere[name]; len(classes) > 0 {
			fmt.Fprintf(&b,
				"\n\n%q is published to %s instead. A publisher writes to one coordinate: point one at %s as well",
				name, strings.Join(classes, " and "), r.plan.Class)
		}
	}
	if len(published) == 0 {
		fmt.Fprintf(&b, "\n\nNothing at all is published to %s.", coordinate)
	} else {
		fmt.Fprintf(&b, "\n\nPublished to %s: %s.", coordinate, strings.Join(published, ", "))
	}
	return Refuse(CodeNotReady, "%s", b.String())
}

func (r *deployRun) publishingClasses(ctx context.Context, missing []string) map[string][]string {
	found := map[string][]string{}
	for _, class := range []Class{ClassProduction, ClassPreview} {
		if class == r.plan.Class {
			continue
		}
		names, err := r.values.PublishedNames(ctx, values.Scope{Project: r.plan.Slug, Class: class}, r.plan.linkEnvironment())
		if err != nil {
			continue
		}
		for _, name := range missing {
			if slices.Contains(names, name) {
				found[name] = append(found[name], string(class))
			}
		}
	}
	return found
}

func (r *deployRun) warnShadowed(resources []Resource, published []string) {
	report := r.report(r.stages.Provisioning, phaseOf(r.stages.Provisioning, r.stages))
	for _, resource := range resources {
		if resource.Linked || !slices.Contains(published, resource.Declared) {
			continue
		}
		report.Say(fmt.Sprintf(
			"a link named %q is already published to %s, and this deploy provisions %s beside it. "+
				"Ocel binds neither to the other on its own: add %q to `links` to consume the published record instead",
			resource.Declared, describeCoordinate(string(r.plan.Class), r.plan.linkEnvironment()), resource.Name, resource.Declared))
	}
}

func describeCoordinate(class, environment string) string {
	if environment == "" {
		return class
	}
	return class + "/" + environment
}

func thatName(n int) string {
	if n == 1 {
		return "that name"
	}
	return "those names"
}

func quoteAll(names []string) string {
	quoted := make([]string, 0, len(names))
	for _, name := range names {
		quoted = append(quoted, fmt.Sprintf("%q", name))
	}
	return strings.Join(quoted, ", ")
}
