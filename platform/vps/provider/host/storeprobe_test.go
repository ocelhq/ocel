package host

import (
	"net/url"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/ocelhq/ocel/pkg/constants"
	"github.com/ocelhq/ocel/pkg/providerkit/enginetest"
)

func anExternalStore(t *testing.T, store enginetest.Store, bucket string) ExternalStore {
	t.Helper()
	aBucketOn(t, store, bucket)
	return ExternalStore{
		Endpoint:    store.Endpoint,
		Region:      store.Region,
		Bucket:      bucket,
		AccessKeyID: store.AccessKeyID,
		SecretKey:   store.SecretKey,
		PathStyle:   true,
	}
}

func hereRuns(t *testing.T) probeRunner {
	t.Helper()
	return func(what, script string) (string, error) {
		run := exec.Command("sh", "-c", script)
		out, err := run.Output()
		if err != nil {
			t.Fatalf("%s: %v\n%s", what, err, script)
		}
		return string(out), nil
	}
}

func TestARealStoreAnswersEveryCallTheProbeAsksOfIt(t *testing.T) {
	if testing.Short() {
		t.Skip("stands a real store up")
	}
	store := anExternalStore(t, enginetest.AStore(t), "probe-bucket")

	probed, err := probeExternalStore(store, probePrefix+"abc123/", time.Now().UTC(), hereRuns(t))
	if err != nil {
		t.Fatalf("a store that serves every call was refused: %v", err)
	}
	if !probed.PostPolicies {
		t.Error("the probe found no post policy on a store that signs them, so browsers would be sent a PUT with no size bound")
	}
}

func TestTheProbeLeavesNothingOfItsOwnBehind(t *testing.T) {
	if testing.Short() {
		t.Skip("stands a real store up")
	}
	store := anExternalStore(t, enginetest.AStore(t), "swept-bucket")

	if _, err := probeExternalStore(store, probePrefix+"swept/", time.Now().UTC(), hereRuns(t)); err != nil {
		t.Fatalf("probe = %v", err)
	}

	now := time.Now().UTC()
	listed, err := store.call("objects", "GET", "", "list-type=2&prefix="+url.QueryEscape(constants.ReservedKeyPrefix), nil, nil, true, now)
	if err != nil {
		t.Fatal(err)
	}
	unfinished, err := store.call("uploads", "GET", "", "uploads", nil, nil, true, now)
	if err != nil {
		t.Fatal(err)
	}
	said, err := hereRuns(t)("read what the probe left", probeScript([]probeCall{listed, unfinished}))
	if err != nil {
		t.Fatal(err)
	}
	answers := readProbe(said)

	objects := string(answers["objects"].body)
	if !strings.Contains(objects, "ListBucketResult") {
		t.Fatalf("the store was never asked what it holds: %q", objects)
	}
	if strings.Contains(objects, "<Key>") {
		t.Errorf("the probe left objects behind: %s", objects)
	}
	if opened := string(answers["uploads"].body); strings.Contains(opened, "<Upload>") {
		t.Errorf("the probe left a multipart upload open: %s", opened)
	}
}

func TestAStoreThatAnswersNothingIsRefusedByName(t *testing.T) {
	t.Parallel()

	store := ExternalStore{
		Endpoint: "http://127.0.0.1:1", Region: "us-east-1", Bucket: "absent",
		AccessKeyID: "a", SecretKey: "b", PathStyle: true,
	}
	silence := func(what, script string) (string, error) { return "", nil }

	_, err := probeExternalStore(store, probePrefix+"silent/", time.Now().UTC(), silence)
	if err == nil {
		t.Fatal("a store that answered nothing was accepted")
	}
	for _, named := range []string{"If-None-Match", "Cors", "presigned url", "multipart", "absent"} {
		if !strings.Contains(err.Error(), named) {
			t.Errorf("the refusal does not name %s:\n%s", named, err)
		}
	}
}
