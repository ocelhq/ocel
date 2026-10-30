package account

import (
	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/pricing"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/refusal"
)

func InventoryParts(ns Namespace, tier environment.Tier, parts []string, options ...InventoryOption) (map[string][]pricing.Resource, error) {
	in := partInputs{ns: ns, tier: tier, alongside: PartSet{}, refs: stackRefs{bucketTag: naming.NewRandomTag()}}
	for _, option := range options {
		option(&in)
	}
	for _, name := range parts {
		in.alongside[name] = true
	}
	core, err := inventoryTemplate(coreStackTemplate(ns, tier, coreVariablesKey(in.alongside, in.variablesKey), in.refs.bucketTag))
	if err != nil {
		return nil, err
	}
	inventories := map[string][]pricing.Resource{
		provider.BasePart: core,
		RuntimeLayersPart: InventoryRuntimeLayers(),
	}
	for _, name := range parts {
		f, known := partNamed(name)
		if !known {
			return nil, refusal.Refuse(refusal.CodeInvalid, "this provider has no bootstrap part named %q", name)
		}
		if f.free {
			continue
		}
		stack, err := f.template(in)
		if err != nil {
			return nil, err
		}
		if inventories[name], err = inventoryTemplate(stack.template); err != nil {
			return nil, err
		}
	}
	return inventories, nil
}
