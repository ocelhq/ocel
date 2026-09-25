package providerkit

import (
	"context"
	"maps"
	"slices"
	"strconv"
	"strings"

	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/providerkit/values"
)

const (
	InjectedPortName = "PORT"
	InjectedPort     = 8080
	ownedPrefix      = "OCEL_"
)

var InjectedPortText = strconv.Itoa(InjectedPort)

func ResourceEnvName(kind BindingType, resource string) string {
	return naming.ResourceEnvName(WireBindingType(kind), resource)
}

func imaged(provider Provider, compute Compute) bool {
	switch compute {
	case ComputeContainer:
		return true
	case ComputeServerless:
		_, images := provider.(FunctionImager)
		return images
	}
	return false
}

func (r *deployRun) deliver(entry AppEntry, held AppValues) map[string]string {
	if !imaged(r.provider, entry.Compute()) {
		return nil
	}
	delivered := make(map[string]string, len(held.Plain)+len(held.Sensitive)+1)
	maps.Copy(delivered, held.Plain)
	maps.Copy(delivered, held.Sensitive)
	maps.Copy(delivered, held.Injected())
	return delivered
}

func (r *deployRun) refuseUnsetSecret(app, key string) error {
	return Refuse(CodeNotReady,
		"app %s declares %s as a secret and nothing is stored for it in %s: a container is handed the value the deploy resolved, so an unset secret is refused here rather than at the app's first read. Set it with `ocel env set %s <value>`",
		app, key, describeCoordinate(string(r.plan.Class), r.plan.bindingEnvironment()), key)
}

func (r *deployRun) refuseContainerValues(ctx context.Context) error {
	var stored map[values.Cell]bool
	for _, entry := range r.plan.Apps {
		if !imaged(r.provider, entry.Compute()) {
			continue
		}
		held, err := r.manifestValues(entry, nil)
		if err != nil {
			return err
		}
		if err := refuseOwnedNames(entry.App, entry.Manifest.GetClientBundle(), held); err != nil {
			return err
		}
		if len(held.Secrets) == 0 {
			continue
		}
		if stored == nil {
			if stored, err = r.storedCells(ctx); err != nil {
				return err
			}
		}
		for _, secret := range held.Secrets {
			if !stored[values.Cell{Folder: secret.Folder, Key: secret.Key}] {
				return r.refuseUnsetSecret(entry.App, secret.Key)
			}
		}
	}
	return nil
}

func (r *deployRun) storedCells(ctx context.Context) (map[values.Cell]bool, error) {
	held, err := r.values.List(ctx, r.scope)
	if err != nil {
		return nil, err
	}
	shadowed := map[string]bool{"": true, r.plan.bindingEnvironment(): true}
	stored := make(map[values.Cell]bool, len(held))
	for _, metadata := range held {
		if shadowed[metadata.Coordinate.Environment] {
			stored[metadata.Coordinate.Cell] = true
		}
	}
	return stored, nil
}

func refuseOwnedNames(app string, clientBundle bool, held AppValues) error {
	var injected, served, owned []string
	for _, key := range declaredNames(clientBundle, held) {
		switch {
		case key == InjectedPortName:
			injected = append(injected, key)
		case key == HandlerName:
			served = append(served, key)
		case strings.HasPrefix(key, ownedPrefix):
			owned = append(owned, key)
		}
	}
	if len(served) > 0 {
		return Refuse(CodeInvalid,
			"app %s declares %s, and %s is the name a function's image sets to the file its runtime serves: a value declared under it would take the place of the app's own entrypoint and leave the release gated on a function that never boots. Rename it",
			app, strings.Join(served, ", "), HandlerName)
	}
	if len(injected) > 0 {
		return Refuse(CodeInvalid,
			"app %s declares %s, and %s is the one name a provider running a container injects itself: it names the port the app is told to bind, so a value declared under it would either be lost or win and leave the release gated on a port nothing is listening on. Rename it",
			app, strings.Join(injected, ", "), InjectedPortName)
	}
	if len(owned) > 0 {
		return Refuse(CodeInvalid,
			"app %s declares %s, and a container is handed every value it declares under that value's own bare name: %s is the prefix ocel delivers its own entries under, so a value named that way would sit beside — or on top of — a bound resource's record. Rename it",
			app, strings.Join(owned, ", "), ownedPrefix)
	}
	return nil
}

func declaredNames(clientBundle bool, held AppValues) []string {
	names := make([]string, 0, len(held.Plain)+len(held.Sensitive)+len(held.Secrets))
	for _, named := range []map[string]string{held.Plain, held.Sensitive} {
		for key := range named {
			if OcelWritten(clientBundle, key) {
				continue
			}
			names = append(names, key)
		}
	}
	for _, secret := range held.Secrets {
		names = append(names, secret.Key)
	}
	slices.Sort(names)
	return names
}
