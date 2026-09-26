package providerserver

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/providerkit/envvars"
	"github.com/ocelhq/ocel/pkg/providerkit/provider"
	"github.com/ocelhq/ocel/pkg/providerkit/refusal"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

func (r *deployRun) admitBindings(ctx context.Context, progress edge.Progress) error {
	resources, err := manifestResources(r.manifest)
	if err != nil {
		return err
	}
	bindings, err := r.reader().Published(ctx)
	if err != nil {
		return err
	}
	published := make(map[string]provider.Binding, len(bindings))
	names := make([]string, 0, len(bindings))
	for _, binding := range bindings {
		published[binding.Name] = binding
		names = append(names, binding.Name)
	}
	r.warnShadowed(progress, resources, published)

	var missing []string
	for _, resource := range resources {
		if resource.Binding == "" || r.writtenByTheDeploy(resource.Binding, published) {
			continue
		}
		if _, bound := published[resource.Binding]; !bound {
			missing = append(missing, resource.Binding)
		}
	}
	if len(missing) > 0 {
		return r.refuseUnpublished(ctx, missing, names)
	}
	for _, resource := range resources {
		if resource.Binding == "" || r.writtenByTheDeploy(resource.Binding, published) {
			continue
		}
		if err := ReadableAs(published[resource.Binding], resource.Declared, resource.Type, proxied); err != nil {
			return err
		}
	}
	return nil
}

func (r *deployRun) writtenByTheDeploy(name string, published map[string]provider.Binding) bool {
	_, held := published[name]
	return r.dry && !held && naming.IsInlineRecord(name)
}

func proxied(kind provider.BindingType) bool {
	return naming.Proxied(provider.WireBindingType(kind))
}

func ReadableAs(binding provider.Binding, declaredName string, declared provider.BindingType, proxied func(provider.BindingType) bool) error {
	switch {
	case binding.Type == provider.BindingCustom:
		return refusal.Refuse(refusal.CodeInvalid,
			"`bindings` binds %s.%s to %q, and the record published under that name is a custom one: "+
				"a custom binding is read by transforms; it is external by definition and never provisioned, so it is not bound here. "+
				"Drop it from `bindings` and read it from a transform as `bindings.custom.%s.<property>`",
			declared, declaredName, binding.Name, binding.Name)
	case binding.Type != declared:
		return refusal.Refuse(refusal.CodeInvalid,
			"`bindings` declares %s as a %s and binds it to %q, and the record published under that name is a %s. "+
				"Every app that uses %s would fail at its first cold start, so this deploy stops here. "+
				"Declare it as what was published, or republish %q as a %s",
			declaredName, declared, binding.Name, binding.Type, declaredName, binding.Name, declared)
	case binding.Source != "" && proxied(declared) && !binding.Endpointed():
		return refusal.Refuse(refusal.CodeInvalid,
			"`bindings` binds %s to the %s record %q published by %s, and the record names no store for ocel's %s client to reach it in. "+
				"Bind it inline instead, with the store's endpoint and a key pair, or publish a record that carries them",
			declaredName, declared, binding.Name, binding.Source, declared)
	}
	return nil
}

func (r *deployRun) refuseUnpublished(ctx context.Context, missing, published []string) error {
	elsewhere := r.publishingClasses(ctx, missing)
	coordinate := describeCoordinate(string(r.plan.Class), bindingEnvironment(r.plan))

	var b strings.Builder
	fmt.Fprintf(&b,
		"`bindings` binds %s, and nothing has published a record under %s to %s. "+
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
	return refusal.Refuse(refusal.CodeNotReady, "%s", b.String())
}

func (r *deployRun) publishingClasses(ctx context.Context, missing []string) map[string][]string {
	found := map[string][]string{}
	for _, class := range []edge.Class{edge.ClassProduction, edge.ClassPreview} {
		if class == r.plan.Class {
			continue
		}
		names, err := r.values.PublishedNames(ctx, envvars.Scope{Project: r.plan.Slug, Class: class}, bindingEnvironment(r.plan))
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

func (r *deployRun) warnShadowed(progress edge.Progress, resources []provider.Resource, published map[string]provider.Binding) {
	for _, resource := range resources {
		if resource.Binding != "" {
			continue
		}
		namesake, held := published[resource.Declared]
		if !held || ReadableAs(namesake, resource.Declared, resource.Type, proxied) != nil {
			continue
		}
		progress.Say(fmt.Sprintf(
			"a binding named %q is already published to %s, and this deploy provisions %s beside it. "+
				"Ocel binds neither to the other on its own: put %q in `bindings` — \"bindings\": { %q: { %q: %q } } — to consume the published record instead",
			resource.Declared, describeCoordinate(string(r.plan.Class), bindingEnvironment(r.plan)), resource.Name,
			resource.Declared, string(resource.Type), resource.Declared, "@"+resource.Declared))
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
