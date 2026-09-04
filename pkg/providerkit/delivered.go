package providerkit

import (
	"context"
	"maps"
	"slices"
	"strings"

	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/providerkit/values"
)

const (
	InjectedPortName = "PORT"
	PhaseEnvName     = "OCEL_PHASE"
	ownedPrefix      = "OCEL_"
)

const PhaseResourcesSuppressed = "resources-suppressed"

func ResourceEnvName(kind LinkType, resource string) string {
	return naming.ResourceEnvName(WireLinkType(kind), resource)
}

func (r *deployRun) deliver(ctx context.Context, entry AppEntry, held AppValues) (map[string]string, error) {
	if entry.Compute() != ComputeContainer {
		return nil, nil
	}
	delivered := make(map[string]string, len(held.Plain)+len(held.Sensitive)+len(held.Secrets)+len(held.Links))
	maps.Copy(delivered, held.Plain)
	maps.Copy(delivered, held.Sensitive)

	cells := make([]values.Cell, 0, len(held.Secrets))
	for _, secret := range held.Secrets {
		cells = append(cells, values.Cell{Folder: secret.Folder, Key: secret.Key})
	}
	reader := values.Reader{
		Records:     r.provider.Records(),
		Sealer:      r.provider.Sealer(),
		Scope:       r.scope,
		Environment: r.plan.linkEnvironment(),
	}
	opened, err := reader.Values(ctx, cells)
	if err != nil {
		return nil, err
	}
	for _, secret := range held.Secrets {
		plaintext, found := opened[secret.Key]
		if !found {
			return nil, r.refuseUnsetSecret(entry.App, secret.Key)
		}
		delivered[secret.Key] = plaintext
	}
	for _, link := range held.Links {
		name := ResourceEnvName(link.Type, grantedResource(link))
		if len(link.Wire) == 0 {
			return nil, Refuse(CodeNotReady,
				"app %s consumes %s, and this deploy resolved no record for it: a container reads a linked resource off %s alone, so handing it an empty one would fail at the app's first connection rather than here",
				entry.App, link.Name, name)
		}
		if _, taken := delivered[name]; taken {
			return nil, Refuse(CodeInvalid,
				"app %s consumes two resources that both reach it as %s, and one would silently take the other's place: rename one of them",
				entry.App, name)
		}
		delivered[name] = string(link.Wire)
	}
	if held.Phase != "" {
		delivered[PhaseEnvName] = held.Phase
	}
	return delivered, nil
}

func (r *deployRun) refuseUnsetSecret(app, key string) error {
	return Refuse(CodeNotReady,
		"app %s declares %s as a secret and nothing is stored for it in %s: a container is handed the value the deploy resolved, so an unset secret is refused here rather than at the app's first read. Set it with `ocel env set %s <value>`",
		app, key, describeCoordinate(string(r.plan.Class), r.plan.linkEnvironment()), key)
}

func (r *deployRun) refuseContainerValues(ctx context.Context) error {
	var stored map[values.Cell]bool
	for _, entry := range r.plan.Apps {
		if entry.Compute() != ComputeContainer {
			continue
		}
		held, err := r.manifestValues(entry, nil)
		if err != nil {
			return err
		}
		if err := refuseOwnedNames(entry.App, held); err != nil {
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
	shadowed := map[string]bool{"": true, r.plan.linkEnvironment(): true}
	stored := make(map[values.Cell]bool, len(held))
	for _, metadata := range held {
		if shadowed[metadata.Coordinate.Environment] {
			stored[metadata.Coordinate.Cell] = true
		}
	}
	return stored, nil
}

func refuseOwnedNames(app string, held AppValues) error {
	var injected, owned []string
	for _, key := range declaredNames(held) {
		switch {
		case key == InjectedPortName:
			injected = append(injected, key)
		case strings.HasPrefix(key, ownedPrefix):
			owned = append(owned, key)
		}
	}
	if len(injected) > 0 {
		return Refuse(CodeInvalid,
			"app %s declares %s, and %s is the one name a provider running a container injects itself: it names the port the app is told to bind, so a value declared under it would either be lost or win and leave the release gated on a port nothing is listening on. Rename it",
			app, strings.Join(injected, ", "), InjectedPortName)
	}
	if len(owned) > 0 {
		return Refuse(CodeInvalid,
			"app %s declares %s, and a container is handed every value it declares under that value's own bare name: %s is the prefix ocel delivers its own entries under, so a value named that way would sit beside — or on top of — a linked resource's record. Rename it",
			app, strings.Join(owned, ", "), ownedPrefix)
	}
	return nil
}

func declaredNames(held AppValues) []string {
	names := make([]string, 0, len(held.Plain)+len(held.Sensitive)+len(held.Secrets))
	for _, named := range []map[string]string{held.Plain, held.Sensitive} {
		for key := range named {
			names = append(names, key)
		}
	}
	for _, secret := range held.Secrets {
		names = append(names, secret.Key)
	}
	slices.Sort(names)
	return names
}
