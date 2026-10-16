package bootstrap

import (
	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/pricing"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/refusal"
)

func ShapeParts(ns Namespace, tier environment.Tier, parts []string, options ...ShapeOption) (map[string][]pricing.Shaped, error) {
	in := partInputs{ns: ns, tier: tier, alongside: PartSet{}}
	for _, option := range options {
		option(&in)
	}
	for _, name := range parts {
		in.alongside[name] = true
	}
	shapes := map[string][]pricing.Shaped{
		provider.BasePart: shapeTemplate(coreStackTemplate(ns, tier, coreVariablesKey(in.alongside, in.variablesKey))),
		RuntimeLayersPart: shapedRuntimeLayers(),
	}
	for _, name := range parts {
		f, known := partNamed(name)
		if !known {
			return nil, refusal.Refuse(refusal.CodeInvalid, "this provider has no bootstrap part named %q", name)
		}
		stack, err := f.template(in)
		if err != nil {
			return nil, err
		}
		shapes[name] = shapeTemplate(stack.body)
	}
	return shapes, nil
}
