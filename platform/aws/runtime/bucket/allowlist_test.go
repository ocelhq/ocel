package bucket

import (
	"context"
	"testing"

	connect "connectrpc.com/connect"

	"github.com/ocelhq/ocel/pkg/constants"
	bucketv1 "github.com/ocelhq/ocel/pkg/proto/app/bucket/v1"
)

func reaching(svc *Service, bucket, key string) map[string]func() error {
	ctx := context.Background()
	return map[string]func() error{
		"head": func() error {
			_, err := svc.Head(ctx, &bucketv1.HeadRequest{Bucket: bucket, Key: key})
			return err
		},
		"list": func() error {
			_, err := svc.List(ctx, &bucketv1.ListRequest{Bucket: bucket, Prefix: key})
			return err
		},
		"delete": func() error {
			_, err := svc.Delete(ctx, &bucketv1.DeleteRequest{Bucket: bucket, Keys: []string{key}})
			return err
		},
		"copy": func() error {
			_, err := svc.Copy(ctx, &bucketv1.CopyRequest{Bucket: bucket, SourceKey: key, DestinationKey: "taken"})
			return err
		},
		"copy to": func() error {
			_, err := svc.Copy(ctx, &bucketv1.CopyRequest{Bucket: bucket, SourceKey: "mine", DestinationKey: key})
			return err
		},
		"sign": func() error {
			_, err := svc.Sign(ctx, &bucketv1.SignRequest{
				Bucket: bucket, Key: key,
				Operation: bucketv1.SignedOperation_SIGNED_OPERATION_GET,
			})
			return err
		},
		"create a multipart upload": func() error {
			_, err := svc.CreateMultipart(ctx, &bucketv1.CreateMultipartRequest{Bucket: bucket, Key: key})
			return err
		},
		"sign parts": func() error {
			_, err := svc.SignParts(ctx, &bucketv1.SignPartsRequest{
				Bucket: bucket, Key: key, UploadId: "u", PartNumbers: []int32{1},
			})
			return err
		},
		"assemble a multipart upload": func() error {
			_, err := svc.CompleteMultipart(ctx, &bucketv1.CompleteMultipartRequest{
				Bucket: bucket, Key: key, UploadId: "u",
				Parts: []*bucketv1.CompletedPart{{PartNumber: 1, Etag: "e"}},
			})
			return err
		},
		"abandon a multipart upload": func() error {
			_, err := svc.AbortMultipart(ctx, &bucketv1.AbortMultipartRequest{Bucket: bucket, Key: key, UploadId: "u"})
			return err
		},
		"presign an upload": func() error {
			_, err := svc.PresignUpload(ctx, &bucketv1.PresignUploadRequest{
				Bucket: bucket,
				Files:  []*bucketv1.PresignFile{{Key: key, Name: "n", Size: 3, MimeType: "image/png"}},
			})
			return err
		},
	}
}

func refused(t *testing.T, what string, err error) {
	t.Helper()
	if err == nil {
		t.Fatalf("%s answered a caller that was granted nothing there", what)
	}
	if code := connect.CodeOf(err); code != connect.CodePermissionDenied {
		t.Fatalf("%s = %v (%v), want it refused as denied", what, err, code)
	}
}

func TestOnlyTheBucketsThisDeploymentWasGrantedAnswerIt(t *testing.T) {
	t.Parallel()

	objects := newFakeS3()
	objects.seed("someone-elses", "secret.png", "xyz", "image/png")
	svc := newObjectService(t, objects)

	for name, reach := range reaching(svc, "someone-elses", "secret.png") {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			refused(t, name, reach())
		})
	}
}

func TestTheStoresOwnBookkeepingIsNoKeyADeploymentCanName(t *testing.T) {
	t.Parallel()

	objects := newFakeS3()
	objects.seed("storage", constants.ReservedKeyPrefix+"sessions/sess_1", "{}", "application/json")
	svc := newObjectService(t, objects)

	for name, reach := range reaching(svc, "storage", constants.ReservedKeyPrefix+"sessions/sess_1") {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			refused(t, name, reach())
		})
	}
}
