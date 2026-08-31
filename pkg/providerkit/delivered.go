package providerkit

import (
	"context"
	"fmt"
	"maps"
	"slices"

	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/providerkit/values"
)

const InjectedPortName = "PORT"

func ResourceEnvName(kind LinkType, resource string) string {
	return naming.ResourceEnvName(WireLinkType(kind), resource)
}

func (r *deployRun) deliver(ctx context.Context, entry AppEntry, held AppValues) (map[string]string, error) {
	if entry.Compute() != ComputeContainer {
		return nil, nil
	}
	if err := refuseInjectedNames(entry.App, held); err != nil {
		return nil, err
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
			return nil, Refuse(CodeNotReady,
				"app %s declares %s as a secret and nothing is stored for it in %s: a container is handed the value the deploy resolved, so an unset secret is refused here rather than at the app's first read. Set it with `ocel env set %s <value>`",
				entry.App, secret.Key, describeCoordinate(string(r.plan.Class), r.plan.linkEnvironment()), secret.Key)
		}
		delivered[secret.Key] = plaintext
	}
	for _, link := range held.Links {
		delivered[ResourceEnvName(link.Type, grantedResource(link))] = string(link.Wire)
	}
	return delivered, nil
}

func refuseInjectedNames(app string, held AppValues) error {
	var taken []string
	for _, named := range []map[string]string{held.Plain, held.Sensitive} {
		for key := range named {
			if key == InjectedPortName {
				taken = append(taken, key)
			}
		}
	}
	for _, secret := range held.Secrets {
		if secret.Key == InjectedPortName {
			taken = append(taken, secret.Key)
		}
	}
	if len(taken) == 0 {
		return nil
	}
	slices.Sort(taken)
	return Refuse(CodeInvalid,
		"app %s declares %s, and %s is the one name a provider running a container injects itself: it names the port the app is told to bind, so a value declared under it would either be lost or win and leave the release gated on a port nothing is listening on. Rename it",
		app, fmt.Sprint(taken), InjectedPortName)
}
