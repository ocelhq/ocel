package host

import (
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/ocelhq/ocel/pkg/constants"
)

func aStandingStore(t *testing.T) string {
	t.Helper()
	name := "ocel-store-probe"
	_ = exec.Command(dockerEngine, "rm", "--force", name).Run()
	up := exec.Command(dockerEngine, "run", "--detach", "--name", name,
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
		ready := exec.Command(dockerEngine, "exec", name,
			"curl", "-fsS", "http://127.0.0.1:9000/health/ready")
		if err := ready.Run(); err == nil {
			return name
		}
		if time.Now().After(deadline) {
			t.Skip("the store never answered its readiness probe")
		}
		time.Sleep(time.Second)
	}
}

func TestARealStoreTakesEveryCallABucketIsDescribedWith(t *testing.T) {
	if testing.Short() {
		t.Skip("stands a real store up")
	}
	store := aStandingStore(t)

	for what, spec := range map[string]BucketSpec{
		"a bucket the project named no origin for": {Bucket: "plain-bucket"},
		"a bucket with declared origins":           {Bucket: "cors-bucket", AllowedOrigins: []string{"https://app.example.com"}},
		"a public bucket":                          {Bucket: "public-bucket", Public: true},
		"the store's own sessions bucket":          {Bucket: constants.StoreSessionsBucket(), Internal: true},
	} {
		spec.Store = store
		spec.Endpoint = "http://127.0.0.1:9000"
		spec.Region = "us-east-1"
		spec.AccessKeyID = "ocel"
		spec.SecretKey = "probe-secret"

		calls, err := spec.calls()
		if err != nil {
			t.Fatalf("%s: calls() = %v", what, err)
		}
		for _, call := range calls {
			req, err := spec.signed(call, time.Now().UTC())
			if err != nil {
				t.Fatalf("%s: signed(%s) = %v", what, call.what, err)
			}
			run := exec.Command("sh", "-c", curlCommand(store, req, call))
			if out, err := run.CombinedOutput(); err != nil {
				t.Errorf("%s: the store refused to %s: %v\n%s",
					what, call.what, err, strings.TrimSpace(string(out)))
			}
		}
	}
}
