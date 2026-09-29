package clitest

import (
	"archive/tar"
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/images"
)

func ServeImageDaemon(t *testing.T, architecture string) {
	t.Helper()
	saved := savedImage(t, architecture)
	daemon := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/json"):
			_, _ = w.Write([]byte(`{"Architecture":"` + architecture + `","Os":"linux"}`))
		case strings.HasSuffix(r.URL.Path, "/get"):
			_, _ = w.Write(saved)
		default:
			http.Error(w, "unexpected "+r.Method+" "+r.URL.Path, http.StatusNotFound)
		}
	}))
	t.Cleanup(daemon.Close)
	t.Setenv(images.DockerTLSVerifyEnv, "")
	t.Setenv(images.DockerCertPathEnv, "")
	t.Setenv(images.DockerHostEnv, "tcp://"+strings.TrimPrefix(daemon.URL, "http://"))
}

func savedImage(t *testing.T, architecture string) []byte {
	t.Helper()
	var saved bytes.Buffer
	archive := tar.NewWriter(&saved)
	for _, file := range []struct{ name, body string }{
		{"config.json", `{"architecture":"` + architecture + `","os":"linux","config":{"Entrypoint":["/app/server"]},"rootfs":{"type":"layers","diff_ids":[]}}`},
		{"manifest.json", `[{"Config":"config.json","RepoTags":["ocel/saved:latest"],"Layers":[]}]`},
	} {
		if err := archive.WriteHeader(&tar.Header{Name: file.name, Mode: 0o644, Size: int64(len(file.body)), Typeflag: tar.TypeReg}); err != nil {
			t.Fatalf("save the image: %v", err)
		}
		if _, err := archive.Write([]byte(file.body)); err != nil {
			t.Fatalf("save the image: %v", err)
		}
	}
	if err := archive.Close(); err != nil {
		t.Fatalf("save the image: %v", err)
	}
	return saved.Bytes()
}
