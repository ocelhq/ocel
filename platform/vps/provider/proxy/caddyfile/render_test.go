package caddyfile_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/platform/vps/provider/proxy"
	"github.com/ocelhq/ocel/platform/vps/provider/proxy/caddyfile"
)

var served = []string{"shop.example.com", "ocel-edge-probe.preview.example.com", "pr-12-web-abcdefghijklmnopp3347l26.preview.example.com", "box.example.com"}

func golden(t *testing.T, name string) string {
	t.Helper()
	read, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return string(read)
}

func TestTheSnippetIsOneSiteBlockOverEveryHostnameInEachWayYourCaddyRuns(t *testing.T) {
	t.Parallel()

	for name, each := range map[string]struct {
		front  caddyfile.Caddyfile
		golden string
	}{
		"a container on a network reaches the switchboard by name and holds streams open 30s": {
			front: caddyfile.Caddyfile{Container: "caddy", Config: "/etc/caddy/Caddyfile", Network: "web"}, golden: "network.caddy"},
		"Coolify's Caddy, a container on its network": {
			front: caddyfile.Caddyfile{Preset: "coolify", Container: "coolify-proxy", Config: "/config/caddy/Caddyfile.autosave", Network: "coolify"}, golden: "network.caddy"},
		"a container on the host's network reaches the loopback port and holds streams open 30s": {
			front: caddyfile.Caddyfile{Container: "caddy", Config: "/etc/caddy/Caddyfile", Port: 8480}, golden: "hostnetwork.caddy"},
		"a service reaches the loopback port, without stream_close_delay, which the 2.6 package refuses": {
			front: caddyfile.Caddyfile{Port: 8480}, golden: "service.caddy"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			rendered, err := each.front.Render(proxy.Spec{Hostnames: served, PreviewBase: "preview.example.com"})
			if err != nil {
				t.Fatalf("Render() = %v", err)
			}
			if want := golden(t, each.golden); string(rendered) != want {
				t.Errorf("Render() =\n%s\nwant\n%s", rendered, want)
			}
		})
	}
}

func TestEachForwardToTheSwitchboardKeepsTryingItForTheSecondsItTakesToRestart(t *testing.T) {
	t.Parallel()

	for name, front := range map[string]caddyfile.Caddyfile{
		"a container on a network": {Container: "caddy", Config: "/etc/caddy/Caddyfile", Network: "web"},
		"the caddy package":        {Port: 8480},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			rendered, err := front.Render(proxy.Spec{Hostnames: served, PreviewBase: "preview.example.com"})
			if err != nil {
				t.Fatalf("Render() = %v", err)
			}
			forwards := strings.Count(string(rendered), "reverse_proxy ")
			if forwards == 0 || strings.Count(string(rendered), "\t\tlb_try_duration 10s\n\t\tlb_try_interval 250ms\n") != forwards {
				t.Errorf("Render() =\n%s\nwant every reverse_proxy to try the switchboard again for 10s: a request the restarting switchboard refused would otherwise answer 502", rendered)
			}
		})
	}
}

func TestABoxServingNoHostnameRendersABlankLineAndNeverABarePortOrAnEmptyFile(t *testing.T) {
	t.Parallel()

	for name, front := range map[string]caddyfile.Caddyfile{
		"Coolify's Caddy":   {Preset: "coolify", Container: "coolify-proxy", Network: "coolify"},
		"the caddy package": {Port: 8480},
		"a Caddy container": {Container: "caddy", Port: 8480},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			rendered, err := front.Render(proxy.Spec{})
			if err != nil || string(rendered) != "\n" {
				t.Errorf("Render() = %q, %v; want a blank line: a site block with no hostname is a bare port, and the caddy 2.6 package refuses to import an empty file with EOF", rendered, err)
			}
		})
	}
}

func TestTheSnippetIsPlacedAsOcelCaddyInTheDirectoryYourCaddyImports(t *testing.T) {
	t.Parallel()

	if got, want := (caddyfile.Caddyfile{Directory: "/etc/caddy/ocel.d"}).File(), "/etc/caddy/ocel.d/ocel.caddy"; got != want {
		t.Errorf("File() = %q, want %q", got, want)
	}
}
