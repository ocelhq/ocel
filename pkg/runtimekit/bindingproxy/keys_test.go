package bindingproxy

import (
	"context"
	"errors"
	"testing"

	connect "connectrpc.com/connect"

	"github.com/ocelhq/ocel/pkg/constants"
	bucketv1 "github.com/ocelhq/ocel/pkg/proto/app/bucket/v1"
	"github.com/ocelhq/ocel/pkg/proto/app/bucket/v1/bucketv1connect"
)

func keyed(client bucketv1connect.BucketServiceClient, key string) map[string]func() error {
	ctx := context.Background()
	return map[string]func() error{
		"head": func() error {
			_, err := client.Head(ctx, &bucketv1.HeadRequest{Bucket: "storage", Key: key})
			return err
		},
		"list": func() error {
			_, err := client.List(ctx, &bucketv1.ListRequest{Bucket: "storage", Prefix: key})
			return err
		},
		"delete": func() error {
			_, err := client.Delete(ctx, &bucketv1.DeleteRequest{Bucket: "storage", Keys: []string{key}})
			return err
		},
		"copy from": func() error {
			_, err := client.Copy(ctx, &bucketv1.CopyRequest{Bucket: "storage", SourceKey: key, DestinationKey: "b"})
			return err
		},
		"copy to": func() error {
			_, err := client.Copy(ctx, &bucketv1.CopyRequest{Bucket: "storage", SourceKey: "a", DestinationKey: key})
			return err
		},
		"sign": func() error {
			_, err := client.Sign(ctx, &bucketv1.SignRequest{
				Bucket: "storage", Key: key,
				Operation: bucketv1.SignedOperation_SIGNED_OPERATION_GET,
			})
			return err
		},
		"create a multipart upload": func() error {
			_, err := client.CreateMultipart(ctx, &bucketv1.CreateMultipartRequest{Bucket: "storage", Key: key})
			return err
		},
		"sign parts": func() error {
			_, err := client.SignParts(ctx, &bucketv1.SignPartsRequest{
				Bucket: "storage", Key: key, UploadId: "u", PartNumbers: []int32{1},
			})
			return err
		},
		"assemble a multipart upload": func() error {
			_, err := client.CompleteMultipart(ctx, &bucketv1.CompleteMultipartRequest{
				Bucket: "storage", Key: key, UploadId: "u",
				Parts: []*bucketv1.CompletedPart{{PartNumber: 1, Etag: "e"}},
			})
			return err
		},
		"abandon a multipart upload": func() error {
			_, err := client.AbortMultipart(ctx, &bucketv1.AbortMultipartRequest{Bucket: "storage", Key: key, UploadId: "u"})
			return err
		},
		"presign an upload": func() error {
			_, err := client.PresignUpload(ctx, &bucketv1.PresignUploadRequest{
				Bucket: "storage",
				Files:  []*bucketv1.PresignFile{{Key: key, Name: "n", Size: 1, MimeType: "image/png"}},
			})
			return err
		},
	}
}

func TestEveryKeyedCallRefusesAKeyThatIsNotTheAppsToName(t *testing.T) {
	t.Parallel()

	for _, key := range []string{
		constants.ReservedKeyPrefix + "sessions/sess_1",
		"../secrets/key",
		"avatars/../../etc/passwd",
		`a\b`,
		"a\nb",
	} {
		t.Run(key, func(t *testing.T) {
			t.Parallel()
			client, _ := serveBuckets(t)
			for name, reach := range keyed(client, key) {
				var held *connect.Error
				err := reach()
				if !errors.As(err, &held) || held.Code() != connect.CodeInvalidArgument {
					t.Errorf("%s of %q = %v, want it refused before the service ever reads the key", name, key, err)
				}
			}
		})
	}
}
