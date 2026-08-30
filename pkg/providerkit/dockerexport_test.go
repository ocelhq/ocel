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
	host, err := providerkit.OpenDockerHost()
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
	host, err := providerkit.OpenDockerHost()
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
