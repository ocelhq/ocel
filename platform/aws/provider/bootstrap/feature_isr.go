package bootstrap

import (
	"context"
	"fmt"

	"github.com/ocelhq/ocel/pkg/buildoutput"
)

var isrFeature = feature{
	name:       FeatureISR,
	summary:    "incremental static regeneration — the invalidator that clears stale copies from the fronts",
	serves:     buildoutput.Uses{ISR: true},
	template:   isrTemplate,
	payloads:   isrPayloads,
	placements: isrPlacements,
}

func isrPayloads(ctx context.Context, store ObjectStore, bucket string) (stackPayloads, error) {
	var code stackPayloads
	var err error
	code.invalidator, err = ensureTagInvalidatorPayload(ctx, store, bucket)
	return code, err
}

func isrPlacements(bucket string) stackPayloads {
	return stackPayloads{
		invalidator: tagInvalidatorPlacement(bucket),
	}
}

func isrTemplate(in featureInputs) (featureStack, error) {
	params, values := crossStack([]crossStackParam{
		{paramStateTableName, "The core bootstrap's state table, which stores the tag clock the invalidator turns into cache invalidations.", in.refs.stateTable},
		{paramStateTableARN, "ARN of that table, so the invalidator's role can read the ledger items naming which distributions to reach.", in.refs.stateTableARN},
		{paramStateTableStreamARN, "ARN of that table's stream, the only trigger the invalidator has.", in.refs.stateTableStreamARN},
	})
	seedParam, err := PurgeSeedParamFor(in.ns, in.tier, KindCloudflare)
	if err != nil {
		return featureStack{}, err
	}
	return featureStack{
		params: values,
		body: fmt.Sprintf(`AWSTemplateFormatVersion: '2010-09-09'
Description: "Ocel bootstrap feature (%s, %s) - incremental static regeneration for every app in this bootstrap: the invalidator that clears the stale copy from the fronts that cached it."
%sResources:
%s`,
			FeatureISR, in.tier, params,
			tagInvalidatorResources(in.ns, in.code.invalidator, in.tier, seedParam)),
	}, nil
}
