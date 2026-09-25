package providerkit_test

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/providerkit"
)

func TestAnImageIsExportedUnderTheCoordinateItIsNamedBy(t *testing.T) {
	var asked string
	daemonServing(t, func(w http.ResponseWriter, r *http.Request) {
		asked = r.Method + " " + r.URL.Path
		_, _ = io.WriteString(w, "tar-bytes")
	})
	host, err := providerkit.DockerHostFromEnv()
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
	carried, err := io.ReadAll(stream)
	if err != nil {
		t.Fatal(err)
	}
	if string(carried) != "tar-bytes" {
		t.Errorf("Export() carried %q", carried)
	}
}

func TestAnImageTheDaemonDoesNotHoldIsRefusedWithWhatItSaid(t *testing.T) {
	daemonServing(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = io.WriteString(w, `{"message":"No such image: ocel/web:sha256-abc"}`)
	})
	host, err := providerkit.DockerHostFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	transport := host.Transport()
	defer transport.CloseIdleConnections()

	_, err = host.Export(context.Background(), &http.Client{Transport: transport}, "ocel/web:sha256-abc")
	if err == nil {
		t.Fatal("Export() of an image the daemon does not hold succeeded, and the transfer would carry nothing")
	}
	if !strings.Contains(err.Error(), "No such image") {
		t.Errorf("Export() = %v, want the daemon's own reason", err)
	}
}

func inspectedArchitecture(t *testing.T, handler http.HandlerFunc) (string, error) {
	t.Helper()
	daemonServing(t, handler)
	host, err := providerkit.DockerHostFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	transport := host.Transport()
	defer transport.CloseIdleConnections()
	return host.Architecture(context.Background(), &http.Client{Transport: transport}, "ocel/web:sha256-abc")
}

func TestTheArchitectureAnImageIsBuiltForIsReadOffTheDaemonsOwnInspection(t *testing.T) {
	var asked string
	arch, err := inspectedArchitecture(t, func(w http.ResponseWriter, r *http.Request) {
		asked = r.Method + " " + r.URL.Path
		_, _ = io.WriteString(w, `{"Architecture":"arm64","Os":"linux"}`)
	})
	if err != nil {
		t.Fatalf("Architecture() = %v", err)
	}
	if arch != "arm64" {
		t.Errorf("Architecture() = %q, want the architecture the daemon says the image is built for", arch)
	}
	if want := "GET /images/ocel/web:sha256-abc/json"; asked != want {
		t.Errorf("Architecture() asked the daemon for %q, want %q", asked, want)
	}
}

func TestAnImageTheDaemonNamesNoArchitectureForIsRefusedRatherThanWrappedBlind(t *testing.T) {
	_, err := inspectedArchitecture(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"Os":"linux"}`)
	})
	if err == nil {
		t.Fatal("Architecture() read an inspection naming none as an answer, and the image would be wrapped in a runtime built for another architecture")
	}
	for _, want := range []string{"tcp://", "ocel/web:sha256-abc"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("Architecture() = %v, want it to name %q", err, want)
		}
	}
}

func TestADaemonThatRefusesTheInspectionIsReportedWithWhatItSaid(t *testing.T) {
	_, err := inspectedArchitecture(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = io.WriteString(w, `{"message":"No such image: ocel/web:sha256-abc"}`)
	})
	if err == nil {
		t.Fatal("Architecture() read a refusal as an answer")
	}
	for _, want := range []string{"tcp://", "No such image"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("Architecture() = %v, want it to name %q", err, want)
		}
	}
}
