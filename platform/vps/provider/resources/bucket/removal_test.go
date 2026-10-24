package bucket

import (
	"fmt"
	"os/exec"
	"testing"
	"time"
)

func nowHere() time.Time { return time.Now().UTC() }

func TestNothingIsAskedOfAStoreTheBoxIsNoLongerRunning(t *testing.T) {
	t.Parallel()

	spec := Spec{
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
