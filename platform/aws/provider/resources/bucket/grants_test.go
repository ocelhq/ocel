package bucket

import (
	"testing"

	"github.com/ocelhq/ocel/platform/aws/provider/component/componenttest"
)

func TestBucketGrantsCoverEveryCallTheRuntimeMakes(t *testing.T) {
	t.Parallel()

	granted := map[string]bool{}
	for _, grant := range Grants("shop-prod-uploads-abc", NewSessions("shop", "prod", componenttest.StateTableARN)) {
		for _, action := range grant.GetActions() {
			granted[action] = true
		}
	}

	for _, action := range []string{
		"s3:AbortMultipartUpload",
		"s3:DeleteObject",
		"s3:GetObject",
		"s3:ListBucket",
		"s3:ListBucketMultipartUploads",
		"s3:ListMultipartUploadParts",
		"s3:PutObject",
		"s3:PutObjectTagging",
	} {
		if !granted[action] {
			t.Errorf("an app's bucket role may not %s, and the runtime's data plane calls it", action)
		}
	}
}
