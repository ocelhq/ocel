package providerserver

import (
	"context"
	"maps"
	"slices"
	"strings"

	"github.com/ocelhq/ocel/pkg/providerkit/appbuild"
	"github.com/ocelhq/ocel/pkg/providerkit/envvars"
	"github.com/ocelhq/ocel/pkg/providerkit/images"
	"github.com/ocelhq/ocel/pkg/providerkit/provider"
	"github.com/ocelhq/ocel/pkg/providerkit/refusal"
)

const ownedPrefix = "OCEL_"

func imaged(p provider.Provider, compute provider.Compute) bool {
	switch compute {
	case provider.ComputeContainer:
		return true
	case provider.ComputeServerless:
		return p.Hooks().FunctionImages != nil
	}
	return false
}

func (r *deployRun) deliver(entry provider.AppEntry, held provider.AppValues) map[string]string {
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
	return refusal.Refuse(refusal.CodeNotReady,
		"app %s declares %s as a secret and nothing is stored for it in %s: a container is handed the value the deploy resolved, so an unset secret is refused here rather than at the app's first read. Set it with `ocel env set %s <value>`",
		app, key, describeCoordinate(string(r.spec.Class), bindingEnvironment(r.spec)), key)
}

func (r *deployRun) refuseContainerValues(ctx context.Context) error {
	var stored map[envvars.Cell]bool
	for _, entry := range r.spec.Apps {
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
			if !stored[envvars.Cell{Folder: secret.Folder, Key: secret.Key}] {
				return r.refuseUnsetSecret(entry.App, secret.Key)
			}
		}
	}
	return nil
}

func (r *deployRun) storedCells(ctx context.Context) (map[envvars.Cell]bool, error) {
	held, err := r.values.List(ctx, r.scope)
	if err != nil {
		return nil, err
	}
	shadowed := map[string]bool{"": true, bindingEnvironment(r.spec): true}
	stored := make(map[envvars.Cell]bool, len(held))
	for _, metadata := range held {
		if shadowed[metadata.Coordinate.Environment] {
			stored[metadata.Coordinate.Cell] = true
		}
	}
	return stored, nil
}

func refuseOwnedNames(app string, clientBundle bool, held provider.AppValues) error {
	var injected, served, owned []string
	for _, key := range declaredNames(clientBundle, held) {
		switch {
		case key == appbuild.InjectedPortName:
			injected = append(injected, key)
		case key == images.HandlerName:
			served = append(served, key)
		case strings.HasPrefix(key, ownedPrefix):
			owned = append(owned, key)
		}
	}
	if len(served) > 0 {
		return refusal.Refuse(refusal.CodeInvalid,
			"app %s declares %s, and %s is the name a function's image sets to the file its runtime serves: a value declared under it would take the place of the app's own entrypoint and leave the release gated on a function that never boots. Rename it",
			app, strings.Join(served, ", "), images.HandlerName)
	}
	if len(injected) > 0 {
		return refusal.Refuse(refusal.CodeInvalid,
			"app %s declares %s, and %s is the one name a provider running a container injects itself: it names the port the app is told to bind, so a value declared under it would either be lost or win and leave the release gated on a port nothing is listening on. Rename it",
			app, strings.Join(injected, ", "), appbuild.InjectedPortName)
	}
	if len(owned) > 0 {
		return refusal.Refuse(refusal.CodeInvalid,
			"app %s declares %s, and a container is handed every value it declares under that value's own bare name: %s is the prefix ocel delivers its own entries under, so a value named that way would sit beside — or on top of — a bound resource's record. Rename it",
			app, strings.Join(owned, ", "), ownedPrefix)
	}
	return nil
}

func declaredNames(clientBundle bool, held provider.AppValues) []string {
	names := make([]string, 0, len(held.Plain)+len(held.Sensitive)+len(held.Secrets))
	for _, named := range []map[string]string{held.Plain, held.Sensitive} {
		for key := range named {
			if appbuild.IsOcelInjectedEnv(clientBundle, key) {
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
