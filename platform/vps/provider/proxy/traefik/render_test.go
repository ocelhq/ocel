package traefik_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

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

func TestTwoPlacementsOfOcelsFileShareNoRouterServiceOrMiddlewareName(t *testing.T) {
	t.Parallel()

	watched := coolifyTraefik()
	beneath := coolifyTraefik()
	beneath.Directory = watched.Directory + "/ocel"
	first, second := rendered(t, watched, served()), rendered(t, beneath, served())
	for name := range first.HTTP.Routers {
		if _, shared := second.HTTP.Routers[name]; shared {
			t.Errorf("both placements name router %s, want each named apart: your Traefik keeps the first file's router of a name and skips the other's, so while both are placed it may never read the new one", name)
		}
	}
	for name := range first.HTTP.Services {
		if _, shared := second.HTTP.Services[name]; shared {
			t.Errorf("both placements name service %s, want each named apart", name)
		}
	}
	for name := range first.HTTP.Middlewares {
		if _, shared := second.HTTP.Middlewares[name]; shared {
			t.Errorf("both placements name middleware %s, want each named apart", name)
		}
	}
}

func TestEveryRouterToTheSwitchboardRetriesARequestTheSwitchboardDidNotAccept(t *testing.T) {
	t.Parallel()

	read := rendered(t, coolifyTraefik(), served())
	forwarding := 0
	for name, router := range read.HTTP.Routers {
		if _, switchboard := read.HTTP.Services[router.Service]; !switchboard {
			continue
		}
		forwarding++
		retried := false
		for _, used := range router.Middlewares {
			if retry := read.HTTP.Middlewares[used].Retry; retry != nil {
				retried = true
				waited, err := time.ParseDuration(retry.InitialInterval)
				if err != nil || time.Duration(retry.Attempts-1)*waited < 5*time.Second {
					t.Errorf("router %s retries %d times from %q, want the attempts spaced to outlast the seconds a switchboard takes to restart", name, retry.Attempts, retry.InitialInterval)
				}
			}
		}
		if !retried {
			t.Errorf("router %s forwards to the switchboard through %v, want a retry: a request the restarting switchboard refused would otherwise answer 502", name, router.Middlewares)
		}
	}
	if forwarding == 0 {
		t.Fatalf("no router forwards to a service ocel's file defines: %+v", read.HTTP.Routers)
	}
}

func TestOcelsFileRoutesAHostnameOnlyItsOwnPlacementNamesAndOrdersItNoCertificate(t *testing.T) {
	t.Parallel()

	front := coolifyTraefik()
	read := rendered(t, front, served())
	placement := traefik.DerivePlacementHostname(front.File())
	moved := coolifyTraefik()
	moved.Directory += "/ocel"
	if placement == traefik.DerivePlacementHostname(moved.File()) {
		t.Fatalf("both placements are named %s, want each its own", placement)
	}
	for _, router := range read.HTTP.Routers {
		if router.Rule != "Host(`"+placement+"`)" {
			continue
		}
		if _, switchboard := read.HTTP.Services[router.Service]; !switchboard || len(router.EntryPoints) != 1 || router.EntryPoints[0] != front.HTTPS {
			t.Errorf("the router for %s is %+v, want it forwarded to the switchboard on %s", placement, router, front.HTTPS)
		}
		if router.TLS == nil || router.TLS.CertResolver != "" {
			t.Errorf("the router for %s is %+v, want TLS with no resolver: nothing may order a certificate for a name that only proves your Traefik reads this file", placement, router)
		}
		return
	}
	t.Errorf("no router routes %s among %+v", placement, read.HTTP.Routers)
}
