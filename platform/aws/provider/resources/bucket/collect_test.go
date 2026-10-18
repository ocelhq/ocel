package bucket

import (
	"testing"

	bindingsv1 "github.com/ocelhq/ocel/pkg/proto/common/bindings/v1"
	"github.com/ocelhq/ocel/platform/aws/provider/component/componenttest"
)

const fixtureStateTableARN = "arn:aws:dynamodb:us-east-1:111122223333:table/ocel-state"

var fixtureSessions = NewSessions("shop", "prod", fixtureStateTableARN)

func TestBucketProducerEmitsTheFixture(t *testing.T) {
	t.Parallel()

	want := componenttest.BindingFixture(t, bindingsv1.BindingType_BINDING_TYPE_BUCKET).GetBucket()
	fields := map[string]any{OutputKeyBucket: want.GetBucket()}

	got, err := collectBinding("bucket--uploads", fixtureSessions, fields)
	if err != nil {
		t.Fatalf("collectBucketBinding: %v", err)
	}
	componenttest.AssertMatchesFixture(t, got, bindingsv1.BindingType_BINDING_TYPE_BUCKET)
}
