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
		imageOptimizerARN:   "arn:aws:lambda:us-east-1:111122223333:function:ocel-image-optimizer",
	}
}

func everyFeature() FeatureSet {
	set := FeatureSet{}
	for _, name := range featureNames() {
		set[name] = true
	}
	return set
}

func featureTemplate(name string, tier environment.Tier) string {
	return featureTemplateWith(name, tier, everyFeature())
}

func featureTemplateWith(name string, tier environment.Tier, alongside FeatureSet) string {
	return featureStackFor(name, tier, alongside).body
}

func featureStackFor(name string, tier environment.Tier, alongside FeatureSet) featureStack {
	f, ok := featureNamed(name)
	if !ok {
		panic("no feature named " + name)
	}
	return renderedTemplate(f, featureInputs{
		ns:        defaultNamespace,
		tier:      tier,
		code:      fixturePayloads(),
		refs:      fixtureRefs(),
		alongside: alongside,
	})
}

func renderedTemplate(f feature, in featureInputs) featureStack {
	stack, err := f.template(in)
	if err != nil {
		panic(err)
	}
	return stack
}
