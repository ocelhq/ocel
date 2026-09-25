package inlinebinding

import (
	"context"

	bindingsv1 "github.com/ocelhq/ocel/pkg/proto/common/bindings/v1"
	s3store "github.com/ocelhq/ocel/platform/s3"
)

func CheckBucket(ctx context.Context, props *bindingsv1.BucketProperties, public bool, origins []string) ([]string, error) {
	return s3store.Check(ctx, props, s3store.Want{Public: public, Origins: origins})
}
