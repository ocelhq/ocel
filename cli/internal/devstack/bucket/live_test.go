package bucket_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"google.golang.org/protobuf/encoding/protojson"

	"github.com/ocelhq/ocel/cli/internal/declare"
	"github.com/ocelhq/ocel/cli/internal/devstack/bucket"
	"github.com/ocelhq/ocel/cli/internal/devstack/docker"
	blobv1 "github.com/ocelhq/ocel/pkg/proto/app/blob/v1"
	bindingsv1 "github.com/ocelhq/ocel/pkg/proto/common/bindings/v1"
)

const liveEnv = "OCEL_LIVE_DOCKER"

type liveBucket struct {
	component *bucket.Component
	name      string
	callbacks chan map[string]any
	app       *httptest.Server
}

func startLive(t *testing.T, project string) liveBucket {
	t.Helper()
	if os.Getenv(liveEnv) == "" {
		t.Skipf("no docker daemon promised to this run; set %s=1 where one is running", liveEnv)
	}
	ctx := context.Background()

	callbacks := make(chan map[string]any, 4)
	app := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		body["op"] = r.URL.Query().Get("op")
		callbacks <- body
	}))
	t.Cleanup(app.Close)

	component := bucket.New(docker.Open, []string{app.URL}, func(err error) { t.Logf("reported: %v", err) })
	t.Cleanup(func() {
		_ = component.Close(ctx)
		engine, err := docker.Open(ctx)
		if err != nil {
			return
		}
		_ = engine.RemoveVolumes(ctx, docker.ProjectLabels(project))
		_ = engine.Close()
	})

	resolved, err := component.Resolve(ctx, project, []declare.Resource{declared("User Uploads")})
	if err != nil {
		t.Fatalf("Resolve = %v", err)
	}
	var bound bindingsv1.Binding
	if err := protojson.Unmarshal([]byte(resolved[0].Env["OCEL_RESOURCE_BUCKET_User Uploads"]), &bound); err != nil {
		t.Fatalf("the binding is not the JSON the SDKs read: %v (%v)", err, resolved[0].Env)
	}
	return liveBucket{component: component, name: bound.GetBucket().GetBucket(), callbacks: callbacks, app: app}
}

func put(t *testing.T, target *blobv1.PresignedTarget, contentType, body string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(http.MethodPut, target.GetUrl(), bytes.NewReader([]byte(body)))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", contentType)
	for name, value := range target.GetHeaders() {
		req.Header.Set(name, value)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("PUT = %v", err)
	}
	t.Cleanup(func() { _ = resp.Body.Close() })
	return resp
}

func TestLiveAnUploadCompletesThroughTheDeployedBucketService(t *testing.T) {
	live := startLive(t, "bucket-live-test")
	ctx := context.Background()

	presigned, err := live.component.PresignUpload(ctx, &blobv1.PresignUploadRequest{
		Bucket:          live.name,
		CallbackBaseUrl: live.app.URL + "/api/upload",
		Files:           []*blobv1.PresignFile{{Key: "a.txt", Name: "a.txt", Size: 5, MimeType: "text/plain"}},
	})
	if err != nil {
		t.Fatalf("PresignUpload = %v", err)
	}

	resp := put(t, presigned.GetFiles()[0], "text/plain", "hello")
	if resp.StatusCode != http.StatusOK {
		said, _ := io.ReadAll(resp.Body)
		t.Fatalf("PUT to the presigned url answered %s: %s", resp.Status, said)
	}

	select {
	case callback := <-live.callbacks:
		if callback["op"] != "callback" || callback["sessionId"] != presigned.GetSessionId() {
			t.Fatalf("callback = %v, want the completion of session %s", callback, presigned.GetSessionId())
		}
		file, _ := callback["file"].(map[string]any)
		verified, err := live.component.VerifyUploadSignature(ctx, &blobv1.VerifyUploadSignatureRequest{
			SessionId: presigned.GetSessionId(),
			Signature: callback["signature"].(string),
			File:      &blobv1.CompletedFile{Key: file["key"].(string), Name: file["name"].(string), Size: int64(file["size"].(float64)), MimeType: file["mimeType"].(string)},
		})
		if err != nil || !verified.GetValid() {
			t.Fatalf("VerifyUploadSignature = %v, %v, want the callback's signature to hold", verified, err)
		}
	case <-time.After(time.Minute):
		t.Fatal("the app was never told the upload finished")
	}

	status, err := live.component.GetUploadStatus(ctx, &blobv1.GetUploadStatusRequest{SessionId: presigned.GetSessionId()})
	if err != nil {
		t.Fatalf("GetUploadStatus = %v", err)
	}
	if status.GetState() != blobv1.UploadState_UPLOAD_STATE_SUCCEEDED {
		t.Fatalf("state = %v, want succeeded", status.GetState())
	}
}

func TestAnUploadThatBreaksItsSignedConditionsIsRefused(t *testing.T) {
	t.Skip("TODO(#1203): ghcr.io/ocelhq/floci:2.0.1-ocel.2 verifies no query signature, so a presigned PUT with another content type or length is stored; unskip once the fork enforces SigV4 presigned requests")

	live := startLive(t, "bucket-live-conditions-test")
	presigned, err := live.component.PresignUpload(context.Background(), &blobv1.PresignUploadRequest{
		Bucket:          live.name,
		CallbackBaseUrl: live.app.URL + "/api/upload",
		Files: []*blobv1.PresignFile{
			{Key: "type.txt", Name: "type.txt", Size: 5, MimeType: "text/plain"},
			{Key: "size.txt", Name: "size.txt", Size: 5, MimeType: "text/plain"},
		},
	})
	if err != nil {
		t.Fatalf("PresignUpload = %v", err)
	}
	if resp := put(t, presigned.GetFiles()[0], "image/png", "hello"); resp.StatusCode != http.StatusForbidden {
		t.Errorf("a PUT under another content type answered %s, want 403", resp.Status)
	}
	if resp := put(t, presigned.GetFiles()[1], "text/plain", "hello, this is far more than five bytes"); resp.StatusCode != http.StatusForbidden {
		t.Errorf("a PUT of another length answered %s, want 403", resp.Status)
	}
}
