package host

import (
	"crypto/md5"
	"encoding/base64"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/ocelhq/ocel/pkg/constants"
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
	made := exec.Command("sh", "-c", curlCommand(store, req, calls[0]))
	made.Stdin = fedBody(calls[0].body)
	if out, err := made.CombinedOutput(); err != nil {
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
		ran := exec.Command("sh", "-c", script)
		ran.Stdin = fedBody(call.body)
		if out, err := ran.CombinedOutput(); err != nil {
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

func TestTheSecretAnAppReachesTheStoreWithNeverRidesTheCommandLine(t *testing.T) {
	t.Parallel()

	const secret = "a-secret-no-process-list-should-hold"
	account := StoreAccount{
		Store: "shop-prod-store-s3", Endpoint: "http://127.0.0.1:9000", Region: "us-east-1",
		RootKeyID: "ocel", RootSecret: "root-secret",
		AccessKeyID: StoreAccountKey("prod", "web"), SecretKey: secret,
		Buckets: []string{"shop-prod-uploads"},
	}
	calls, err := account.calls()
	if err != nil {
		t.Fatalf("calls() = %v", err)
	}
	for _, call := range calls {
		script, err := accountScript(account, call, time.Unix(0, 0).UTC())
		if err != nil {
			t.Fatalf("accountScript(%s) = %v", call.what, err)
		}
		for what, held := range map[string]string{
			"in the clear": secret,
			"base64'd":     base64.StdEncoding.EncodeToString(call.body),
		} {
			if strings.Contains(script, held) {
				t.Errorf("the script that would %s carries the app's store secret %s, and every process on the box reads it out of the process list:\n%s",
					call.what, what, script)
			}
		}
	}
}

func askingAs(t *testing.T, store ExternalStore, bucket, name, method, key, query string, body []byte) string {
	t.Helper()
	held := store
	held.Bucket = bucket
	headers := map[string]string{}
	if query == "cors" {
		sum := md5.Sum(body)
		headers["Content-Type"] = "application/xml"
		headers["Content-MD5"] = base64.StdEncoding.EncodeToString(sum[:])
	}
	call, err := held.call(name, method, key, query, headers, body, false, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	said, err := hereRuns(t)(name, probeScript([]probeCall{call}))
	if err != nil {
		t.Fatal(err)
	}
	return readProbe(said)[name].code
}

func TestAnAppsAccountDrivesTheDataPlaneAndNothingThatReshapesTheBucket(t *testing.T) {
	if testing.Short() {
		t.Skip("stands a real store up")
	}
	root := anExternalStore(t, "scoped-bucket")

	account := StoreAccount{
		AccessKeyID: StoreAccountKey("prod", "scoped"),
		SecretKey:   "an-app-secret",
		Buckets:     []string{"scoped-bucket"},
		Sessions:    constants.StoreSessionsBucket() + "/prod/scoped",
	}
	if err := anAccountOn(t, "ocel-external-store-probe", account); err != nil {
		t.Fatalf("the store granted the app no account of its own: %v", err)
	}
	held := root
	held.AccessKeyID = account.AccessKeyID
	held.SecretKey = account.SecretKey

	cors, err := corsBody([]string{"https://taken.example.com"})
	if err != nil {
		t.Fatal(err)
	}
	policy, err := anonymousReadPolicy("scoped-bucket")
	if err != nil {
		t.Fatal(err)
	}
	for _, allowed := range []struct {
		name   string
		method string
		key    string
		query  string
		body   []byte
		code   string
	}{
		{"write an object", "PUT", "own.txt", "", []byte(probeBody), "200"},
		{"read an object", "GET", "own.txt", "", nil, "200"},
		{"list the bucket", "GET", "", "list-type=2", nil, "200"},
		{"open a multipart upload", "POST", "big.txt", "uploads", nil, "200"},
		{"delete an object", "DELETE", "own.txt", "", nil, "204"},
	} {
		code := askingAs(t, held, "scoped-bucket", allowed.name, allowed.method, allowed.key, allowed.query, allowed.body)
		if code != allowed.code {
			t.Errorf("the app's own account answered %s asked to %s, which is what its data plane does all day", code, allowed.name)
		}
	}

	for _, refused := range []struct {
		name   string
		method string
		query  string
		body   []byte
	}{
		{"open the bucket to every origin", "PUT", "cors", cors},
		{"open the bucket to the anonymous", "PUT", "policy", policy},
		{"take the whole bucket down", "DELETE", "", nil},
	} {
		code := askingAs(t, held, "scoped-bucket", refused.name, refused.method, "", refused.query, refused.body)
		if code != "403" {
			t.Errorf("the app's own account answered %s asked to %s, and reshaping the bucket is the deploy's to do and not the app's", code, refused.name)
		}
	}
}
