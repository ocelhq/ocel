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
	"sync/atomic"
	"testing"

	"google.golang.org/protobuf/encoding/protojson"

	"github.com/ocelhq/ocel/cli/internal/declaration"
	"github.com/ocelhq/ocel/cli/internal/devresources/bucket"
	"github.com/ocelhq/ocel/cli/internal/devresources/docker"
	bucketv1 "github.com/ocelhq/ocel/pkg/proto/app/bucket/v1"
	bindingsv1 "github.com/ocelhq/ocel/pkg/proto/common/bindings/v1"
)

const liveEnv = "OCEL_LIVE_DOCKER"

type liveBucket struct {
	backend   *bucket.Backend
	project   string
	name      string
	endpoint  string
	callbacks chan map[string]any
	app       *httptest.Server
	origins   *[]string
}

func startLive(t *testing.T, project string) liveBucket {
	t.Helper()
	if os.Getenv(liveEnv) == "" {
		t.Skipf("no docker daemon promised to this run; set %s=1 where one is running", liveEnv)
	}
	ctx := context.Background()

	callbacks := make(chan map[string]any, 16)
	app := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		body["op"] = r.URL.Query().Get("op")
		callbacks <- body
	}))
	t.Cleanup(app.Close)

	origins := []string{app.URL}
	backend := bucket.New(docker.Open, t.TempDir(), func() []string { return origins })
	t.Cleanup(func() {
		_ = backend.Close(ctx, true)
		engine, err := docker.Open(ctx)
		if err != nil {
			return
		}
		_ = engine.Wipe(ctx, docker.ProjectLabels(project))
		_ = engine.Close()
	})

	resolved, err := backend.Resolve(ctx, project, []declaration.Resource{declared("User Uploads")})
	if err != nil {
		t.Fatalf("Resolve = %v", err)
	}
	var bound bindingsv1.Binding
	if err := protojson.Unmarshal([]byte(resolved[0].Env["OCEL_RESOURCE_BUCKET_User Uploads"]), &bound); err != nil {
		t.Fatalf("the binding is not the JSON the SDKs read: %v (%v)", err, resolved[0].Env)
	}
	_, endpoint, _ := strings.Cut(resolved[0].Origin, " @ ")
	return liveBucket{backend: backend, project: project, name: bound.GetBucket().GetBucket(), endpoint: "http://" + endpoint, callbacks: callbacks, app: app, origins: &origins}
}

func send(t *testing.T, method, url string, headers map[string]string, body string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(method, url, bytes.NewReader([]byte(body)))
	if err != nil {
		t.Fatal(err)
	}
	for name, value := range headers {
		req.Header.Set(name, value)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("%s = %v", method, err)
	}
	t.Cleanup(func() { _ = resp.Body.Close() })
	return resp
}

func put(t *testing.T, target *bucketv1.PresignedTarget, contentType, body string) *http.Response {
	t.Helper()
	headers := map[string]string{}
	for name, value := range target.GetHeaders() {
		headers[http.CanonicalHeaderKey(name)] = value
	}
	headers["Content-Type"] = contentType
	return send(t, http.MethodPut, target.GetUrl(), headers, body)
}

func (l liveBucket) presign(t *testing.T, callbackBase string, files ...*bucketv1.PresignFile) *bucketv1.PresignUploadResponse {
	t.Helper()
	presigned, err := l.backend.PresignUpload(context.Background(), &bucketv1.PresignUploadRequest{
		Bucket:          l.name,
		CallbackBaseUrl: callbackBase,
		Files:           files,
	})
	if err != nil {
		t.Fatalf("PresignUpload = %v", err)
	}
	return presigned
}

func TestDockerAnUploadThatBreaksItsSignedConditionsIsRefused(t *testing.T) {
	live := startLive(t, "bucket-live-conditions-test")
	presigned := live.presign(t, live.app.URL+"/api/upload",
		&bucketv1.PresignFile{Key: "type.txt", Name: "type.txt", Size: 5, MimeType: "text/plain"},
		&bucketv1.PresignFile{Key: "size.txt", Name: "size.txt", Size: 5, MimeType: "text/plain"},
	)
	if resp := put(t, presigned.GetFiles()[0], "image/png", "hello"); resp.StatusCode != http.StatusForbidden {
		t.Errorf("a PUT under another content type answered %s, want 403", resp.Status)
	}
	if resp := put(t, presigned.GetFiles()[1], "text/plain", "hello, this is far more than five bytes"); resp.StatusCode != http.StatusForbidden {
		t.Errorf("a PUT of another length answered %s, want 403", resp.Status)
	}
}

func TestDockerACompletedUploadCallsTheAppBackWithTheSignedFile(t *testing.T) {
	live := startLive(t, "bucket-live-complete-test")
	presigned := live.presign(t, live.app.URL+"/api/upload",
		&bucketv1.PresignFile{Key: "a.txt", Name: "a.txt", Size: 5, MimeType: "text/plain"})
	if resp := put(t, presigned.GetFiles()[0], "text/plain", "hello"); resp.StatusCode != http.StatusOK {
		t.Fatalf("PUT to the presigned url answered %s", resp.Status)
	}

	completed, err := live.backend.CompleteUpload(context.Background(), &bucketv1.CompleteUploadRequest{SessionId: presigned.GetSessionId()})
	if err != nil {
		t.Fatalf("CompleteUpload = %v", err)
	}
	if completed.GetState() != bucketv1.UploadState_UPLOAD_STATE_SUCCEEDED {
		t.Fatalf("CompleteUpload state = %v, want succeeded", completed.GetState())
	}
	select {
	case called := <-live.callbacks:
		if called["op"] != "callback" || called["sessionId"] != presigned.GetSessionId() {
			t.Errorf("the app was called with %v, want the callback for the session", called)
		}
	default:
		t.Fatal("a completed upload never called the app back")
	}

	status, err := live.backend.GetUploadStatus(context.Background(), &bucketv1.GetUploadStatusRequest{SessionId: presigned.GetSessionId()})
	if err != nil {
		t.Fatalf("GetUploadStatus = %v", err)
	}
	if status.GetState() != bucketv1.UploadState_UPLOAD_STATE_SUCCEEDED {
		t.Errorf("GetUploadStatus state = %v, want succeeded", status.GetState())
	}
}

func TestDockerAnUploadWhoseCallbackGoesToAnOriginNoBucketAllowsCompletesWithoutPostingIt(t *testing.T) {
	var posts atomic.Int32
	foreign := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { posts.Add(1) }))
	t.Cleanup(foreign.Close)

	live := startLive(t, "bucket-live-foreign-callback-test")
	presigned := live.presign(t, foreign.URL+"/api/upload",
		&bucketv1.PresignFile{Key: "a.txt", Name: "a.txt", Size: 5, MimeType: "text/plain"})
	if resp := put(t, presigned.GetFiles()[0], "text/plain", "hello"); resp.StatusCode != http.StatusOK {
		t.Fatalf("PUT to the presigned url answered %s", resp.Status)
	}

	if _, err := live.backend.CompleteUpload(context.Background(), &bucketv1.CompleteUploadRequest{SessionId: presigned.GetSessionId()}); err != nil {
		t.Fatalf("CompleteUpload = %v, want the upload completed", err)
	}
	if got := posts.Load(); got != 0 {
		t.Fatalf("the origin no bucket allows received %d callbacks, want 0", got)
	}
}

func TestDockerAnObjectSignedForIsListedReadAndDeleted(t *testing.T) {
	live := startLive(t, "bucket-live-objects-test")
	ctx := context.Background()
	sign := func(operation bucketv1.SignedOperation) *bucketv1.PresignedTarget {
		signed, err := live.backend.Sign(ctx, &bucketv1.SignRequest{
			Bucket:      live.name,
			Key:         "notes/hello.txt",
			Operation:   operation,
			Audience:    bucketv1.SignedAudience_SIGNED_AUDIENCE_INTERNAL,
			Constraints: &bucketv1.SignConstraints{ContentType: "text/plain"},
		})
		if err != nil {
			t.Fatalf("Sign %v = %v", operation, err)
		}
		return signed.GetTarget()
	}

	if resp := put(t, sign(bucketv1.SignedOperation_SIGNED_OPERATION_PUT), "text/plain", "hello"); resp.StatusCode != http.StatusOK {
		t.Fatalf("PUT to the signed url answered %s", resp.Status)
	}
	listed, err := live.backend.List(ctx, &bucketv1.ListRequest{Bucket: live.name, Prefix: "notes/"})
	if err != nil {
		t.Fatalf("List = %v", err)
	}
	if len(listed.GetObjects()) != 1 || listed.GetObjects()[0].GetKey() != "notes/hello.txt" {
		t.Fatalf("List = %v, want the one object put", listed.GetObjects())
	}
	got := send(t, http.MethodGet, sign(bucketv1.SignedOperation_SIGNED_OPERATION_GET).GetUrl(), nil, "")
	if body, _ := io.ReadAll(got.Body); string(body) != "hello" {
		t.Errorf("GET of the signed url = %s %q, want the object put", got.Status, body)
	}
	if _, err := live.backend.Delete(ctx, &bucketv1.DeleteRequest{Bucket: live.name, Keys: []string{"notes/hello.txt"}}); err != nil {
		t.Fatalf("Delete = %v", err)
	}
	head, err := live.backend.Head(ctx, &bucketv1.HeadRequest{Bucket: live.name, Key: "notes/hello.txt"})
	if err != nil {
		t.Fatalf("Head = %v", err)
	}
	if head.GetObject() != nil {
		t.Error("Head found the object after it was deleted")
	}
}

func TestDockerTheAppsOriginIsReadAgainOnEverySync(t *testing.T) {
	live := startLive(t, "bucket-live-origins-test")

	allowed := func(origin string) string {
		resp := send(t, http.MethodOptions, live.endpoint+"/"+live.name+"/a.txt", map[string]string{
			"Origin":                        origin,
			"Access-Control-Request-Method": http.MethodPut,
		}, "")
		return resp.Header.Get("Access-Control-Allow-Origin")
	}

	const moved = "http://localhost:4100"
	if got := allowed(moved); got == moved {
		t.Fatalf("%s may upload before the app ever ran there", moved)
	}
	*live.origins = []string{moved}
	if _, err := live.backend.Resolve(context.Background(), live.project, []declaration.Resource{declared("User Uploads")}); err != nil {
		t.Fatalf("Resolve = %v", err)
	}
	if got := allowed(moved); got != moved {
		t.Fatalf("Access-Control-Allow-Origin = %q for %s after the app moved there", got, moved)
	}
}
