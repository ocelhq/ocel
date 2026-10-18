package s3

import (
	"context"
	"testing"

	"github.com/aws/aws-sdk-go-v2/service/s3"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"

	bucketv1 "github.com/ocelhq/ocel/pkg/proto/app/bucket/v1"
)

type finishedUploads struct{ *fakeStore }

func (finishedUploads) AbortMultipartUpload(context.Context, *s3.AbortMultipartUploadInput, ...func(*s3.Options)) (*s3.AbortMultipartUploadOutput, error) {
	return nil, &s3types.NoSuchUpload{}
}

func TestAbortingAnUploadTheStoreNoLongerHasSucceeds(t *testing.T) {
	t.Parallel()

	service := New(Config{Objects: finishedUploads{newFakeStore()}, Granted: []string{"store"}})
	_, err := service.AbortMultipart(context.Background(), &bucketv1.AbortMultipartRequest{
		Bucket: "store", Key: "a.bin", UploadId: "gone",
	})
	if err != nil {
		t.Fatalf("AbortMultipart() = %v, want a retried abort of an upload already gone to succeed", err)
	}
}
