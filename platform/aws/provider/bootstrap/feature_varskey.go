package bootstrap

import "fmt"

var varsKeyFeature = feature{
	name:     FeatureVarsKey,
	summary:  "a KMS key to encrypt variables under, the one bootstrap item with a standing cost, about $1 a month prorated hourly",
	template: varsKeyTemplate,
}

func varsKeyTemplate(in featureInputs) featureStack {
	return featureStack{
		body: fmt.Sprintf(`AWSTemplateFormatVersion: '2010-09-09'
Description: "Ocel bootstrap feature (%s, %s) - the KMS key every encrypted variable of this class is sealed under, and the alias naming it."
Resources:
%sOutputs:
%s`,
			FeatureVarsKey, in.class,
			varsKeyResources(in.class),
			varsKeyOutputs()),
	}
}
