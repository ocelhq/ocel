package bucket_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"google.golang.org/protobuf/encoding/protojson"

	"github.com/ocelhq/ocel/cli/internal/declare"
	"github.com/ocelhq/ocel/cli/internal/devstack/bucket"
	"github.com/ocelhq/ocel/cli/internal/devstack/docker"
	bucketv1 "github.com/ocelhq/ocel/pkg/proto/app/bucket/v1"
	bindingsv1 "github.com/ocelhq/ocel/pkg/proto/common/bindings/v1"
)

const liveEnv = "OCEL_LIVE_DOCKER"

type liveBucket struct {
	component *bucket.Component
	name      string
	endpoint  string
	callbacks chan map[string]any
	reported  chan error
	app       *httptest.Server
	origins   *[]string
}

func startLive(t *testing.T, project string, answer int) liveBucket {
	t.Helper()
	if os.Getenv(liveEnv) == "" {
		t.Skipf("no docker daemon promised to this run; set %s=1 where one is running", liveEnv)
	}
	ctx := context.Background()

	callbacks := make(chan map[string]any, 16)
	reported := make(chan error, 16)
	app := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		body["op"] = r.URL.Query().Get("op")
		callbacks <- body
		w.WriteHeader(answer)
	}))
	t.Cleanup(app.Close)

	origins := []string{app.URL}
	component := bucket.New(docker.Open, func() []string { return origins }, func(err error) { reported <- err })
	t.Cleanup(func() {
		_ = component.Close(ctx, true)
		engine, err := docker.Open(ctx)
		if err != nil {
			return
		}
		_ = engine.Wipe(ctx, docker.ProjectLabels(project))
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
	_, endpoint, _ := strings.Cut(resolved[0].Origin, " @ ")
	return liveBucket{component: component, name: bound.GetBucket().GetBucket(), endpoint: "http://" + endpoint, callbacks: callbacks, reported: reported, app: app, origins: &origins}
}

func put(t *testing.T, target *bucketv1.PresignedTarget, contentType, body string) *http.Response {
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

func TestDockerAnUploadCompletesThroughTheDeployedBucketService(t *testing.T) {
	live := startLive(t, "bucket-live-test", http.StatusOK)
	ctx := context.Background()

	presigned, err := live.component.PresignUpload(ctx, &bucketv1.PresignUploadRequest{
		Bucket:          live.name,
		CallbackBaseUrl: live.app.URL + "/api/upload",
		Files:           []*bucketv1.PresignFile{{Key: "a.txt", Name: "a.txt", Size: 5, MimeType: "text/plain"}},
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
		verified, err := live.component.VerifyUploadSignature(ctx, &bucketv1.VerifyUploadSignatureRequest{
			SessionId: presigned.GetSessionId(),
			Signature: callback["signature"].(string),
			File:      &bucketv1.CompletedFile{Key: file["key"].(string), Name: file["name"].(string), Size: int64(file["size"].(float64)), MimeType: file["mimeType"].(string)},
		})
		if err != nil || !verified.GetValid() {
			t.Fatalf("VerifyUploadSignature = %v, %v, want the callback's signature to hold", verified, err)
		}
	case <-time.After(time.Minute):
		t.Fatal("the app was never told the upload finished")
	}

	status, err := live.component.GetUploadStatus(ctx, &bucketv1.GetUploadStatusRequest{SessionId: presigned.GetSessionId()})
	if err != nil {
		t.Fatalf("GetUploadStatus = %v", err)
	}
	if status.GetState() != bucketv1.UploadState_UPLOAD_STATE_SUCCEEDED {
		t.Fatalf("state = %v, want succeeded", status.GetState())
	}
}

func TestAnUploadThatBreaksItsSignedConditionsIsRefused(t *testing.T) {
	t.Skip("TODO(#1203): ghcr.io/ocelhq/floci:2.0.1-ocel.2 verifies no query signature, so a presigned PUT with another content type or length is stored; unskip once the fork enforces SigV4 presigned requests")

	live := startLive(t, "bucket-live-conditions-test", http.StatusOK)
	presigned, err := live.component.PresignUpload(context.Background(), &bucketv1.PresignUploadRequest{
		Bucket:          live.name,
		CallbackBaseUrl: live.app.URL + "/api/upload",
		Files: []*bucketv1.PresignFile{
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

func TestDockerACompletionTheAppKeepsRefusingIsReportedOnceAndDropped(t *testing.T) {
	live := startLive(t, "bucket-live-poison-test", http.StatusInternalServerError)

	presigned, err := live.component.PresignUpload(context.Background(), &bucketv1.PresignUploadRequest{
		Bucket:          live.name,
		CallbackBaseUrl: live.app.URL + "/api/upload",
		Files:           []*bucketv1.PresignFile{{Key: "a.txt", Name: "a.txt", Size: 5, MimeType: "text/plain"}},
	})
	if err != nil {
		t.Fatalf("PresignUpload = %v", err)
	}
	if resp := put(t, presigned.GetFiles()[0], "text/plain", "hello"); resp.StatusCode != http.StatusOK {
		t.Fatalf("PUT to the presigned url answered %s", resp.Status)
	}

	select {
	case err := <-live.reported:
		if !strings.Contains(err.Error(), "3 attempts") {
			t.Fatalf("reported %v, want the completion given up on after 3 attempts", err)
		}
	case <-time.After(time.Minute):
		t.Fatal("a completion that keeps failing was never reported")
	}

	time.Sleep(12 * time.Second)
	if attempts := len(live.callbacks); attempts != 3 {
		t.Errorf("the app was called %d times, want 3 and then no more", attempts)
	}
	if again := len(live.reported); again != 0 {
		t.Errorf("the failure was reported %d more times, want it said once", again)
	}
}

func TestDockerTheAppsOriginIsReadAgainOnEverySync(t *testing.T) {
	live := startLive(t, "bucket-live-origins-test", http.StatusOK)

	allowed := func(origin string) string {
		req, err := http.NewRequest(http.MethodOptions, live.endpoint+"/"+live.name+"/a.txt", nil)
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Origin", origin)
		req.Header.Set("Access-Control-Request-Method", http.MethodPut)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("preflight = %v", err)
		}
		_ = resp.Body.Close()
		return resp.Header.Get("Access-Control-Allow-Origin")
	}

	const moved = "http://localhost:4100"
	if got := allowed(moved); got == moved {
		t.Fatalf("%s may upload before the app ever ran there", moved)
	}
	*live.origins = []string{moved}
	if _, err := live.component.Resolve(context.Background(), "bucket-live-origins-test", []declare.Resource{declared("User Uploads")}); err != nil {
		t.Fatalf("Resolve = %v", err)
	}
	if got := allowed(moved); got != moved {
		t.Fatalf("Access-Control-Allow-Origin = %q for %s after the app moved there", got, moved)
	}
}
