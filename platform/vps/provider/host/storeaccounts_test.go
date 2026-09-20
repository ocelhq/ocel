package host

import (
	"os/exec"
	"strings"
	"testing"
	"time"
)

func aBucketOn(t *testing.T, store, bucket string) {
	t.Helper()
	spec := BucketSpec{
		Store: store, Endpoint: "http://127.0.0.1:9000", Region: "us-east-1",
		AccessKeyID: "ocel", SecretKey: "probe-secret", Bucket: bucket,
	}
	calls, err := spec.calls()
	if err != nil {
		t.Fatal(err)
	}
	req, err := spec.signed(calls[0], time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("sh", "-c", curlCommand(store, req, calls[0])).CombinedOutput(); err != nil {
		t.Fatalf("the store kept no bucket %s: %v\n%s", bucket, err, out)
	}
}

func anAccountOn(t *testing.T, store string, account StoreAccount) error {
	t.Helper()
	account.Store = store
	account.Endpoint = "http://127.0.0.1:9000"
	account.Region = "us-east-1"
	account.RootKeyID = "ocel"
	account.RootSecret = "probe-secret"

	calls, err := account.calls()
	if err != nil {
		t.Fatal(err)
	}
	for _, call := range calls {
		script, err := accountScript(account, call, time.Now().UTC())
		if err != nil {
			t.Fatal(err)
		}
		if out, err := exec.Command("sh", "-c", script).CombinedOutput(); err != nil {
			t.Logf("%s: %s", call.what, strings.TrimSpace(string(out)))
			return err
		}
	}
	return nil
}

func writingAs(t *testing.T, store ExternalStore, bucket, key string) string {
	t.Helper()
	held := store
	held.Bucket = bucket
	call, err := held.call("wrote", "PUT", key, "", nil, []byte(probeBody), false, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	said, err := hereRuns(t)("write as the app's own account", probeScript([]probeCall{call}))
	if err != nil {
		t.Fatal(err)
	}
	return readProbe(said)["wrote"].code
}

func TestAnAppsAccountReachesTheBucketsItWasGrantedAndNoOthers(t *testing.T) {
	if testing.Short() {
		t.Skip("stands a real store up")
	}
	root := anExternalStore(t, "granted-bucket")
	aBucketOn(t, "ocel-external-store-probe", "ungranted-bucket")

	account := StoreAccount{
		AccessKeyID: StoreAccountKey("prod", "web"),
		SecretKey:   "an-app-secret",
		Buckets:     []string{"granted-bucket"},
	}
	if err := anAccountOn(t, "ocel-external-store-probe", account); err != nil {
		t.Fatalf("the store granted the app no account of its own: %v", err)
	}

	held := root
	held.AccessKeyID = account.AccessKeyID
	held.SecretKey = account.SecretKey

	if code := writingAs(t, held, "granted-bucket", "own.txt"); code != "200" {
		t.Errorf("the app's own account answered %s writing to the bucket it was granted", code)
	}
	if code := writingAs(t, held, "ungranted-bucket", "other.txt"); code != "403" {
		t.Errorf("the app's own account answered %s writing to a bucket it was never granted, and an account that reaches every bucket is the root credential by another name", code)
	}
}

func TestGrantingTheSameAppTwiceHoldsItToWhatItDeclaresNow(t *testing.T) {
	if testing.Short() {
		t.Skip("stands a real store up")
	}
	root := anExternalStore(t, "first-bucket")
	aBucketOn(t, "ocel-external-store-probe", "second-bucket")

	account := StoreAccount{
		AccessKeyID: StoreAccountKey("prod", "regranted"),
		SecretKey:   "an-app-secret",
		Buckets:     []string{"first-bucket"},
	}
	if err := anAccountOn(t, "ocel-external-store-probe", account); err != nil {
		t.Fatal(err)
	}
	account.Buckets = []string{"second-bucket"}
	if err := anAccountOn(t, "ocel-external-store-probe", account); err != nil {
		t.Fatalf("a second deploy of the same app was refused its account: %v", err)
	}

	held := root
	held.AccessKeyID = account.AccessKeyID
	held.SecretKey = account.SecretKey
	if code := writingAs(t, held, "second-bucket", "now.txt"); code != "200" {
		t.Errorf("the account answered %s writing to the bucket the app declares now", code)
	}
	if code := writingAs(t, held, "first-bucket", "then.txt"); code != "403" {
		t.Errorf("the account answered %s writing to a bucket the app no longer declares", code)
	}
}

func TestAnAccountIsNamedInWhatEveryStoreKeepsAnAccessKeyIn(t *testing.T) {
	t.Parallel()

	key := StoreAccountKey("prod", "web")
	if len(key) != 20 {
		t.Errorf("StoreAccountKey() = %q, %d characters, and a store keeps an access key in 3 to 20", key, len(key))
	}
	if key == StoreAccountKey("prod", "admin") || key == StoreAccountKey("preview-pr-1", "web") {
		t.Error("two apps reach the store under one account, so either reaches what the other was granted")
	}
}
