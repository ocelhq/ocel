package bucket

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	connect "connectrpc.com/connect"
	"google.golang.org/protobuf/encoding/protojson"

	bucketv1 "github.com/ocelhq/ocel/pkg/proto/app/bucket/v1"
	"github.com/ocelhq/ocel/pkg/proto/app/bucket/v1/bucketv1connect"
	bindingsv1 "github.com/ocelhq/ocel/pkg/proto/common/bindings/v1"
	"github.com/ocelhq/ocel/pkg/runtime/live"
	s3store "github.com/ocelhq/ocel/platform/s3"
)

const (
	appAccount = "ocel-production@acme-prod.iam.gserviceaccount.com"
	uploads    = "ocel-shop-prod-uploads-0dd909b8"
	avatars    = "ocel-shop-prod-avatars-1a2b3c4d"
)

type records struct {
	values   map[string]string
	bindings []live.Binding
}

func (r records) Value(key string) string  { return r.values[key] }
func (r records) Bindings() []live.Binding { return r.bindings }
func (r records) Generation() uint32       { return 1 }
func (r *records) bind(t *testing.T, name string, properties *bindingsv1.BucketProperties) {
	t.Helper()
	encoded, err := protojson.Marshal(&bindingsv1.Binding{Name: name, Properties: &bindingsv1.Binding_Bucket{Bucket: properties}})
	if err != nil {
		t.Fatal(err)
	}
	key := "OCEL_BUCKET_" + strings.ToUpper(name)
	r.values[key] = string(encoded)
	r.bindings = append(r.bindings, live.Binding{Name: name, Key: key, Type: bindingsv1.BindingType_BINDING_TYPE_BUCKET})
}

type poster struct {
	mu    sync.Mutex
	posts []string
}

func (p *poster) Post(_ context.Context, target string, _ []byte) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.posts = append(p.posts, target)
	return nil
}

type harness struct {
	server   *storageServer
	key      *rsa.PrivateKey
	callback *poster
	handler  bucketv1connect.BucketServiceHandler
}

func serving(t *testing.T) *harness {
	t.Helper()
	server, endpoint := startStorage(t)
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	bound := &records{values: map[string]string{}}
	bound.bind(t, "uploads", &bindingsv1.BucketProperties{Bucket: uploads})
	bound.bind(t, "avatars", &bindingsv1.BucketProperties{Bucket: avatars})
	callback := &poster{}
	store := Store{
		Endpoint: endpoint,
		Client:   http.DefaultClient,
		Account:  func(context.Context) (string, error) { return appAccount, nil },
		SignBlob: func(_ context.Context, account string, payload []byte) ([]byte, error) {
			if account != appAccount {
				t.Errorf("signed as %q, want the account the app runs as", account)
			}
			digest := sha256.Sum256(payload)
			return rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, digest[:])
		},
	}
	handler, err := NewDispatch(context.Background(), store, bound, callback)
	if err != nil {
		t.Fatalf("NewDispatch() = %v", err)
	}
	return &harness{server: server, key: key, callback: callback, handler: handler}
}

func TestHeadReadsWhatCloudStorageKeepsAboutAnObject(t *testing.T) {
	h := serving(t)
	stored := h.server.put(uploads, "avatars/ada.png", []byte("png!"), "image/png")
	stored.metadata = map[string]string{"owner": "ada"}

	resp, err := h.handler.Head(context.Background(), &bucketv1.HeadRequest{Bucket: uploads, Key: "avatars/ada.png"})
	if err != nil {
		t.Fatalf("Head() = %v", err)
	}
	object := resp.GetObject()
	if object.GetSize() != 4 || object.GetContentType() != "image/png" || object.GetMetadata()["owner"] != "ada" {
		t.Errorf("Head() = %v, want the object's size, type and metadata", object)
	}
	if !object.GetUploadedAt().AsTime().Equal(stored.created) {
		t.Errorf("Head() says uploaded at %v, want %v: the time this generation's bytes were written", object.GetUploadedAt().AsTime(), stored.created)
	}
	if object.GetEtag() == "" {
		t.Error("Head() returned no etag, and a conditional write names one")
	}

	absent, err := h.handler.Head(context.Background(), &bucketv1.HeadRequest{Bucket: uploads, Key: "missing.png"})
	if err != nil || absent.GetObject() != nil {
		t.Errorf("Head() of a missing object = %v, %v, want no object and no error", absent, err)
	}
}

func TestListPagesThroughTheAppsObjectsAndHidesTheRuntimesOwn(t *testing.T) {
	h := serving(t)
	for _, key := range []string{"a.png", "b.png", "c.png", s3store.SessionKeyPrefix + "sess_1"} {
		h.server.put(uploads, key, []byte("x"), "image/png")
	}

	var keys []string
	cursor, pages := "", 0
	for {
		page, err := h.handler.List(context.Background(), &bucketv1.ListRequest{Bucket: uploads, Limit: 2, Cursor: cursor})
		if err != nil {
			t.Fatalf("List(cursor %q) = %v", cursor, err)
		}
		pages++
		for _, object := range page.GetObjects() {
			keys = append(keys, object.GetKey())
		}
		if cursor = page.GetNextCursor(); cursor == "" || pages == 5 {
			break
		}
	}
	if pages < 2 {
		t.Errorf("List(limit 2) over four objects answered in %d page, want a cursor to the rest", pages)
	}
	if strings.Join(keys, ",") != "a.png,b.png,c.png" {
		t.Errorf("List() across pages = %v, want every object the app wrote and none of the runtime's sessions", keys)
	}
}

func TestDeleteAndCopyActOnTheBucketTheyName(t *testing.T) {
	h := serving(t)
	h.server.put(uploads, "a.png", []byte("ada"), "image/png")
	h.server.put(uploads, "b.png", []byte("bob"), "image/png")

	copied, err := h.handler.Copy(context.Background(), &bucketv1.CopyRequest{Bucket: uploads, SourceKey: "a.png", DestinationKey: "copies/a.png"})
	if err != nil {
		t.Fatalf("Copy() = %v", err)
	}
	if copied.GetObject().GetSize() != 3 || h.server.object(uploads, "copies/a.png") == nil {
		t.Errorf("Copy() = %v, want the copy written beside the source", copied)
	}
	if _, err := h.handler.Delete(context.Background(), &bucketv1.DeleteRequest{Bucket: uploads, Keys: []string{"a.png", "b.png", "gone.png"}}); err != nil {
		t.Fatalf("Delete() = %v", err)
	}
	if h.server.object(uploads, "a.png") != nil || h.server.object(uploads, "b.png") != nil {
		t.Error("Delete() left objects it named in place")
	}
}

func TestABucketTheAppWasNotBoundToIsRefused(t *testing.T) {
	h := serving(t)

	_, err := h.handler.Head(context.Background(), &bucketv1.HeadRequest{Bucket: "ocel-other-prod-uploads-ffffffff", Key: "a.png"})
	if connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("Head() of another project's bucket = %v, want permission denied", err)
	}
}

func decodedPolicy(t *testing.T, fields map[string]string) map[string]any {
	t.Helper()
	raw, err := base64.StdEncoding.DecodeString(fields["policy"])
	if err != nil {
		t.Fatalf("the policy field %q is not base64: %v", fields["policy"], err)
	}
	var policy map[string]any
	if err := json.Unmarshal(raw, &policy); err != nil {
		t.Fatalf("the policy %s is not JSON: %v", raw, err)
	}
	return policy
}

func TestAnUploadSessionSignsABrowserFormBoundToTheSizeAndTypeItDeclared(t *testing.T) {
	h := serving(t)

	resp, err := h.handler.PresignUpload(context.Background(), &bucketv1.PresignUploadRequest{
		Bucket:          avatars,
		CallbackBaseUrl: "http://127.0.0.1:3000/api/upload",
		Files:           []*bucketv1.PresignFile{{Key: "ada.png", Name: "ada.png", Size: 4, MimeType: "image/png"}},
	})
	if err != nil {
		t.Fatalf("PresignUpload() = %v", err)
	}
	target := resp.GetFiles()[0]
	if target.GetMethod() != http.MethodPost || !strings.HasSuffix(target.GetUrl(), "/"+avatars+"/") {
		t.Errorf("the upload target is %s %s, want a POST form to the bucket", target.GetMethod(), target.GetUrl())
	}
	fields := target.GetFields()
	if fields["x-goog-algorithm"] != "GOOG4-RSA-SHA256" || !strings.HasPrefix(fields["x-goog-credential"], appAccount+"/") || fields["x-goog-signature"] == "" {
		t.Errorf("the form is signed with %v, want a V4 signature by the app's own account", fields)
	}
	if fields["key"] != "ada.png" || fields["Content-Type"] != "image/png" {
		t.Errorf("the form fields are %v, want the key and the declared type", fields)
	}
	policy, _ := json.Marshal(decodedPolicy(t, fields)["conditions"])
	if !strings.Contains(string(policy), `["content-length-range",4,4]`) {
		t.Errorf("the policy's conditions are %s, want the body held to the 4 bytes declared", policy)
	}
	if session := h.server.object(avatars, s3store.SessionKeyPrefix+resp.GetSessionId()); session == nil {
		t.Errorf("the session %s is not kept in the bucket it guards", resp.GetSessionId())
	}

	h.server.put(avatars, "ada.png", []byte("png!"), "image/png")
	completed, err := h.handler.CompleteUpload(context.Background(), &bucketv1.CompleteUploadRequest{SessionId: resp.GetSessionId()})
	if err != nil {
		t.Fatalf("CompleteUpload() = %v", err)
	}
	if completed.GetState() != bucketv1.UploadState_UPLOAD_STATE_SUCCEEDED || len(h.callback.posts) != 1 {
		t.Errorf("CompleteUpload() = %v with %d callbacks, want the upload confirmed and the app called back once", completed, len(h.callback.posts))
	}
	for _, write := range h.server.writes {
		if write.Get("ifGenerationMatch") == "" {
			t.Errorf("a session was written with %v, want every write conditioned on the generation it read: two replicas complete one session", write)
		}
	}
}

func signedQuery(t *testing.T, target string) url.Values {
	t.Helper()
	parsed, err := url.Parse(target)
	if err != nil {
		t.Fatal(err)
	}
	return parsed.Query()
}

func TestASignedReadAndWriteCarryWhatTheAppConstrained(t *testing.T) {
	h := serving(t)

	read, err := h.handler.Sign(context.Background(), &bucketv1.SignRequest{
		Bucket: uploads, Key: "report.pdf", Operation: bucketv1.SignedOperation_SIGNED_OPERATION_GET,
		Constraints: &bucketv1.SignConstraints{DownloadFilename: "report.pdf"},
	})
	if err != nil {
		t.Fatalf("Sign(GET) = %v", err)
	}
	query := signedQuery(t, read.GetTarget().GetUrl())
	if query.Get("X-Goog-Algorithm") != "GOOG4-RSA-SHA256" || !strings.HasPrefix(query.Get("X-Goog-Credential"), appAccount+"/") {
		t.Errorf("the read is signed with %v, want a V4 signature by the app's own account", query)
	}
	if !strings.Contains(query.Get("response-content-disposition"), `filename=report.pdf`) {
		t.Errorf("the read asks for disposition %q, want it downloaded as report.pdf", query.Get("response-content-disposition"))
	}

	write, err := h.handler.Sign(context.Background(), &bucketv1.SignRequest{
		Bucket: uploads, Key: "report.pdf", Operation: bucketv1.SignedOperation_SIGNED_OPERATION_PUT,
		Constraints: &bucketv1.SignConstraints{ContentType: "application/pdf", IfNoneMatch: "*", Metadata: map[string]string{"owner": "ada"}},
	})
	if err != nil {
		t.Fatalf("Sign(PUT) = %v", err)
	}
	headers := write.GetTarget().GetHeaders()
	if write.GetTarget().GetMethod() != http.MethodPut || headers["content-type"] != "application/pdf" ||
		headers["x-goog-if-generation-match"] != "0" || headers["x-goog-meta-owner"] != "ada" {
		t.Errorf("the write is %s with headers %v, want a PUT whose signature holds the type, the metadata and create-only", write.GetTarget().GetMethod(), headers)
	}
	signed := signedQuery(t, write.GetTarget().GetUrl()).Get("X-Goog-SignedHeaders")
	for _, header := range []string{"content-type", "x-goog-if-generation-match", "x-goog-meta-owner"} {
		if !strings.Contains(signed, header) {
			t.Errorf("the write signs headers %q, want %s among them, or a client could drop it", signed, header)
		}
	}
}

func TestAWriteConditionedOnAnEtagIsConditionedOnTheGenerationItNames(t *testing.T) {
	h := serving(t)
	h.server.put(uploads, "doc.txt", []byte("v1"), "text/plain")
	head, err := h.handler.Head(context.Background(), &bucketv1.HeadRequest{Bucket: uploads, Key: "doc.txt"})
	if err != nil {
		t.Fatal(err)
	}

	write, err := h.handler.Sign(context.Background(), &bucketv1.SignRequest{
		Bucket: uploads, Key: "doc.txt", Operation: bucketv1.SignedOperation_SIGNED_OPERATION_PUT,
		Constraints: &bucketv1.SignConstraints{IfMatch: head.GetObject().GetEtag()},
	})
	if err != nil {
		t.Fatalf("Sign(PUT if-match) = %v", err)
	}
	generation := h.server.object(uploads, "doc.txt").generation
	if got := write.GetTarget().GetHeaders()["x-goog-if-generation-match"]; got != formatGeneration(generation) {
		t.Errorf("the write is conditioned on generation %q, want %d: the etag Head returned names it", got, generation)
	}
}

func TestAMultipartUploadIsOpenedSignedPartByPartAndAssembled(t *testing.T) {
	h := serving(t)

	opened, err := h.handler.CreateMultipart(context.Background(), &bucketv1.CreateMultipartRequest{
		Bucket: uploads, Key: "video.mp4", ContentType: "video/mp4", Metadata: map[string]string{"owner": "ada"},
	})
	if err != nil {
		t.Fatalf("CreateMultipart() = %v", err)
	}
	parts, err := h.handler.SignParts(context.Background(), &bucketv1.SignPartsRequest{
		Bucket: uploads, Key: "video.mp4", UploadId: opened.GetUploadId(), PartNumbers: []int32{1, 2},
	})
	if err != nil {
		t.Fatalf("SignParts() = %v", err)
	}
	if len(parts.GetParts()) != 2 {
		t.Fatalf("SignParts() = %v, want a URL for each part", parts)
	}
	query := signedQuery(t, parts.GetParts()[1].GetUrl())
	if query.Get("partNumber") != "2" || query.Get("uploadId") != opened.GetUploadId() || query.Get("X-Goog-Signature") == "" {
		t.Errorf("part 2 is signed as %v, want its number and the upload under the signature", query)
	}

	done, err := h.handler.CompleteMultipart(context.Background(), &bucketv1.CompleteMultipartRequest{
		Bucket: uploads, Key: "video.mp4", UploadId: opened.GetUploadId(),
		Parts: []*bucketv1.CompletedPart{{PartNumber: 1, Etag: `"a"`}, {PartNumber: 2, Etag: `"b"`}},
	})
	if err != nil {
		t.Fatalf("CompleteMultipart() = %v", err)
	}
	if done.GetObject().GetContentType() != "video/mp4" || done.GetObject().GetMetadata()["owner"] != "ada" {
		t.Errorf("CompleteMultipart() = %v, want the assembled object with the type and metadata it was opened with", done.GetObject())
	}

	again, err := h.handler.CreateMultipart(context.Background(), &bucketv1.CreateMultipartRequest{Bucket: uploads, Key: "drop.mp4"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.handler.AbortMultipart(context.Background(), &bucketv1.AbortMultipartRequest{Bucket: uploads, Key: "drop.mp4", UploadId: again.GetUploadId()}); err != nil {
		t.Fatalf("AbortMultipart() = %v", err)
	}
	if len(h.server.aborted) != 1 {
		t.Errorf("Cloud Storage aborted %v, want the abandoned upload", h.server.aborted)
	}
}

func TestAnAssemblyThatWouldOverwriteWhatCreateOnlyForbadeIsRefused(t *testing.T) {
	h := serving(t)
	h.server.put(uploads, "video.mp4", []byte("already"), "video/mp4")
	opened, err := h.handler.CreateMultipart(context.Background(), &bucketv1.CreateMultipartRequest{Bucket: uploads, Key: "video.mp4"})
	if err != nil {
		t.Fatal(err)
	}

	_, err = h.handler.CompleteMultipart(context.Background(), &bucketv1.CompleteMultipartRequest{
		Bucket: uploads, Key: "video.mp4", UploadId: opened.GetUploadId(), IfNoneMatch: "*",
		Parts: []*bucketv1.CompletedPart{{PartNumber: 1, Etag: `"a"`}},
	})
	if connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Fatalf("CompleteMultipart(if-none-match *) over an existing object = %v, want failed precondition", err)
	}
}

func TestASignatureTheAppGaveNoLifetimeExpiresInAnHour(t *testing.T) {
	h := serving(t)

	resp, err := h.handler.Sign(context.Background(), &bucketv1.SignRequest{
		Bucket: uploads, Key: "a.png", Operation: bucketv1.SignedOperation_SIGNED_OPERATION_GET,
	})
	if err != nil {
		t.Fatal(err)
	}
	expires, _ := strconv.Atoi(signedQuery(t, resp.GetTarget().GetUrl()).Get("X-Goog-Expires"))
	if expires < int((time.Hour-time.Minute).Seconds()) || expires > int(time.Hour.Seconds()) {
		t.Errorf("a read signed with no lifetime expires in %d seconds, want an hour", expires)
	}
}

func TestASignatureAskedForOffGoogleCloudSaysOnlyTheDeployedAppCanSign(t *testing.T) {
	metadataServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "no such host", http.StatusNotFound)
	}))
	t.Cleanup(metadataServer.Close)
	t.Setenv("GCE_METADATA_HOST", strings.TrimPrefix(metadataServer.URL, "http://"))

	store, err := Open(context.Background(), "http://storage.invalid")
	if err != nil {
		t.Fatalf("Open() = %v", err)
	}
	_, err = store.Account(context.Background())

	if err == nil || !strings.Contains(err.Error(), "a build cannot sign a bucket URL") {
		t.Errorf("Account() off Google Cloud = %v, want it to say a build cannot sign a bucket URL", err)
	}
}

func TestTheAccountAnAppSignsAsIsAskedAgainAfterTheMetadataServerFailedAndKeptOnceKnown(t *testing.T) {
	var asked atomic.Int32
	metadataServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if asked.Add(1) == 1 {
			http.Error(w, "not yet", http.StatusNotFound)
			return
		}
		w.Header().Set("Metadata-Flavor", "Google")
		_, _ = w.Write([]byte(appAccount))
	}))
	t.Cleanup(metadataServer.Close)
	t.Setenv("GCE_METADATA_HOST", strings.TrimPrefix(metadataServer.URL, "http://"))

	store, err := Open(context.Background(), "http://storage.invalid")
	if err != nil {
		t.Fatalf("Open() = %v", err)
	}
	if _, err := store.Account(context.Background()); err == nil {
		t.Fatalf("Account() = nil error while the metadata server refused, want its failure")
	}
	for range 2 {
		account, err := store.Account(context.Background())
		if err != nil || account != appAccount {
			t.Fatalf("Account() = %q, %v after the metadata server recovered, want %q: a failed lookup was kept and every signature after it fails", account, err, appAccount)
		}
	}
	if got := asked.Load(); got != 2 {
		t.Errorf("the metadata server was asked %d times, want 2: once for the failure, once for the account, which is then kept", got)
	}
}
