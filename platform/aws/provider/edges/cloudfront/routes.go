package cloudfront

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"math"
	"math/rand/v2"
	"slices"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/cloudfront"
	"github.com/aws/aws-sdk-go-v2/service/cloudfrontkeyvaluestore"
	kvstypes "github.com/aws/aws-sdk-go-v2/service/cloudfrontkeyvaluestore/types"

	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

const (
	routeAttempts     = 10
	routeRetryBase    = 200 * time.Millisecond
	routeRetryCeiling = 10 * time.Second
)

type route struct {
	Stack       string `json:"stack,omitempty"`
	Origin      string `json:"origin"`
	Release     string `json:"release"`
	Assets      string `json:"assets"`
	AssetPrefix string `json:"assetPrefix,omitempty"`
	Secret      string `json:"secret"`
}

type routeWriter struct {
	clients Clients
	arn     string
	wait    func(context.Context, time.Duration) error
	jitter  func() float64
}

func (w routeWriter) hold(ctx context.Context, attempt int) error {
	delay := time.Duration(float64(routeRetryBase) * math.Pow(2, float64(attempt)))
	if delay > routeRetryCeiling {
		delay = routeRetryCeiling
	}
	spread := w.chance()
	delay += time.Duration(float64(delay) * 0.25 * (2*spread - 1))
	if w.wait != nil {
		return w.wait(ctx, delay)
	}
	return waitFor(ctx, delay)
}

func (w routeWriter) chance() float64 {
	if w.jitter == nil {
		return rand.Float64()
	}
	return w.jitter()
}

func (w routeWriter) apply(ctx context.Context, puts map[string]route, deletes []string) error {
	if w.arn == "" {
		return fmt.Errorf("the native edge names no key value store to publish routes into; bootstrap the account first")
	}
	if len(puts) == 0 && len(deletes) == 0 {
		return nil
	}

	written := make([]kvstypes.PutKeyRequestListItem, 0, len(puts))
	for _, hostname := range slices.Sorted(maps.Keys(puts)) {
		encoded, err := json.Marshal(puts[hostname])
		if err != nil {
			return fmt.Errorf("encode the route %s answers on: %w", hostname, err)
		}
		written = append(written, kvstypes.PutKeyRequestListItem{
			Key:   aws.String(routeKey(hostname)),
			Value: aws.String(string(encoded)),
		})
	}
	lowered := make([]string, 0, len(deletes))
	for _, hostname := range deletes {
		lowered = append(lowered, routeKey(hostname))
	}
	dropped := make([]kvstypes.DeleteKeyRequestListItem, 0, len(lowered))
	for _, hostname := range slices.Compact(slices.Sorted(slices.Values(lowered))) {
		dropped = append(dropped, kvstypes.DeleteKeyRequestListItem{Key: aws.String(hostname)})
	}

	var last error
	for attempt := 0; attempt < routeAttempts; attempt++ {
		if attempt > 0 {
			if err := w.hold(ctx, attempt-1); err != nil {
				return err
			}
		}
		etag, err := w.etag(ctx)
		if err != nil {
			if !throttled(err) {
				return err
			}
			last = err
			continue
		}
		_, err = w.clients.KeyValueStore.UpdateKeys(ctx, &cloudfrontkeyvaluestore.UpdateKeysInput{
			KvsARN:  aws.String(w.arn),
			IfMatch: aws.String(etag),
			Puts:    written,
			Deletes: dropped,
		})
		if err == nil {
			return nil
		}
		if !staleStoreETag(err) && !throttled(err) {
			return fmt.Errorf("publish the hostnames this release answers on: %w", err)
		}
		last = err
	}
	return fmt.Errorf("publish the hostnames this release answers on: the key value store refused or was changed by another deploy on every one of %d attempts, so this deploy stopped rather than overwrite it. Wait for the other deploy to finish, or a few seconds if CloudFront is rate-limiting this account, and promote again: %w", routeAttempts, last)
}

func routeKey(hostname string) string { return strings.ToLower(hostname) }

func routeOwner(ctx context.Context, c Clients, class edge.Class, hostname string) (string, bool, error) {
	store, err := c.CloudFront.DescribeKeyValueStore(ctx, &cloudfront.DescribeKeyValueStoreInput{
		Name: aws.String(keyValueStoreName(class)),
	})
	if err != nil {
		if isNotFound(err) {
			return "", false, nil
		}
		return "", false, fmt.Errorf("read the key value store the native edge routes %s with: %w", class, err)
	}
	out, err := c.KeyValueStore.GetKey(ctx, &cloudfrontkeyvaluestore.GetKeyInput{
		KvsARN: store.KeyValueStore.ARN,
		Key:    aws.String(routeKey(hostname)),
	})
	if err != nil {
		if missingRoute(err) {
			return "", false, nil
		}
		return "", false, fmt.Errorf("read the release %s answers on: %w", hostname, err)
	}
	var held route
	if err := json.Unmarshal([]byte(aws.ToString(out.Value)), &held); err != nil {
		return "", false, fmt.Errorf("decode the route %s answers on: it is not the JSON the resolver reads, so something other than Ocel wrote it. Remove that key from the %s key value store and promote again: %w", hostname, keyValueStoreName(class), err)
	}
	if held.Stack == "" {
		return "", false, nil
	}
	return held.Stack, true, nil
}

func (w routeWriter) etag(ctx context.Context) (string, error) {
	out, err := w.clients.KeyValueStore.DescribeKeyValueStore(ctx, &cloudfrontkeyvaluestore.DescribeKeyValueStoreInput{
		KvsARN: aws.String(w.arn),
	})
	if err != nil {
		return "", fmt.Errorf("read the version of the key value store the native edge routes with: %w", err)
	}
	return aws.ToString(out.ETag), nil
}
