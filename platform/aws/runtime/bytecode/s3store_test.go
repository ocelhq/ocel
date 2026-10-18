package bytecode

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

func newS3Store(t *testing.T, handler http.HandlerFunc) (objectStore, *httptest.Server) {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	client := s3.New(s3.Options{
		Region:           "us-east-1",
		BaseEndpoint:     &srv.URL,
		UsePathStyle:     true,
		Credentials:      credentials.NewStaticCredentialsProvider("AKID", "SECRET", ""),
		RetryMaxAttempts: 1,
	})
	return s3Store{client: client}, srv
}

func TestS3BytecodeStore(t *testing.T) {
	t.Run("object exists", func(t *testing.T) {
		cases := []struct {
			name       string
			status     int
			wantExists bool
			wantErr    bool
		}{
			{name: "404 means the object is absent", status: http.StatusNotFound, wantExists: false},
			{name: "200 means it is already there", status: http.StatusOK, wantExists: true},
			{name: "403 is how an absent key reads without s3:ListBucket", status: http.StatusForbidden, wantExists: false},
			{name: "500 is a real error", status: http.StatusInternalServerError, wantErr: true},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				store, _ := newS3Store(t, func(w http.ResponseWriter, r *http.Request) {
					if r.Method != http.MethodHead {
						t.Errorf("method = %s, want HEAD", r.Method)
					}
					w.WriteHeader(tc.status)
				})

				exists, err := store.objectExists(context.Background(), "assets-xyz", "ocel/bytecode/my-app/node24-arm64.tar.gz")
				if tc.wantErr {
					if err == nil {
						t.Fatalf("objectExists() = %v, nil, want an error", exists)
					}
					return
				}
				if err != nil {
					t.Fatalf("objectExists() unexpected error: %v", err)
				}
				if exists != tc.wantExists {
					t.Errorf("objectExists() = %v, want %v", exists, tc.wantExists)
				}
			})
		}
	})

	t.Run("put object", func(t *testing.T) {
		var gotPath string
		var gotBody []byte
		store, _ := newS3Store(t, func(w http.ResponseWriter, r *http.Request) {
			gotPath = r.URL.Path
			gotBody, _ = io.ReadAll(r.Body)
			w.WriteHeader(http.StatusOK)
		})

		if err := store.putObject(context.Background(), "assets-xyz", "ocel/bytecode/my-app/node24-arm64.tar.gz", []byte("archive bytes")); err != nil {
			t.Fatalf("putObject: %v", err)
		}
		if want := "/assets-xyz/ocel/bytecode/my-app/node24-arm64.tar.gz"; gotPath != want {
			t.Errorf("put path = %q, want %q", gotPath, want)
		}
		if string(gotBody) != "archive bytes" {
			t.Errorf("put body = %q, want %q", gotBody, "archive bytes")
		}
	})

	t.Run("put object reports a failure", func(t *testing.T) {
		store, _ := newS3Store(t, func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusForbidden)
		})

		if err := store.putObject(context.Background(), "assets-xyz", "some/key", []byte("x")); err == nil {
			t.Error("putObject() error = nil, want the failure reported")
		}
	})

	t.Run("get object", func(t *testing.T) {
		t.Run("200 returns the body and content length", func(t *testing.T) {
			store, _ := newS3Store(t, func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodGet {
					t.Errorf("method = %s, want GET", r.Method)
				}
				w.Header().Set("Content-Length", "13")
				w.WriteHeader(http.StatusOK)
				w.Write([]byte("archive bytes"))
			})

			body, size, err := store.getObject(context.Background(), "assets-xyz", "ocel/bytecode/my-app/node24.3.1-arm64.tar.gz")
			if err != nil {
				t.Fatalf("getObject: %v", err)
			}
			defer body.Close()
			if size != 13 {
				t.Errorf("size = %d, want 13", size)
			}
			got, err := io.ReadAll(body)
			if err != nil {
				t.Fatalf("read body: %v", err)
			}
			if string(got) != "archive bytes" {
				t.Errorf("body = %q, want %q", got, "archive bytes")
			}
		})

		t.Run("a NoSuchKey error body is a miss, not an error", func(t *testing.T) {
			store, _ := newS3Store(t, func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusNotFound)
				w.Write([]byte(`<?xml version="1.0" encoding="UTF-8"?><Error><Code>NoSuchKey</Code><Message>The specified key does not exist.</Message></Error>`))
			})

			_, _, err := store.getObject(context.Background(), "assets-xyz", "missing/key")
			if !errors.Is(err, errCacheMiss) {
				t.Errorf("getObject() error = %v, want errCacheMiss", err)
			}
		})

		t.Run("403 is a real error, not an absence", func(t *testing.T) {
			store, _ := newS3Store(t, func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusForbidden)
				w.Write([]byte(`<?xml version="1.0" encoding="UTF-8"?><Error><Code>AccessDenied</Code><Message>Access Denied</Message></Error>`))
			})

			_, _, err := store.getObject(context.Background(), "assets-xyz", "some/key")
			if err == nil {
				t.Fatal("getObject() error = nil, want the failure reported")
			}
			if errors.Is(err, errCacheMiss) {
				t.Error("getObject() reported a miss for an access failure")
			}
		})
	})
}
