package bootstrap

import (
	"context"
	"fmt"
)

var isrFeature = feature{
	name:     FeatureISR,
	summary:  "Incremental static regeneration: the shared revalidation queue, the revalidator that drains it, and the invalidator that carries each tag raise out to the fronts.",
	template: isrTemplate,
	payloads: isrPayloads,
}

func isrPayloads(ctx context.Context, store ObjectStore, bucket string) (stackPayloads, error) {
	var code stackPayloads
	var err error
	if code.revalidator, err = ensureRevalidatorPayload(ctx, store, bucket); err != nil {
		return code, err
	}
	code.invalidator, err = ensureTagInvalidatorPayload(ctx, store, bucket)
	return code, err
}

func isrTemplate(in featureInputs) featureStack {
	params, values := crossStack([]crossStackParam{
		{paramAssetBucketName, "The core bootstrap's asset bucket, where the revalidator reads each build's origin descriptor and the fronts read their static assets.", in.refs.assetBucket},
		{paramAssetBucketARN, "ARN of that bucket, so the revalidator's role can be scoped to the origin descriptors inside it and nothing else.", in.refs.assetBucketARN},
		{paramStateTableName, "The core bootstrap's state table, holding the tag clock the invalidator turns into cache invalidations.", in.refs.stateTable},
		{paramStateTableARN, "ARN of that table, so the invalidator's role can read the ledger items naming which distributions to reach.", in.refs.stateTableARN},
		{paramStateTableStreamARN, "ARN of that table's stream, the only trigger the invalidator has.", in.refs.stateTableStreamARN},
	})
	return featureStack{
		params: values,
		body: fmt.Sprintf(`AWSTemplateFormatVersion: '2010-09-09'
Description: "Ocel bootstrap feature (%s, %s) - incremental static regeneration for every app in this substrate: the queue a front sends an admitted refresh to, the revalidator that turns it into one signed render at the app's own origin, and the invalidator that reads each tag raise off the state table stream and invalidates the fronts holding the stale copy. Created and updated by ocel bootstrap --features. Deleting this stack leaves apps serving stale pages until they are redeployed without it."
%sResources:
%s%s%sOutputs:
%s%s`,
			FeatureISR, in.class, params,
			revalidateQueueResources(in.class),
			revalidatorResources(in.code.revalidator),
			tagInvalidatorResources(in.code.invalidator, in.class),
			revalidateQueueOutputs(), bootstrapVersionOutput(in.version)),
	}
}
