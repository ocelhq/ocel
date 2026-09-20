package host

import (
	"net/url"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/ocelhq/ocel/pkg/constants"
)

const probePort = "19100"

func aPublishedStore(t *testing.T) string {
	t.Helper()
	name := "ocel-external-store-probe"
	_ = exec.Command(dockerEngine, "rm", "--force", name).Run()
	up := exec.Command(dockerEngine, "run", "--detach", "--name", name,
		"--publish", "127.0.0.1:"+probePort+":9000",
		"--env", "RUSTFS_ACCESS_KEY=ocel",
		"--env", "RUSTFS_SECRET_KEY=probe-secret",
		"--env", "RUSTFS_ADDRESS=:9000",
		"--env", "RUSTFS_CONSOLE_ENABLE=false",
		"--env", "RUSTFS_REGION=us-east-1",
		"--env", "RUSTFS_VOLUMES=/data",
		constants.ObjectStoreImage())
	if out, err := up.CombinedOutput(); err != nil {
		t.Skipf("no store to drive: %v\n%s", err, out)
	}
	t.Cleanup(func() { _ = exec.Command(dockerEngine, "rm", "--force", name).Run() })

	deadline := time.Now().Add(90 * time.Second)
	for {
		ready := exec.Command("curl", "-fsS", "http://127.0.0.1:"+probePort+"/health/ready")
		if err := ready.Run(); err == nil {
			return name
		}
		if time.Now().After(deadline) {
			t.Skip("the store never answered its readiness probe")
		}
		time.Sleep(time.Second)
	}
}

func anExternalStore(t *testing.T, bucket string) ExternalStore {
	t.Helper()
	store := aPublishedStore(t)
	spec := BucketSpec{
		Store:       store,
		Endpoint:    "http://127.0.0.1:9000",
		Region:      "us-east-1",
		AccessKeyID: "ocel",
		SecretKey:   "probe-secret",
		Bucket:      bucket,
	}
	calls, err := spec.calls()
	if err != nil {
		t.Fatal(err)
	}
	req, err := spec.signed(calls[0], time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	made := exec.Command("sh", "-c", curlCommand(store, req, calls[0]))
	if out, err := made.CombinedOutput(); err != nil {
		t.Fatalf("the store kept no bucket to probe: %v\n%s", err, out)
	}
	return ExternalStore{
		Endpoint:    "http://127.0.0.1:" + probePort,
		Region:      "us-east-1",
		Bucket:      bucket,
		AccessKeyID: "ocel",
		SecretKey:   "probe-secret",
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
	store := anExternalStore(t, "probe-bucket")

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
	store := anExternalStore(t, "swept-bucket")

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
