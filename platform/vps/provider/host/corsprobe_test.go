package host

import (
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/ocelhq/ocel/pkg/constants"
	"github.com/ocelhq/ocel/pkg/providerkit/enginetest"
)

func TestARealStoreTakesEveryCallABucketIsDescribedWith(t *testing.T) {
	if testing.Short() {
		t.Skip("stands a real store up")
	}
	store := enginetest.SharedObjectStore(t)

	for what, spec := range map[string]BucketSpec{
		"a bucket the project named no origin for": {Bucket: "plain-bucket"},
		"a bucket with declared origins":           {Bucket: "cors-bucket", AllowedOrigins: []string{"https://app.example.com"}},
		"a public bucket":                          {Bucket: "public-bucket", Public: true},
		"the store's own sessions bucket":          {Bucket: constants.StoreSessionsBucket(), Internal: true},
	} {
		store.ClaimBucket(t, spec.Bucket)
		spec.Store = store.Name
		spec.Endpoint = store.Inside
		spec.Region = store.Region
		spec.AccessKeyID = store.AccessKeyID
		spec.SecretKey = store.SecretKey

		calls, err := spec.calls()
		if err != nil {
			t.Fatalf("%s: calls() = %v", what, err)
		}
		for _, call := range calls {
			req, err := spec.signed(call, time.Now().UTC())
			if err != nil {
				t.Fatalf("%s: signed(%s) = %v", what, call.what, err)
			}
			run := exec.Command("sh", "-c", curlCommand(spec.Store, req, call))
			run.Stdin = fedBody(call.body)
			if out, err := run.CombinedOutput(); err != nil {
				t.Errorf("%s: the store refused to %s: %v\n%s",
					what, call.what, err, strings.TrimSpace(string(out)))
			}
		}
	}
}
