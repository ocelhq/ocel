package host

import (
	"fmt"
	"net/http"
	"os/exec"
	"slices"
	"strconv"
	"testing"
	"time"
)

func nowHere() time.Time { return time.Now().UTC() }

func runsHere(t *testing.T) probeRunner {
	t.Helper()
	return func(what, script string) (string, error) {
		run := exec.Command("sh", "-c", script)
		out, err := run.Output()
		if err != nil {
			return string(out), fmt.Errorf("%s: %w", what, err)
		}
		return string(out), nil
	}
}

func aStoredBucket(t *testing.T, bucket string) BucketSpec {
	t.Helper()
	spec := BucketSpec{
		Store:       aPublishedStore(t),
		Endpoint:    "http://127.0.0.1:9000",
		Region:      "us-east-1",
		AccessKeyID: "ocel",
		SecretKey:   "probe-secret",
		Bucket:      bucket,
	}
	aBucketOn(t, spec.Store, bucket)
	return spec
}

func droveHere(t *testing.T, spec BucketSpec, calls []storeCall) map[string]probeAnswer {
	t.Helper()
	said, err := droveStore(spec, calls, time.Now().UTC(), runsHere(t))
	if err != nil {
		t.Fatalf("the store refused what the test asked of it: %v", err)
	}
	return said
}

func reachedFromHere(spec BucketSpec) ExternalStore {
	return ExternalStore{
		Endpoint: "http://127.0.0.1:" + probePort, Region: spec.Region, Bucket: spec.Bucket,
		AccessKeyID: spec.AccessKeyID, SecretKey: spec.SecretKey, PathStyle: true,
	}
}

func wroteObjects(t *testing.T, spec BucketSpec, count int) {
	t.Helper()
	store := reachedFromHere(spec)
	const batch = 100
	for written := 0; written < count; written += batch {
		var calls []probeCall
		for i := written; i < count && i < written+batch; i++ {
			at := strconv.Itoa(i)
			call, err := store.call("put"+at, http.MethodPut, "held/"+at+".txt", "",
				nil, []byte(probeBody), false, time.Now().UTC())
			if err != nil {
				t.Fatal(err)
			}
			calls = append(calls, call)
		}
		said, err := runsHere(t)("write what the bucket is to hold", probeScript(calls))
		if err != nil {
			t.Fatal(err)
		}
		for name, answer := range readProbe(said) {
			if !slices.Contains(storeWrote, answer.code) {
				t.Fatalf("the store answered %s writing %s", answer.code, name)
			}
		}
	}
}

func anOpenUpload(t *testing.T, spec BucketSpec, key string) {
	t.Helper()
	said := droveHere(t, spec, []storeCall{{
		name: "create", what: "open an upload", method: http.MethodPost,
		key: key, query: "uploads", capture: true, allow: []string{"200"},
	}})
	found := uploadIDInAnswer.FindSubmatch(said["create"].body)
	if found == nil {
		t.Fatalf("the store opened no upload: %s", said["create"].body)
	}
	droveHere(t, spec, []storeCall{{
		name: "part", what: "stage a part", method: http.MethodPut, key: key,
		query: "partNumber=1&uploadId=" + string(found[1]),
		body:  []byte(probeBody), allow: storeWrote,
	}})
}

func heldBy(t *testing.T, spec BucketSpec) (int, int) {
	t.Helper()
	said := droveHere(t, spec, listingCalls())
	objects, err := objectsIn(said["objects"])
	if err != nil {
		t.Fatal(err)
	}
	uploads, err := uploadsIn(said["uploads"])
	if err != nil {
		t.Fatal(err)
	}
	return len(objects), len(uploads)
}

func TestABucketRemovedFromTheStoreIsGoneFromTheStoreAndNotJustFromItsDisk(t *testing.T) {
	if testing.Short() {
		t.Skip("stands a real store up")
	}
	spec := aStoredBucket(t, "removed-bucket")
	wroteObjects(t, spec, 3)

	if err := removedBucket(spec, nowHere, runsHere(t)); err != nil {
		t.Fatalf("the store kept the bucket it was asked to take down: %v", err)
	}

	said := droveHere(t, spec, []storeCall{{
		name: "head", what: "ask after the bucket", method: http.MethodHead,
		allow: []string{"200", "404"},
	}})
	if said["head"].code != "404" {
		t.Errorf("the store answered %s asked after a bucket it was told to remove, so its own record of the bucket outlived the bucket", said["head"].code)
	}

	aBucketOn(t, spec.Store, spec.Bucket)
	if objects, uploads := heldBy(t, spec); objects != 0 || uploads != 0 {
		t.Errorf("a bucket of the same name came back holding %d objects and %d unfinished uploads", objects, uploads)
	}
}

func TestRemovingABucketTakesEveryPageOfItAndEveryUnfinishedUploadWithIt(t *testing.T) {
	if testing.Short() {
		t.Skip("stands a real store up")
	}
	spec := aStoredBucket(t, "crowded-bucket")
	wroteObjects(t, spec, 1001)
	anOpenUpload(t, spec, "held/unfinished.bin")

	if err := removedBucket(spec, nowHere, runsHere(t)); err != nil {
		t.Fatalf("the store kept a bucket of more than one page: %v", err)
	}

	aBucketOn(t, spec.Store, spec.Bucket)
	if objects, uploads := heldBy(t, spec); objects != 0 || uploads != 0 {
		t.Errorf("a bucket of the same name came back holding %d objects and %d unfinished uploads", objects, uploads)
	}
}

func TestABucketTheStoreNoLongerHoldsIsRemovedAgainWithoutComplaint(t *testing.T) {
	if testing.Short() {
		t.Skip("stands a real store up")
	}
	spec := aStoredBucket(t, "twice-removed")
	if err := removedBucket(spec, nowHere, runsHere(t)); err != nil {
		t.Fatal(err)
	}
	if err := removedBucket(spec, nowHere, runsHere(t)); err != nil {
		t.Errorf("removing a bucket the store no longer holds failed, and a teardown that cannot be run twice cannot be resumed: %v", err)
	}
}

func TestNothingIsAskedOfAStoreTheBoxIsNoLongerRunning(t *testing.T) {
	t.Parallel()

	spec := BucketSpec{
		Store: "a-store-that-is-not-there", Endpoint: "http://127.0.0.1:9000",
		Region: "us-east-1", AccessKeyID: "ocel", SecretKey: "s3cr3t", Bucket: "orphan",
	}
	if err := removedBucket(spec, nowHere, func(what, script string) (string, error) {
		run := exec.Command("sh", "-c", script)
		out, err := run.Output()
		if err != nil {
			return string(out), fmt.Errorf("%s: %w", what, err)
		}
		return string(out), nil
	}); err != nil {
		t.Errorf("removing a bucket whose store container is gone failed: %v", err)
	}
}
