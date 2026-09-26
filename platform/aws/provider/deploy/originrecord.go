package deploy

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/ocelhq/ocel/pkg/providerkit/provider"
)

const (
	originRecordVersion = 1

	originRecordSuffix = "/origin.json"
)

type originRecord struct {
	Version      int               `json:"v"`
	FunctionURLs map[string]string `json:"functionUrls"`
}

func routeURLs(functions []appFunction, deployed []provider.Function) map[string]string {
	byLogical := make(map[string]string, len(deployed))
	for _, fn := range deployed {
		byLogical[fn.Name] = fn.URL
	}
	urls := make(map[string]string, len(functions))
	for _, fn := range functions {
		if url := byLogical[fn.Logical]; url != "" {
			urls[fn.route()] = url
		}
	}
	return urls
}

func writeOriginRecord(ctx context.Context, cfg Config, app string, work *appWork, result provider.StackResult) error {
	if work == nil || work.cache == nil {
		return nil
	}
	target := uploadTarget{up: cfg.Objects, bucket: work.cache.Bucket, class: cfg.Class}
	if err := target.validate(); err != nil {
		return fmt.Errorf("publish the origin record for %s: %w", app, err)
	}
	body, err := json.Marshal(originRecord{
		Version:      originRecordVersion,
		FunctionURLs: routeURLs(work.functions.Functions, result.Functions),
	})
	if err != nil {
		return fmt.Errorf("encode the origin record for %s: %w", app, err)
	}
	key := work.cache.Prefix + originRecordSuffix
	if err := putArtifact(ctx, target.up, target.bucket, key, objectHeaders{contentType: "application/json"}, body); err != nil {
		return fmt.Errorf("publish the origin record %s in %s: %w; %s would go live with routes that enqueue a revalidation and never receive one", key, target.bucket, err, app)
	}
	return nil
}
