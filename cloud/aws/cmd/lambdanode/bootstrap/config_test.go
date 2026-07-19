package main

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/ocelhq/ocel/cloud/aws/bootstrap"
)

// fakeFetcher replays a scripted sequence of outcomes, one per attempt, holding
// the last one once exhausted, and counts the attempts made.
type fakeFetcher struct {
	results []struct {
		store cacheStore
		err   error
	}
	calls  int
	params []string
}

func (f *fakeFetcher) fetchCacheStore(_ context.Context, param string) (cacheStore, error) {
	f.calls++
	f.params = append(f.params, param)
	r := f.results[min(f.calls, len(f.results))-1]
	return r.store, r.err
}

func fetcher(results ...any) *fakeFetcher {
	f := &fakeFetcher{}
	for _, r := range results {
		var entry struct {
			store cacheStore
			err   error
		}
		switch v := r.(type) {
		case cacheStore:
			entry.store = v
		case error:
			entry.err = v
		}
		f.results = append(f.results, entry)
	}
	return f
}

var testStore = cacheStore{
	Bucket:          "ocel-isr",
	Endpoint:        "https://acct.r2.cloudflarestorage.com",
	Region:          "auto",
	AccessKeyID:     "AKIAEXAMPLE",
	SecretAccessKey: "s3cret",
}

func TestFetchCacheStoreEnv_InjectsCoordinatesOnSuccess(t *testing.T) {
	f := fetcher(testStore)
	env, err := fetchCacheStoreEnv(context.Background(), f, "/ocel/edge/cache-store", time.Second)
	if err != nil {
		t.Fatalf("fetchCacheStoreEnv() error = %v, want nil", err)
	}
	want := []string{
		"OCEL_ISR_STORE_BUCKET=ocel-isr",
		"OCEL_ISR_STORE_ENDPOINT=https://acct.r2.cloudflarestorage.com",
		"OCEL_ISR_STORE_REGION=auto",
		"OCEL_ISR_STORE_ACCESS_KEY_ID=AKIAEXAMPLE",
		"OCEL_ISR_STORE_SECRET_ACCESS_KEY=s3cret",
	}
	if !reflect.DeepEqual(env, want) {
		t.Errorf("env = %q, want %q", env, want)
	}
	if f.params[0] != "/ocel/edge/cache-store" {
		t.Errorf("fetched %q, want the parameter it was given", f.params[0])
	}
}

// The rollback path for the whole epic: an unadopted offer leaves the parameter
// absent, which the fetcher reports as the zero store and no error. Injecting
// nothing is what keeps the app on S3, so this must not fail init and must not
// set a single variable — a half-set store would point the handler at a bucket
// that does not exist.
func TestFetchCacheStoreEnv_AbsentParameterInjectsNothingAndSucceeds(t *testing.T) {
	env, err := fetchCacheStoreEnv(context.Background(), fetcher(cacheStore{}), "/ocel/edge/cache-store", time.Second)
	if err != nil {
		t.Fatalf("an absent parameter must not fail init: %v", err)
	}
	if len(env) != 0 {
		t.Errorf("env = %q, want nothing injected", env)
	}
}

func TestFetchCacheStoreEnv_RetriesThroughATransientFailure(t *testing.T) {
	f := fetcher(errors.New("throttled"), errors.New("throttled"), testStore)
	env, err := fetchCacheStoreEnv(context.Background(), f, "/ocel/edge/cache-store", time.Second)
	if err != nil {
		t.Fatalf("a transient failure must be absorbed by retry: %v", err)
	}
	if len(env) == 0 {
		t.Error("env is empty, want the store injected after the retries succeeded")
	}
	if f.calls != 3 {
		t.Errorf("calls = %d, want 3 (two failures then a success)", f.calls)
	}
}

// A function that came up unable to reach its cache would re-render every page
// forever, so a sustained failure fails init rather than starting degraded — and
// it must do so inside its slice of the budget, leaving node time to boot.
func TestFetchCacheStoreEnv_SustainedFailureFailsInitWithinBudget(t *testing.T) {
	f := fetcher(errors.New("AccessDeniedException: not authorized"))
	budget := 300 * time.Millisecond

	start := time.Now()
	env, err := fetchCacheStoreEnv(context.Background(), f, "/ocel/edge/cache-store", budget)
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("fetchCacheStoreEnv() error = nil, want a sustained failure to fail init")
	}
	if env != nil {
		t.Errorf("env = %q, want nothing injected on failure", env)
	}
	if !strings.Contains(err.Error(), "AccessDeniedException") {
		t.Errorf("error = %q, want it to carry the underlying diagnostic", err)
	}
	if !strings.Contains(err.Error(), "/ocel/edge/cache-store") {
		t.Errorf("error = %q, want it to name the parameter it could not read", err)
	}
	if elapsed > budget+200*time.Millisecond {
		t.Errorf("took %s, want it bounded by the %s budget so node still gets its share", elapsed, budget)
	}
	if f.calls < 2 {
		t.Errorf("calls = %d, want the budget spent on more than one attempt", f.calls)
	}
}

// The membrane redeclares CacheStore rather than importing it, to keep
// CloudFormation and IAM out of this binary. The JSON is therefore the contract,
// and this is what holds the two shapes together: a store written by the
// provider must read back field-for-field here.
func TestCacheStore_MatchesTheProviderJSONContract(t *testing.T) {
	payload, err := json.Marshal(bootstrap.CacheStore{
		Bucket:          testStore.Bucket,
		Endpoint:        testStore.Endpoint,
		Region:          testStore.Region,
		AccessKeyID:     testStore.AccessKeyID,
		SecretAccessKey: testStore.SecretAccessKey,
	})
	if err != nil {
		t.Fatal(err)
	}
	var got cacheStore
	if err := json.Unmarshal(payload, &got); err != nil {
		t.Fatal(err)
	}
	if got != testStore {
		t.Errorf("round-tripped store = %+v, want %+v", got, testStore)
	}
	if n := reflect.TypeOf(bootstrap.CacheStore{}).NumField(); n != reflect.TypeOf(cacheStore{}).NumField() {
		t.Errorf("provider CacheStore has %d fields but the membrane's copy has %d; the contract has drifted", n, reflect.TypeOf(cacheStore{}).NumField())
	}
}

// An app with no adopted store is deployed with no parameter named, and must
// then pay neither the credential chain nor an API call.
func TestResolveCacheStoreEnv_UnnamedParameterSkipsTheFetch(t *testing.T) {
	env, err := resolveCacheStoreEnv(context.Background(), "")
	if err != nil {
		t.Fatalf("resolveCacheStoreEnv() error = %v, want nil", err)
	}
	if env != nil {
		t.Errorf("env = %q, want nothing injected", env)
	}
}
