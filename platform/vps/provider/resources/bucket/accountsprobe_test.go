//go:build integration

package bucket

import (
	"crypto/md5"
	"encoding/base64"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/ocelhq/ocel/pkg/enginetest"
	s3store "github.com/ocelhq/ocel/platform/s3"
)

func bucketOn(store enginetest.ObjectStore, bucket string) Spec {
	return Spec{
		Store: store.Name, Endpoint: store.Inside, Region: store.Region,
		AccessKeyID: store.AccessKeyID, SecretKey: store.SecretKey, Bucket: bucket,
	}
}

func aBucketOn(t *testing.T, store enginetest.ObjectStore, bucket string) {
	t.Helper()
	store.ClaimBucket(t, bucket)
	spec := bucketOn(store, bucket)
	calls, err := spec.calls()
	if err != nil {
		t.Fatal(err)
	}
	req, err := spec.signed(calls[0], time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	made := exec.Command("sh", "-c", curlCommand(spec.Store, req, calls[0]))
	made.Stdin = fedBody(calls[0].body)
	if out, err := made.CombinedOutput(); err != nil {
		t.Fatalf("the store kept no bucket %s: %v\n%s", bucket, err, out)
	}
}

func anAccountOn(t *testing.T, store enginetest.ObjectStore, account Account) error {
	t.Helper()
	account.Store = store.Name
	account.Endpoint = store.Inside
	account.Region = store.Region
	account.RootKeyID = store.AccessKeyID
	account.RootSecret = store.SecretKey

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

func writingAs(t *testing.T, store signedStore, bucket, key string) string {
	t.Helper()
	inBucket := store
	inBucket.Bucket = bucket
	call, err := inBucket.call("wrote", "PUT", key, "", nil, []byte(probeBody), false, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	said, err := hereRuns(t)("write as the app's own account", probeScript([]probeCall{call}))
	if err != nil {
		t.Fatal(err)
	}
	return readAnswers(said)["wrote"].code
}

func TestAnAppsAccountReachesTheBucketsItWasGrantedAndNoOthers(t *testing.T) {
	if testing.Short() {
		t.Skip("runs a real store")
	}
	store := enginetest.SharedObjectStore(t)
	root := aSignedStore(t, store, "granted-bucket")
	aBucketOn(t, store, "ungranted-bucket")

	account := Account{
		AccessKeyID: AccountKey("shop", "prod", "web"),
		SecretKey:   "an-app-secret",
		Buckets:     []string{"granted-bucket"},
	}
	if err := anAccountOn(t, store, account); err != nil {
		t.Fatalf("the store granted the app no account of its own: %v", err)
	}

	asApp := root
	asApp.AccessKeyID = account.AccessKeyID
	asApp.SecretKey = account.SecretKey

	if code := writingAs(t, asApp, "granted-bucket", "own.txt"); code != "200" {
		t.Errorf("the app's own account answered %s writing to the bucket it was granted", code)
	}
	if code := writingAs(t, asApp, "ungranted-bucket", "other.txt"); code != "403" {
		t.Errorf("the app's own account answered %s writing to a bucket it was never granted, and an account that reaches every bucket is the root credential by another name", code)
	}
}

func TestRevokingAnAccountTheStoreNoLongerHoldsIsNotAFailure(t *testing.T) {
	if testing.Short() {
		t.Skip("runs a real store")
	}
	store := enginetest.SharedObjectStore(t)
	aSignedStore(t, store, "revoked-bucket")
	account := Account{
		AccessKeyID: AccountKey("shop", "prod", "revoked"),
		SecretKey:   "an-app-secret",
		Buckets:     []string{"revoked-bucket"},
	}
	if err := anAccountOn(t, store, account); err != nil {
		t.Fatalf("the store granted the app no account of its own: %v", err)
	}
	account.Store, account.Endpoint, account.Region = store.Name, store.Inside, store.Region
	account.RootKeyID, account.RootSecret = store.AccessKeyID, store.SecretKey
	for _, attempt := range []string{"first", "second"} {
		script, err := accountScript(account, account.revoking(), time.Now().UTC())
		if err != nil {
			t.Fatal(err)
		}
		if out, err := exec.Command("sh", "-c", script).CombinedOutput(); err != nil {
			t.Errorf("the %s revoke of %s = %v: %s, want an account already gone taken as revoked", attempt, account.AccessKeyID, err, strings.TrimSpace(string(out)))
		}
	}
}

func TestGrantingTheSameAppTwiceLimitsItToWhatItDeclaresNow(t *testing.T) {
	if testing.Short() {
		t.Skip("runs a real store")
	}
	store := enginetest.SharedObjectStore(t)
	root := aSignedStore(t, store, "first-bucket")
	aBucketOn(t, store, "second-bucket")

	account := Account{
		AccessKeyID: AccountKey("shop", "prod", "regranted"),
		SecretKey:   "an-app-secret",
		Buckets:     []string{"first-bucket"},
	}
	if err := anAccountOn(t, store, account); err != nil {
		t.Fatal(err)
	}
	account.Buckets = []string{"second-bucket"}
	if err := anAccountOn(t, store, account); err != nil {
		t.Fatalf("a second deploy of the same app was refused its account: %v", err)
	}

	asApp := root
	asApp.AccessKeyID = account.AccessKeyID
	asApp.SecretKey = account.SecretKey
	if code := writingAs(t, asApp, "second-bucket", "now.txt"); code != "200" {
		t.Errorf("the account answered %s writing to the bucket the app declares now", code)
	}
	if code := writingAs(t, asApp, "first-bucket", "then.txt"); code != "403" {
		t.Errorf("the account answered %s writing to a bucket the app no longer declares", code)
	}
}

func askingAs(t *testing.T, store signedStore, bucket, name, method, key, query string, body []byte) string {
	t.Helper()
	inBucket := store
	inBucket.Bucket = bucket
	headers := map[string]string{}
	if query == "cors" {
		sum := md5.Sum(body)
		headers["Content-Type"] = "application/xml"
		headers["Content-MD5"] = base64.StdEncoding.EncodeToString(sum[:])
	}
	call, err := inBucket.call(name, method, key, query, headers, body, false, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	said, err := hereRuns(t)(name, probeScript([]probeCall{call}))
	if err != nil {
		t.Fatal(err)
	}
	return readAnswers(said)[name].code
}

func TestAnAppsAccountDrivesTheDataPlaneAndNothingThatReshapesTheBucket(t *testing.T) {
	if testing.Short() {
		t.Skip("runs a real store")
	}
	store := enginetest.SharedObjectStore(t)
	root := aSignedStore(t, store, "scoped-bucket")

	account := Account{
		AccessKeyID: AccountKey("shop", "prod", "scoped"),
		SecretKey:   "an-app-secret",
		Buckets:     []string{"scoped-bucket"},
		Sessions:    s3store.SessionsBucket() + "/prod/scoped",
	}
	if err := anAccountOn(t, store, account); err != nil {
		t.Fatalf("the store granted the app no account of its own: %v", err)
	}
	asApp := root
	asApp.AccessKeyID = account.AccessKeyID
	asApp.SecretKey = account.SecretKey

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
		code := askingAs(t, asApp, "scoped-bucket", allowed.name, allowed.method, allowed.key, allowed.query, allowed.body)
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
		code := askingAs(t, asApp, "scoped-bucket", refused.name, refused.method, "", refused.query, refused.body)
		if code != "403" {
			t.Errorf("the app's own account answered %s asked to %s, and reshaping the bucket is the deploy's to do and not the app's", code, refused.name)
		}
	}
}
