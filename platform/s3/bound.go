package s3

import (
	"context"
	"strings"

	bindingsv1 "github.com/ocelhq/ocel/pkg/proto/common/bindings/v1"
)

func Bound(tag, name string, record *bindingsv1.BucketProperties, callbacks Poster) *Service {
	return bound(tag, name, record, callbacks, storeOf(record).Client())
}

func storeOf(record *bindingsv1.BucketProperties) Store {
	return Store{
		Endpoint:        record.GetEndpoint(),
		Region:          record.GetRegion(),
		AccessKeyID:     record.GetAccessKeyId(),
		SecretAccessKey: record.GetSecretAccessKey(),
		PathStyle:       record.GetPathStyle(),
	}
}

func bound(tag, name string, record *bindingsv1.BucketProperties, callbacks Poster, objects ObjectAPI) *Service {
	signer := storeOf(record).Presigner()
	held := scope{bucket: record.GetBucket()}
	if prefix := strings.Trim(record.GetPrefix(), "/"); prefix != "" {
		held.prefix = prefix + "/"
	}
	svc := New(Config{
		Tag:          tag,
		Objects:      objects,
		Internal:     signer,
		External:     func(context.Context) (PresignAPI, string) { return signer, record.GetEndpoint() },
		Callbacks:    callbacks,
		SweepUploads: true,
	})
	svc.sessions = held
	svc.granted = map[string]scope{name: held}
	return svc
}

func Endpointed(record *bindingsv1.BucketProperties) bool {
	return record.GetEndpoint() != ""
}
