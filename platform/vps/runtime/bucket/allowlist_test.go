package bucket

import (
	"context"
	"testing"

	connect "connectrpc.com/connect"

	"github.com/ocelhq/ocel/pkg/constants"
	bucketv1 "github.com/ocelhq/ocel/pkg/proto/app/bucket/v1"
)

func neighbours(t *testing.T) *harness {
	t.Helper()
	h := newHarness(t, func(cfg *Config) {
		cfg.Granted = []string{"customer/shop/production/uploads"}
		cfg.Sessions = "customer/shop/production/web"
	})
	h.store.put("customer", "shop/production/uploads/mine.png", []byte("abc"), "image/png")
	h.store.put("customer", "other/production/theirs/secret.png", []byte("xyz"), "image/png")
	h.store.put(constants.StoreSessionsBucket(), sessionPrefix+"sess_theirs", []byte(`{"secret":"theirs"}`), "application/json")
	return h
}

func denied(t *testing.T, what string, err error) {
	t.Helper()
	if err == nil {
		t.Fatalf("%s answered a caller that was granted nothing there", what)
	}
	if code := connect.CodeOf(err); code != connect.CodePermissionDenied {
		t.Fatalf("%s = %v (%v), want it refused as denied", what, err, code)
	}
}

func TestOnlyTheBucketsThisAppWasGrantedAnswerIt(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	for name, reach := range map[string]func(*harness, string) error{
		"list": func(h *harness, bucket string) error {
			_, err := h.svc.List(ctx, &bucketv1.ListRequest{Bucket: bucket})
			return err
		},
		"head": func(h *harness, bucket string) error {
			_, err := h.svc.Head(ctx, &bucketv1.HeadRequest{Bucket: bucket, Key: "secret.png"})
			return err
		},
		"delete": func(h *harness, bucket string) error {
			_, err := h.svc.Delete(ctx, &bucketv1.DeleteRequest{Bucket: bucket, Keys: []string{"secret.png"}})
			return err
		},
		"copy": func(h *harness, bucket string) error {
			_, err := h.svc.Copy(ctx, &bucketv1.CopyRequest{Bucket: bucket, SourceKey: "secret.png", DestinationKey: "taken.png"})
			return err
		},
		"sign": func(h *harness, bucket string) error {
			_, err := h.svc.Sign(ctx, &bucketv1.SignRequest{
				Bucket: bucket, Key: "secret.png",
				Operation: bucketv1.SignedOperation_SIGNED_OPERATION_GET,
			})
			return err
		},
		"create a multipart upload": func(h *harness, bucket string) error {
			_, err := h.svc.CreateMultipart(ctx, &bucketv1.CreateMultipartRequest{Bucket: bucket, Key: "secret.png"})
			return err
		},
		"sign parts": func(h *harness, bucket string) error {
			_, err := h.svc.SignParts(ctx, &bucketv1.SignPartsRequest{
				Bucket: bucket, Key: "secret.png", UploadId: "u", PartNumbers: []int32{1},
			})
			return err
		},
		"assemble a multipart upload": func(h *harness, bucket string) error {
			_, err := h.svc.CompleteMultipart(ctx, &bucketv1.CompleteMultipartRequest{
				Bucket: bucket, Key: "secret.png", UploadId: "u",
				Parts: []*bucketv1.CompletedPart{{PartNumber: 1, Etag: "e"}},
			})
			return err
		},
		"abandon a multipart upload": func(h *harness, bucket string) error {
			_, err := h.svc.AbortMultipart(ctx, &bucketv1.AbortMultipartRequest{Bucket: bucket, Key: "secret.png", UploadId: "u"})
			return err
		},
		"presign an upload": func(h *harness, bucket string) error {
			_, err := h.svc.PresignUpload(ctx, &bucketv1.PresignUploadRequest{
				Bucket: bucket,
				Files:  []*bucketv1.PresignFile{{Key: "secret.png", Name: "secret.png", Size: 3, MimeType: "image/png"}},
			})
			return err
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			for _, bucket := range []string{
				"customer/other/production/theirs",
				"customer",
				"customer/shop/production/uploads/deeper",
				constants.StoreSessionsBucket(),
				constants.StoreSessionsBucket() + "/" + constants.ProjectStateDirName,
				"customer/shop/production/web",
			} {
				denied(t, name+" of "+bucket, reach(neighbours(t), bucket))
			}
		})
	}
}

func TestTheGrantedBucketIsReachedUnderTheBoxsOwnPrefix(t *testing.T) {
	t.Parallel()

	h := neighbours(t)
	out, err := h.svc.Head(context.Background(), &bucketv1.HeadRequest{
		Bucket: "customer/shop/production/uploads", Key: "mine.png",
	})
	if err != nil {
		t.Fatalf("Head: %v", err)
	}
	if out.GetObject().GetKey() != "mine.png" || out.GetObject().GetSize() != 3 {
		t.Fatalf("Head() = %+v, want the object the app put there, named as the app named it", out.GetObject())
	}
}

func TestTheStoresOwnBookkeepingIsNoKeyAnAppCanName(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	const granted = "customer/shop/production/uploads"
	reserved := constants.ReservedKeyPrefix + "sessions/sess_theirs"

	for name, reach := range map[string]func(*harness) error{
		"head": func(h *harness) error {
			_, err := h.svc.Head(ctx, &bucketv1.HeadRequest{Bucket: granted, Key: reserved})
			return err
		},
		"delete": func(h *harness) error {
			_, err := h.svc.Delete(ctx, &bucketv1.DeleteRequest{Bucket: granted, Keys: []string{"fine.png", reserved}})
			return err
		},
		"copy from": func(h *harness) error {
			_, err := h.svc.Copy(ctx, &bucketv1.CopyRequest{Bucket: granted, SourceKey: reserved, DestinationKey: "taken.json"})
			return err
		},
		"copy to": func(h *harness) error {
			_, err := h.svc.Copy(ctx, &bucketv1.CopyRequest{Bucket: granted, SourceKey: "mine.png", DestinationKey: reserved})
			return err
		},
		"sign": func(h *harness) error {
			_, err := h.svc.Sign(ctx, &bucketv1.SignRequest{
				Bucket: granted, Key: reserved,
				Operation: bucketv1.SignedOperation_SIGNED_OPERATION_GET,
			})
			return err
		},
		"list": func(h *harness) error {
			_, err := h.svc.List(ctx, &bucketv1.ListRequest{Bucket: granted, Prefix: constants.ReservedKeyPrefix})
			return err
		},
		"create a multipart upload": func(h *harness) error {
			_, err := h.svc.CreateMultipart(ctx, &bucketv1.CreateMultipartRequest{Bucket: granted, Key: reserved})
			return err
		},
		"sign parts": func(h *harness) error {
			_, err := h.svc.SignParts(ctx, &bucketv1.SignPartsRequest{
				Bucket: granted, Key: reserved, UploadId: "u", PartNumbers: []int32{1},
			})
			return err
		},
		"assemble a multipart upload": func(h *harness) error {
			_, err := h.svc.CompleteMultipart(ctx, &bucketv1.CompleteMultipartRequest{
				Bucket: granted, Key: reserved, UploadId: "u",
				Parts: []*bucketv1.CompletedPart{{PartNumber: 1, Etag: "e"}},
			})
			return err
		},
		"abandon a multipart upload": func(h *harness) error {
			_, err := h.svc.AbortMultipart(ctx, &bucketv1.AbortMultipartRequest{Bucket: granted, Key: reserved, UploadId: "u"})
			return err
		},
		"presign an upload": func(h *harness) error {
			_, err := h.svc.PresignUpload(ctx, &bucketv1.PresignUploadRequest{
				Bucket: granted,
				Files:  []*bucketv1.PresignFile{{Key: reserved, Name: "s", Size: 3, MimeType: "application/json"}},
			})
			return err
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			denied(t, name+" of "+reserved, reach(neighbours(t)))
		})
	}
}
