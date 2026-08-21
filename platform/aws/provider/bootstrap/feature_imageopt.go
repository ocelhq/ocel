package bootstrap

import (
	"context"
	"fmt"
)

var imageOptimizationFeature = feature{
	name:       FeatureImageOptimization,
	summary:    "On-demand image optimization: one shared function every front in this substrate calls to transform an image.",
	template:   imageOptimizationTemplate,
	payloads:   imageOptimizationPayloads,
	placements: imageOptimizationPlacements,
}

func imageOptimizationPayloads(ctx context.Context, store ObjectStore, bucket string) (stackPayloads, error) {
	var code stackPayloads
	var err error
	code.optimizer, err = ensureOptimizerPayload(ctx, store, bucket)
	return code, err
}

func imageOptimizationPlacements(bucket string) stackPayloads {
	return stackPayloads{optimizer: optimizerPlacement(bucket)}
}

func imageOptimizationTemplate(in featureInputs) featureStack {
	params, values := crossStack([]crossStackParam{
		{paramAssetBucketName, "The core bootstrap's asset bucket, where the optimizer reads each build's source images and image config.", in.refs.assetBucket},
		{paramAssetBucketARN, "ARN of that bucket, so the optimizer's role reaches the asset and image-config prefixes and nothing else.", in.refs.assetBucketARN},
	})
	return featureStack{
		params: values,
		body: fmt.Sprintf(`AWSTemplateFormatVersion: '2010-09-09'
Description: "Ocel bootstrap feature (%s, %s) - the shared image optimizer every app in this substrate serves transformed images through, and the IAM-authenticated Function URL the fronts call it on. Created and updated by ocel bootstrap --features. Deleting this stack makes optimized image requests answer 502 across this substrate until it is bootstrapped again."
%sResources:
%sOutputs:
%s`,
			FeatureImageOptimization, in.class, params,
			imageOptimizerResources(in.code.optimizer),
			imageOptimizerOutputs()),
	}
}
