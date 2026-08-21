package bootstrap

func fixtureRefs() stackRefs {
	return stackRefs{
		assetBucket:         "ocel-assets",
		assetBucketARN:      "arn:aws:s3:::ocel-assets",
		stateTable:          "ocel-state",
		stateTableARN:       "arn:aws:dynamodb:us-east-1:111122223333:table/ocel-state",
		stateTableStreamARN: "arn:aws:dynamodb:us-east-1:111122223333:table/ocel-state/stream/2026-01-01T00:00:00.000",
		revalidateQueueARN:  "arn:aws:sqs:us-east-1:111122223333:ocel-revalidate.fifo",
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

func featureTemplate(name, class string) string {
	return featureTemplateWith(name, class, everyFeature())
}

func featureTemplateWith(name, class string, alongside FeatureSet) string {
	return featureStackFor(name, class, alongside).body
}

func featureStackFor(name, class string, alongside FeatureSet) featureStack {
	f, ok := featureNamed(name)
	if !ok {
		panic("no feature named " + name)
	}
	return f.template(featureInputs{
		class:     class,
		code:      fixturePayloads(),
		refs:      fixtureRefs(),
		alongside: alongside,
	})
}
