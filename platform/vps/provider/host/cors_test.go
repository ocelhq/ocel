package host

import (
	"net/http"
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
		if call.query == "cors" && call.method != http.MethodDelete {
			t.Errorf("a bucket no origin is known for is still bound by some CORS rule: %q", call.body)
		}
		if strings.Contains(string(call.body), "<AllowedOrigin>*<") {
			t.Errorf("%s opens the bucket to every origin: %q", call.what, call.body)
		}
	}
}

func TestABucketNoOriginIsKnownForIsBoundByNoRuleAtAll(t *testing.T) {
	t.Parallel()

	spec := aStore()
	spec.AllowedOrigins = nil
	calls, err := spec.calls()
	if err != nil {
		t.Fatalf("calls() = %v", err)
	}
	for _, call := range calls {
		if call.query == "cors" && call.method == http.MethodDelete {
			return
		}
	}
	t.Errorf("a bucket no origin is known for keeps whatever rule it last had: %v. The hostname it answered was released, or the origin it declared was dropped, and its browsers are still answered", calls)
}
