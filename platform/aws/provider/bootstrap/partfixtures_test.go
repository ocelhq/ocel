package bootstrap

import (
	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/environment"
)

func namesFor(tier environment.Tier, kind edge.Kind) edgeNames {
	names, err := edgeNamesFor(defaultNamespace, tier, kind)
	if err != nil {
		panic("no edge parameter names for tier " + string(tier) + " and kind " + string(kind))
	}
	return names
}

func cloudflareNames(tier environment.Tier) edgeNames { return namesFor(tier, KindCloudflare) }

func fixtureRefs() stackRefs {
	return stackRefs{
		assetBucket:         "ocel-assets",
		assetBucketARN:      "arn:aws:s3:::ocel-assets",
		stateTable:          "ocel-state",
		stateTableARN:       "arn:aws:dynamodb:us-east-1:111122223333:table/ocel-state",
		stateTableStreamARN: "arn:aws:dynamodb:us-east-1:111122223333:table/ocel-state/stream/2026-01-01T00:00:00.000",
	}
}

func everyPart() PartSet {
	set := PartSet{}
	for _, name := range partNames() {
		set[name] = true
	}
	return set
}

func partTemplate(name string, tier environment.Tier) string {
	return partTemplateWith(name, tier, everyPart())
}

func partTemplateWith(name string, tier environment.Tier, alongside PartSet) string {
	return partStackFor(name, tier, alongside).body
}

func partStackFor(name string, tier environment.Tier, alongside PartSet) partStack {
	f, ok := partNamed(name)
	if !ok {
		panic("no part named " + name)
	}
	return renderedTemplate(f, partInputs{
		ns:        defaultNamespace,
		tier:      tier,
		refs:      fixtureRefs(),
		alongside: alongside,
	})
}

func renderedTemplate(f part, in partInputs) partStack {
	stack, err := f.template(in)
	if err != nil {
		panic(err)
	}
	return stack
}
