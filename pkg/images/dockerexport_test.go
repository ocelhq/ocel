package images_test

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/images"
)

func TestAnImageIsExportedUnderTheCoordinateItIsNamedBy(t *testing.T) {
	var asked string
	daemonServing(t, func(w http.ResponseWriter, r *http.Request) {
		asked = r.Method + " " + r.URL.Path
		_, _ = io.WriteString(w, "tar-bytes")
	})
	host, err := images.DockerHostFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	transport := host.Transport()
	defer transport.CloseIdleConnections()

	stream, err := host.Export(context.Background(), &http.Client{Transport: transport}, "ocel/web:sha256-abc")
	if err != nil {
		t.Fatalf("Export() = %v", err)
	}
	defer func() { _ = stream.Close() }()

	if want := "GET /images/ocel/web:sha256-abc/get"; asked != want {
		t.Errorf("Export() asked the daemon for %q, want %q", asked, want)
	}
	read, err := io.ReadAll(stream)
	if err != nil {
		t.Fatal(err)
	}
	if string(read) != "tar-bytes" {
		t.Errorf("Export() sent %q", read)
	}
}

func TestAnImageTheDaemonDoesNotHaveIsRefusedWithWhatItSaid(t *testing.T) {
	daemonServing(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = io.WriteString(w, `{"message":"No such image: ocel/web:sha256-abc"}`)
	})
	host, err := images.DockerHostFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	transport := host.Transport()
	defer transport.CloseIdleConnections()

	_, err = host.Export(context.Background(), &http.Client{Transport: transport}, "ocel/web:sha256-abc")
	if err == nil {
		t.Fatal("Export() of an image the daemon does not have succeeded, and the transfer would send nothing")
	}
	if !strings.Contains(err.Error(), "No such image") {
		t.Errorf("Export() = %v, want the daemon's own reason", err)
	}
}

func inspected(t *testing.T, handler http.HandlerFunc) (images.ImageInspection, error) {
	t.Helper()
	daemonServing(t, handler)
	host, err := images.DockerHostFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	transport := host.Transport()
	defer transport.CloseIdleConnections()
	return host.Inspect(context.Background(), &http.Client{Transport: transport}, "ocel/web:sha256-abc")
}

func TestTheArchitectureAnImageIsBuiltForIsReadOffTheDaemonsOwnInspection(t *testing.T) {
	var asked string
	inspection, err := inspected(t, func(w http.ResponseWriter, r *http.Request) {
		asked = r.Method + " " + r.URL.Path
		_, _ = io.WriteString(w, `{"Architecture":"arm64","Os":"linux"}`)
	})
	if err != nil {
		t.Fatalf("Inspect() = %v", err)
	}
	if inspection.Architecture != "arm64" {
		t.Errorf("Inspect() architecture = %q, want the architecture the daemon says the image is built for", inspection.Architecture)
	}
	if want := "GET /images/ocel/web:sha256-abc/json"; asked != want {
		t.Errorf("Inspect() asked the daemon for %q, want %q", asked, want)
	}
}

func TestAnImageTheDaemonNamesNoArchitectureForIsRefusedRatherThanWrappedBlind(t *testing.T) {
	_, err := inspected(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"Os":"linux"}`)
	})
	if err == nil {
		t.Fatal("Inspect() read an inspection naming none as an answer, and the image would be wrapped in a runtime built for another architecture")
	}
	for _, want := range []string{"tcp://", "ocel/web:sha256-abc"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("Inspect() = %v, want it to name %q", err, want)
		}
	}
}

func inspectedContentDigest(t *testing.T, body string) string {
	t.Helper()
	inspection, err := inspected(t, func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, body) })
	if err != nil {
		t.Fatalf("Inspect() = %v", err)
	}
	return inspection.ContentDigest
}

const inspectionOfABuild = `{"Id":"sha256:aaa","Created":"2026-10-08T10:00:00Z","Architecture":"amd64","Os":"linux",` +
	`"Config":{"Entrypoint":["/app/server"],"Env":["PORT=3000"],"Image":"sha256:parent-a"},` +
	`"RootFS":{"Type":"layers","Layers":["sha256:l1","sha256:l2"]}}`

func TestARebuildOfUnchangedContentHasTheContentDigestOfTheBuildBeforeIt(t *testing.T) {
	rebuilt := strings.NewReplacer(
		`"Id":"sha256:aaa"`, `"Id":"sha256:bbb"`,
		`"Created":"2026-10-08T10:00:00Z"`, `"Created":"2026-10-08T10:05:00Z"`,
		`"Image":"sha256:parent-a"`, `"Image":"sha256:parent-b"`,
	).Replace(inspectionOfABuild)

	first, second := inspectedContentDigest(t, inspectionOfABuild), inspectedContentDigest(t, rebuilt)
	if first != second {
		t.Errorf("a rebuild digests to %s and the build before it to %s: what only the build's clock or id changed must not read as new content, or the box is sent the whole image again", second, first)
	}
	if !strings.HasPrefix(first, "sha256:") || len(first) != len("sha256:")+64 {
		t.Errorf("Inspect() content digest = %q, want a sha256 digest the tag can be derived from", first)
	}
}

func TestAnImageWhoseLayersConfigOrPlatformChangedHasANewContentDigest(t *testing.T) {
	before := inspectedContentDigest(t, inspectionOfABuild)
	for name, changed := range map[string]string{
		"a layer":        strings.Replace(inspectionOfABuild, "sha256:l2", "sha256:l3", 1),
		"a layer more":   strings.Replace(inspectionOfABuild, `"sha256:l2"`, `"sha256:l2","sha256:l3"`, 1),
		"the env":        strings.Replace(inspectionOfABuild, "PORT=3000", "PORT=4000", 1),
		"the entry":      strings.Replace(inspectionOfABuild, "/app/server", "/app/other", 1),
		"the platform":   strings.Replace(inspectionOfABuild, "amd64", "arm64", 1),
		"the variant":    strings.Replace(inspectionOfABuild, `"Architecture":"amd64"`, `"Architecture":"amd64","Variant":"v3"`, 1),
		"the os version": strings.Replace(inspectionOfABuild, `"Os":"linux"`, `"Os":"linux","OsVersion":"10.0.17763.1"`, 1),
	} {
		if inspectedContentDigest(t, changed) == before {
			t.Errorf("an image with %s changed keeps its content digest, so the box would keep running the image it was meant to replace", name)
		}
	}
}

func TestADaemonThatRefusesTheInspectionIsReportedWithWhatItSaid(t *testing.T) {
	_, err := inspected(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = io.WriteString(w, `{"message":"No such image: ocel/web:sha256-abc"}`)
	})
	if err == nil {
		t.Fatal("Inspect() read a refusal as an answer")
	}
	for _, want := range []string{"tcp://", "No such image"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("Inspect() = %v, want it to name %q", err, want)
		}
	}
}
