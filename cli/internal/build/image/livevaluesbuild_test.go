//go:build integration

package image_test

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/cli/internal/build/image"
	"github.com/ocelhq/ocel/pkg/provider/enginetest"
)

func TestMain(m *testing.M) { os.Exit(enginetest.Main(m)) }

const (
	hostAnswer = "answered from the host"
	bindingKey = "OCEL_RESOURCE_POSTGRES_my-db"
)

func answeringOnTheHost(t *testing.T) int {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, hostAnswer)
	})}
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(func() { _ = server.Close() })
	return listener.Addr().(*net.TCPAddr).Port
}

func docker(t *testing.T, args ...string) string {
	t.Helper()
	out, err := exec.Command("docker", args...).CombinedOutput()
	if err != nil {
		t.Fatalf("docker %s = %v\n%s", strings.Join(args, " "), err, out)
	}
	return string(out)
}

func runInImage(t *testing.T, ref, entrypoint string, args ...string) string {
	t.Helper()
	run := append([]string{"run", "--rm", "--network", "none"}, enginetest.RunLabelArgs(t)...)
	run = append(run, "--entrypoint", entrypoint, ref)
	return docker(t, append(run, args...)...)
}

func bindingValue(t *testing.T, port int) string {
	t.Helper()
	random := make([]byte, 12)
	if _, err := rand.Read(random); err != nil {
		t.Fatal(err)
	}
	return fmt.Sprintf("postgres://app:%s@127.0.0.1:%d/db", hex.EncodeToString(random), port)
}

func holdsNowhere(t *testing.T, ref, value string) {
	t.Helper()
	for _, shown := range []string{
		docker(t, "image", "inspect", ref),
		docker(t, "image", "history", "--no-trunc", "--format", "{{.CreatedBy}}", ref),
	} {
		if strings.Contains(shown, value) {
			t.Errorf("the image's config or history holds the binding value:\n%s", shown)
		}
	}
	saved, err := exec.Command("docker", "image", "save", ref).Output()
	if err != nil {
		t.Fatalf("docker image save %s = %v", ref, err)
	}
	if layersHold(t, saved, value) {
		t.Error("a layer of the image holds the binding value")
	}
}

func leavesNoLiveFiles(t *testing.T, ref string) {
	t.Helper()
	listed := runInImage(t, ref, "sh", "-c", "ls -d /run/ocel-live /secrets-hash /used-secrets-hash 2>/dev/null; true")
	if strings.TrimSpace(listed) != "" {
		t.Errorf("the image keeps what the build step read its live values through:\n%s", listed)
	}
}

func layersHold(t *testing.T, archive []byte, value string) bool {
	t.Helper()
	reader := tar.NewReader(bytes.NewReader(archive))
	for {
		header, err := reader.Next()
		if err == io.EOF {
			return false
		}
		if err != nil {
			t.Fatalf("read the saved image: %v", err)
		}
		blob, err := io.ReadAll(reader)
		if err != nil {
			t.Fatal(err)
		}
		if holdsInside(blob, value) {
			t.Logf("found in %s", header.Name)
			return true
		}
	}
}

func holdsInside(blob []byte, value string) bool {
	if bytes.Contains(blob, []byte(value)) {
		return true
	}
	unzipped, err := gzip.NewReader(bytes.NewReader(blob))
	if err != nil {
		return false
	}
	plain, err := io.ReadAll(unzipped)
	if err != nil {
		return false
	}
	return holdsInside(plain, value)
}

func builtWithBinding(t *testing.T, name, fixture string) (image.Image, string) {
	t.Helper()
	value := bindingValue(t, answeringOnTheHost(t))
	built, err := image.Build(context.Background(), image.App{Slug: "secrets", Name: name, Workspace: located(t, fixture)}, "", image.NewLiveValues(map[string]string{bindingKey: value}, nil, []byte("the integration test machine")), os.Stderr)
	if err != nil {
		t.Fatalf("Build() = %v", err)
	}
	t.Cleanup(func() { _ = exec.Command("docker", "image", "rm", "--force", built.Ref).Run() })
	return built, value
}

func proves(t *testing.T, built image.Image, value string, files ...string) {
	t.Helper()
	sum := sha256.Sum256([]byte(value))
	read := func(path string) string {
		return strings.TrimSpace(runInImage(t, built.Ref, "cat", path))
	}
	if got := read(files[0]); got != hex.EncodeToString(sum[:]) {
		t.Errorf("the build step read %q from the file mounted for the binding, want the digest of its value", got)
	}
	if got := read(files[1]); got != hostAnswer {
		t.Errorf("the build step reached %q on the host's 127.0.0.1, want %q", got, hostAnswer)
	}
}

func TestADockerfileBuildReadsABindingThroughASecretMountAndLeavesItInNoLayer(t *testing.T) {
	built, value := builtWithBinding(t, "Dockerfile Secrets", "testdata/secretdockerfile")

	proves(t, built, value, "/proof", "/reached")
	holdsNowhere(t, built.Ref, value)
	leavesNoLiveFiles(t, built.Ref)
}

func TestARailpackBuildReadsABindingFromTheLiveDirAndLeavesItInNoLayer(t *testing.T) {
	built, value := builtWithBinding(t, "Railpack Secrets", "testdata/secretrailpack")

	proves(t, built, value, "/app/dist/proof", "/app/dist/reached")
	if got := strings.TrimSpace(runInImage(t, built.Ref, "cat", "/app/dist/in-environment")); got != "false" {
		t.Errorf("the build saw the binding in its environment (%s), want it only in the live dir", got)
	}
	holdsNowhere(t, built.Ref, value)
	leavesNoLiveFiles(t, built.Ref)
}

func TestARailpackBuildReadsTheBindingProxysSessionTokenFromItsEnvironmentAndLeavesItInNoLayer(t *testing.T) {
	value := bindingValue(t, answeringOnTheHost(t))
	token := bindingValue(t, 0)
	built, err := image.Build(context.Background(), image.App{Slug: "secrets", Name: "Railpack Proxy", Workspace: located(t, "testdata/secretrailpack")}, "", image.NewLiveValues(map[string]string{bindingKey: value}, map[string]string{"OCEL_SESSION_TOKEN": token}, []byte("the integration test machine")), os.Stderr)
	if err != nil {
		t.Fatalf("Build() = %v", err)
	}
	t.Cleanup(func() { _ = exec.Command("docker", "image", "rm", "--force", built.Ref).Run() })

	sum := sha256.Sum256([]byte(token))
	if got := strings.TrimSpace(runInImage(t, built.Ref, "cat", "/app/dist/session-token")); got != hex.EncodeToString(sum[:]) {
		t.Errorf("the build step read %q as its session token, want the digest of the token it was handed", got)
	}
	holdsNowhere(t, built.Ref, token)
}

func TestABuildRunAgainWithAChangedBindingReadsTheNewValueRatherThanTheCachedOne(t *testing.T) {
	for name, fixture := range map[string]struct {
		dir   string
		files []string
	}{
		"dockerfile": {"testdata/secretdockerfile", []string{"/proof", "/reached"}},
		"railpack":   {"testdata/secretrailpack", []string{"/app/dist/proof", "/app/dist/reached"}},
	} {
		t.Run(name, func(t *testing.T) {
			builtWithBinding(t, "Rebuilt Secrets", fixture.dir)
			rebuilt, value := builtWithBinding(t, "Rebuilt Secrets", fixture.dir)

			proves(t, rebuilt, value, fixture.files...)
		})
	}
}
