package s3

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"google.golang.org/protobuf/encoding/protojson"

	bucketv1 "github.com/ocelhq/ocel/pkg/proto/app/bucket/v1"
	bindingsv1 "github.com/ocelhq/ocel/pkg/proto/common/bindings/v1"
	"github.com/ocelhq/ocel/pkg/runtimekit/live"
)

type heldRecords struct {
	bindings []live.Binding
	values   map[string]string
}

func (h heldRecords) Value(key string) string  { return h.values[key] }
func (h heldRecords) Bindings() []live.Binding { return h.bindings }

func TestBackendsAreBuiltForTheBucketsARecordPointsAtAStore(t *testing.T) {
	held := heldRecords{
		bindings: []live.Binding{
			{Name: "uploads", Key: "OCEL_RESOURCE_BUCKET_uploads", Type: bindingsv1.BindingType_BINDING_TYPE_BUCKET},
			{Name: "avatars", Key: "OCEL_RESOURCE_BUCKET_avatars", Type: bindingsv1.BindingType_BINDING_TYPE_BUCKET},
			{Name: "main", Key: "OCEL_RESOURCE_POSTGRES_main", Type: bindingsv1.BindingType_BINDING_TYPE_POSTGRES},
		},
		values: map[string]string{
			"OCEL_RESOURCE_BUCKET_uploads": `{"name":"ocel:bucket.uploads","bucket":{"bucket":"acme","endpoint":"https://abc.r2.cloudflarestorage.com","region":"auto","accessKeyId":"AKID","secretAccessKey":"secret"}}`,
			"OCEL_RESOURCE_BUCKET_avatars": `{"name":"bucket--avatars","bucket":{"bucket":"shop-prod-avatars"}}`,
			"OCEL_RESOURCE_POSTGRES_main":  `{"name":"db--main","postgres":{"host":"db"}}`,
		},
	}

	built, err := backends(held, &recordingPoster{})
	if err != nil {
		t.Fatalf("backends: %v", err)
	}
	if len(built) != 1 || !built[0].holds("OCEL_RESOURCE_BUCKET_uploads") {
		t.Fatalf("backends = %d, want one serving the uploads binding", len(built))
	}
}

type arriving struct {
	heldRecords
	arrived bool
}

func (a *arriving) Value(key string) string {
	if !a.arrived {
		return ""
	}
	return a.values[key]
}

func TestARouterOverRecordsServesThoseThatArriveAfterItStarted(t *testing.T) {
	records := &arriving{heldRecords: heldRecords{
		bindings: []live.Binding{{Name: "uploads", Key: "OCEL_RESOURCE_BUCKET_uploads", Type: bindingsv1.BindingType_BINDING_TYPE_BUCKET}},
		values: map[string]string{
			"OCEL_RESOURCE_BUCKET_uploads": `{"name":"ocel:bucket.uploads","bucket":{"bucket":"acme","endpoint":"http://127.0.0.1:1","region":"auto","accessKeyId":"AKID","secretAccessKey":"secret"}}`,
		},
	}}
	router := RouteRecords(&ownBackend{}, records, &recordingPoster{})

	records.arrived = true
	resp, err := router.Sign(t.Context(), &bucketv1.SignRequest{
		Bucket:    "OCEL_RESOURCE_BUCKET_uploads",
		Key:       "a.png",
		Operation: bucketv1.SignedOperation_SIGNED_OPERATION_GET,
		Audience:  bucketv1.SignedAudience_SIGNED_AUDIENCE_INTERNAL,
	})
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}
	if !strings.Contains(resp.GetTarget().GetUrl(), "127.0.0.1:1") {
		t.Errorf("signed url = %q, want it signed for the store the record that arrived names", resp.GetTarget().GetUrl())
	}
}

func TestAnUnreadableBucketRecordIsRefusedWithoutItsValue(t *testing.T) {
	held := heldRecords{
		bindings: []live.Binding{{Name: "uploads", Key: "OCEL_RESOURCE_BUCKET_uploads", Type: bindingsv1.BindingType_BINDING_TYPE_BUCKET}},
		values:   map[string]string{"OCEL_RESOURCE_BUCKET_uploads": `{"bucket": secret-key-here`},
	}
	_, err := backends(held, &recordingPoster{})
	if err == nil {
		t.Fatal("backends = nil error, want an unreadable record refused")
	}
	if got := err.Error(); len(got) == 0 || strings.Contains(got, "secret-key-here") {
		t.Errorf("error = %q, want the record named and its value kept out", got)
	}
}

type heardRequest struct {
	method, path, credential, reach string
}

type listeningStore struct {
	mu    sync.Mutex
	heard []heardRequest
}

func (l *listeningStore) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	heard := heardRequest{method: r.Method, path: r.URL.Path, reach: r.URL.Path}
	if _, after, found := strings.Cut(r.Header.Get("Authorization"), "Credential="); found {
		heard.credential, _, _ = strings.Cut(after, "/")
	}
	if r.URL.Query().Has("prefix") {
		heard.reach = r.URL.Path + "/" + r.URL.Query().Get("prefix")
	}
	if r.Method == http.MethodPost {
		body, _ := io.ReadAll(r.Body)
		if _, after, found := strings.Cut(string(body), "<Key>"); found {
			key, _, _ := strings.Cut(after, "</Key>")
			heard.reach = r.URL.Path + "/" + key
		}
	}
	l.mu.Lock()
	l.heard = append(l.heard, heard)
	l.mu.Unlock()
	switch {
	case r.Method == http.MethodHead:
		w.Header().Set("Content-Length", "3")
		w.Header().Set("Content-Type", "image/png")
	case r.Method == http.MethodGet && r.URL.Query().Has("list-type"):
		w.Header().Set("Content-Type", "application/xml")
		_, _ = w.Write([]byte(`<ListBucketResult><IsTruncated>false</IsTruncated></ListBucketResult>`))
	case r.Method == http.MethodPost:
		w.Header().Set("Content-Type", "application/xml")
		_, _ = w.Write([]byte(`<DeleteResult></DeleteResult>`))
	}
}

func (l *listeningStore) take() []heardRequest {
	l.mu.Lock()
	defer l.mu.Unlock()
	heard := l.heard
	l.heard = nil
	return heard
}

func storeRecord(t *testing.T, endpoint, prefix, keyID string) string {
	t.Helper()
	raw, err := protojson.Marshal(&bindingsv1.Binding{Name: "ocel:bucket." + keyID, Properties: &bindingsv1.Binding_Bucket{Bucket: &bindingsv1.BucketProperties{
		Bucket: "acme", Endpoint: endpoint, Region: "auto", PathStyle: true, Prefix: prefix,
		AccessKeyId: keyID, SecretAccessKey: keyID + "-secret",
	}}})
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

type fetched map[string]string

func (f fetched) FetchLive(context.Context) (map[string]string, error) { return f, nil }

func TestTwoBindingsOnOneStoreBucketAreEachServedUnderTheirOwnPrefixAndKeyPair(t *testing.T) {
	store := &listeningStore{}
	server := httptest.NewServer(store)
	t.Cleanup(server.Close)

	bindings := []live.Binding{
		{Name: "uploads", Key: "OCEL_RESOURCE_BUCKET_uploads", Type: bindingsv1.BindingType_BINDING_TYPE_BUCKET},
		{Name: "avatars", Key: "OCEL_RESOURCE_BUCKET_avatars", Type: bindingsv1.BindingType_BINDING_TYPE_BUCKET},
	}
	values := live.New(fetched{
		"OCEL_RESOURCE_BUCKET_uploads": storeRecord(t, server.URL, "uploads/", "UPLOADSKEY"),
		"OCEL_RESOURCE_BUCKET_avatars": storeRecord(t, server.URL, "avatars/", "AVATARSKEY"),
	}, []string{"OCEL_RESOURCE_BUCKET_uploads", "OCEL_RESOURCE_BUCKET_avatars"}, bindings, nil)
	root := t.TempDir()
	if err := values.Project(root); err != nil {
		t.Fatal(err)
	}
	if err := values.Join(values.Prefetch(t.Context())); err != nil {
		t.Fatal(err)
	}
	router := RouteRecords(&ownBackend{}, values, &recordingPoster{})

	for _, tc := range []struct{ key, prefix, keyID string }{
		{"OCEL_RESOURCE_BUCKET_uploads", "/acme/uploads/", "UPLOADSKEY"},
		{"OCEL_RESOURCE_BUCKET_avatars", "/acme/avatars/", "AVATARSKEY"},
	} {
		t.Run(tc.key, func(t *testing.T) {
			raw, err := os.ReadFile(filepath.Join(root, tc.key))
			if err != nil {
				t.Fatal(err)
			}
			shown := &bindingsv1.Binding{}
			if err := protojson.Unmarshal(raw, shown); err != nil {
				t.Fatal(err)
			}
			name := shown.GetBucket().GetBucket()
			ctx := t.Context()
			heardUnder := func(op string) {
				t.Helper()
				heard := store.take()
				if len(heard) == 0 {
					t.Errorf("%s reached no store", op)
				}
				for _, h := range heard {
					if h.credential != tc.keyID || !strings.HasPrefix(h.reach, tc.prefix) {
						t.Errorf("%s reached %s %s as %q, want it under %s as %s", op, h.method, h.reach, h.credential, tc.prefix, tc.keyID)
					}
				}
			}
			store.take()

			_, _ = router.Head(ctx, &bucketv1.HeadRequest{Bucket: name, Key: "a.png"})
			heardUnder("Head")
			_, _ = router.List(ctx, &bucketv1.ListRequest{Bucket: name})
			heardUnder("List")
			_, _ = router.Delete(ctx, &bucketv1.DeleteRequest{Bucket: name, Keys: []string{"a.png"}})
			heardUnder("Delete")

			signed, err := router.Sign(ctx, &bucketv1.SignRequest{
				Bucket: name, Key: "a.png",
				Operation: bucketv1.SignedOperation_SIGNED_OPERATION_GET,
				Audience:  bucketv1.SignedAudience_SIGNED_AUDIENCE_INTERNAL,
			})
			if err != nil {
				t.Fatalf("Sign: %v", err)
			}
			target, err := url.Parse(signed.GetTarget().GetUrl())
			if err != nil {
				t.Fatal(err)
			}
			if target.Path != tc.prefix+"a.png" || !strings.HasPrefix(target.Query().Get("X-Amz-Credential"), tc.keyID+"/") {
				t.Errorf("signed %v, want %sa.png signed with %s", target, tc.prefix, tc.keyID)
			}

			session := presignIn(t, router, name)
			heardUnder("PresignUpload")
			_, _ = router.CompleteUpload(ctx, &bucketv1.CompleteUploadRequest{SessionId: session})
			heardUnder("CompleteUpload")
		})
	}
}
