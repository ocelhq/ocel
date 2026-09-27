package traefik_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/platform/vps/provider/proxy"
	"github.com/ocelhq/ocel/platform/vps/provider/proxy/traefik"
)

func served() proxy.Spec {
	return proxy.Spec{
		Hostnames: []string{
			"ocel-edge-probe.preview.example.com",
			"pr-12--web.preview.example.com",
			"shop.example.com",
		},
		PreviewBase: "preview.example.com",
		Upstream:    "unix//run/ocel-front/switchboard.sock",
		Router:      "switchboard",
	}
}

func golden(t *testing.T, name string) string {
	t.Helper()
	read, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return string(read)
}

func TestEachHostnameIsRoutedToTheSwitchboardAndRedirectedFromHTTPAboveEveryRouteOfYours(t *testing.T) {
	t.Parallel()

	front := traefik.Traefik{
		Directory:       "/data/coolify/proxy/dynamic",
		Resolver:        "letsencrypt",
		PreviewResolver: "cloudflare",
		HTTP:            "web",
		HTTPS:           "websecure",
		Network:         "coolify",
	}
	rendered, err := front.Render(served())
	if err != nil {
		t.Fatalf("Render() = %v", err)
	}
	if want := golden(t, "wildcard.yml"); string(rendered) != want {
		t.Errorf("Render() =\n%s\nwant\n%s", rendered, want)
	}
}

func TestWithoutAPreviewResolverEachPreviewHostnameNamesTheResolverAndTheSwitchboardIsReachedOnLoopback(t *testing.T) {
	t.Parallel()

	front := traefik.Traefik{
		Directory: "/etc/traefik/dynamic",
		Resolver:  "letsencrypt",
		HTTP:      "http",
		HTTPS:     "https",
		Port:      9000,
	}
	rendered, err := front.Render(served())
	if err != nil {
		t.Fatalf("Render() = %v", err)
	}
	if want := golden(t, "perhost.yml"); string(rendered) != want {
		t.Errorf("Render() =\n%s\nwant\n%s", rendered, want)
	}
}

func TestOcelsRoutesAreWrittenToOcelYmlInTheDirectoryYourTraefikWatches(t *testing.T) {
	t.Parallel()

	if got := (traefik.Traefik{Directory: "/etc/dokploy/traefik/dynamic/"}).File(); got != "/etc/dokploy/traefik/dynamic/ocel.yml" {
		t.Errorf("File() = %q, want ocel.yml directly in the directory", got)
	}
}

func TestARenderingYourTraefikWouldReadAsATemplateIsRefusedBeforeItIsPlaced(t *testing.T) {
	t.Parallel()

	front := traefik.Traefik{Directory: "/etc/traefik/dynamic", Resolver: "{{ env \"RESOLVER\" }}", HTTP: "web", HTTPS: "websecure", Port: 8480}
	rendered, err := front.Render(served())
	if err != nil {
		t.Fatalf("Render() = %v", err)
	}
	err = front.Validate(context.Background(), rendered)
	if err == nil {
		t.Fatal("Validate() = nil, want a refusal: Traefik runs every file through text/template before it parses it")
	}
	var refused refusal.Refusal
	if !errors.As(err, &refused) || refused.Code != refusal.CodeInvalid || !strings.Contains(err.Error(), "{{") {
		t.Errorf("Validate() = %v, want an invalid refusal naming {{", err)
	}
}
