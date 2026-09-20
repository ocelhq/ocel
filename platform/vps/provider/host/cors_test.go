package host

import (
	"strings"
	"testing"
)

func TestABucketIsNeverOpenedToEveryOriginThereIs(t *testing.T) {
	t.Parallel()

	body, err := corsBody(nil)
	if err != nil {
		t.Fatalf("corsBody(nil) = %v", err)
	}
	if body != nil {
		t.Fatalf("a bucket no origin is known for is described as %q, and a bucket that answers every origin on the internet answers every page on it", body)
	}

	spec := aStore()
	spec.AllowedOrigins = nil
	calls, err := spec.calls()
	if err != nil {
		t.Fatalf("calls() = %v", err)
	}
	for _, call := range calls {
		if call.query == "cors" {
			t.Errorf("a bucket no origin is known for is still held to some CORS rule: %q", call.body)
		}
		if strings.Contains(string(call.body), "<AllowedOrigin>*<") {
			t.Errorf("%s opens the bucket to every origin: %q", call.what, call.body)
		}
	}
}
