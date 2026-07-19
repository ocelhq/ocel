package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/ssm"
	ssmtypes "github.com/aws/aws-sdk-go-v2/service/ssm/types"
)

// cacheStoreParamEnv names the SSM parameter holding this substrate's adopted
// cache store. The deploy resolves the substrate class and writes the parameter
// name here, so the runtime never has to know which class it is running under —
// and the execution role's grant is scoped to this exact name. Unset means a
// deploy that predates the cache-store offer, or an app with no ISR cache;
// either way the app stays on the provider's own store.
const cacheStoreParamEnv = "OCEL_CACHE_STORE_PARAM"

// The store coordinates injected into node's environment. The Next cache
// handler reads these to address the edge-provisioned bucket with the
// S3-compatible client it already has; absent, it falls back to OCEL_ISR_BUCKET.
const (
	storeBucketEnv    = "OCEL_ISR_STORE_BUCKET"
	storeEndpointEnv  = "OCEL_ISR_STORE_ENDPOINT"
	storeRegionEnv    = "OCEL_ISR_STORE_REGION"
	storeAccessKeyEnv = "OCEL_ISR_STORE_ACCESS_KEY_ID"
	storeSecretEnv    = "OCEL_ISR_STORE_SECRET_ACCESS_KEY"
)

// configBudget is the slice of startupBudget the cache-store fetch and its
// retries may consume; node keeps whatever is left. GetParameter is a sub-100ms
// call, so two seconds absorbs a throttle or two while leaving node the bulk of
// the ~10s init ceiling. Letting retries run the whole budget would trade a
// diagnosable init failure for node being killed mid-boot with nothing to say.
const configBudget = 2 * time.Second

// cacheStore mirrors the JSON the provider writes to the cache-store parameter.
// It is redeclared rather than imported from cloud/aws/bootstrap because that
// package's other entry points drag CloudFormation and IAM into this binary —
// 2.3MB of client code the runtime never calls, on the cold path of every cold
// start. The JSON is the contract, not the Go type, and a test pins the two
// shapes together without shipping the dependency.
type cacheStore struct {
	Bucket          string `json:"bucket"`
	Endpoint        string `json:"endpoint"`
	Region          string `json:"region"`
	AccessKeyID     string `json:"accessKeyId"`
	SecretAccessKey string `json:"secretAccessKey"`
}

// env renders the store as node environment entries, or nothing at all for the
// zero store — which is how an unadopted offer stays on the provider's store.
func (s cacheStore) env() []string {
	if s.Bucket == "" {
		return nil
	}
	return []string{
		storeBucketEnv + "=" + s.Bucket,
		storeEndpointEnv + "=" + s.Endpoint,
		storeRegionEnv + "=" + s.Region,
		storeAccessKeyEnv + "=" + s.AccessKeyID,
		storeSecretEnv + "=" + s.SecretAccessKey,
	}
}

// configFetcher reads one globally bootstrapped parameter. An absent parameter
// must read as the zero store and no error: that is the signal to stay on the
// provider's own store, and conflating it with an error would turn the epic's
// rollback path into a fleet-wide init failure.
type configFetcher interface {
	fetchCacheStore(ctx context.Context, param string) (cacheStore, error)
}

type ssmFetcher struct{ client *ssm.Client }

func (f ssmFetcher) fetchCacheStore(ctx context.Context, param string) (cacheStore, error) {
	out, err := f.client.GetParameter(ctx, &ssm.GetParameterInput{
		Name:           aws.String(param),
		WithDecryption: aws.Bool(true),
	})
	if err != nil {
		var notFound *ssmtypes.ParameterNotFound
		if errors.As(err, &notFound) {
			return cacheStore{}, nil
		}
		return cacheStore{}, err
	}
	var store cacheStore
	if err := json.Unmarshal([]byte(aws.ToString(out.Parameter.Value)), &store); err != nil {
		return cacheStore{}, fmt.Errorf("parse %s: %w", param, err)
	}
	return store, nil
}

// fetchCacheStoreEnv resolves the store into node environment entries, retrying
// a failing fetch with bounded backoff until budget expires and then failing.
// Failing closed is deliberate: a function that comes up unable to reach its
// cache would re-render every page forever, which is expensive and invisible,
// and it is what the same mechanism will require once it carries secrets.
func fetchCacheStoreEnv(ctx context.Context, f configFetcher, param string, budget time.Duration) ([]string, error) {
	ctx, cancel := context.WithTimeout(ctx, budget)
	defer cancel()

	backoff := 100 * time.Millisecond
	for attempt := 1; ; attempt++ {
		store, err := f.fetchCacheStore(ctx, param)
		if err == nil {
			return store.env(), nil
		}
		timer := time.NewTimer(backoff)
		select {
		case <-timer.C:
			timer.Stop()
		case <-ctx.Done():
			timer.Stop()
			return nil, fmt.Errorf("read cache store from %s: gave up after %d attempts in %s: %w", param, attempt, budget, err)
		}
		if backoff < 500*time.Millisecond {
			backoff *= 2
		}
	}
}

// resolveCacheStoreEnv is the wiring fetchCacheStoreEnv is tested without: it
// builds the real SSM client, and only when a parameter is actually named, so an
// app with no cache store pays neither the credential chain nor the API call.
func resolveCacheStoreEnv(ctx context.Context, param string) ([]string, error) {
	if param == "" {
		return nil, nil
	}
	cfg, err := awsconfig.LoadDefaultConfig(ctx)
	if err != nil {
		return nil, fmt.Errorf("load aws config: %w", err)
	}
	return fetchCacheStoreEnv(ctx, ssmFetcher{client: ssm.NewFromConfig(cfg)}, param, configBudget)
}
