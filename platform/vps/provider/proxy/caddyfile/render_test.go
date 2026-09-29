package caddyfile_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/ocelhq/ocel/platform/vps/provider/proxy"
	"github.com/ocelhq/ocel/platform/vps/provider/proxy/caddyfile"
)

var served = []string{"shop.example.com", "ocel-edge-probe.preview.example.com", "pr-12--web.preview.example.com", "box.example.com"}

func golden(t *testing.T, name string) string {
	t.Helper()
	read, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return string(read)
}

func TestTheSnippetIsOneSiteBlockOverEveryHostnameToTheSwitchboardOnItsNetwork(t *testing.T) {
	t.Parallel()

	rendered, err := caddyfile.Caddyfile{Network: "coolify"}.Render(proxy.Spec{Hostnames: served, PreviewBase: "preview.example.com"})
	if err != nil {
		t.Fatalf("Render() = %v", err)
	}
	if want := golden(t, "network.caddy"); string(rendered) != want {
		t.Errorf("Render() =\n%s\nwant\n%s", rendered, want)
	}
}

func TestTheSnippetReachesTheSwitchboardOnTheLoopbackPortWithoutANetwork(t *testing.T) {
	t.Parallel()

	rendered, err := caddyfile.Caddyfile{Port: 8480}.Render(proxy.Spec{Hostnames: served})
	if err != nil {
		t.Fatalf("Render() = %v", err)
	}
	if want := golden(t, "loopback.caddy"); string(rendered) != want {
		t.Errorf("Render() =\n%s\nwant\n%s", rendered, want)
	}
}

func TestABoxServingNoHostnameRendersAnEmptySnippetAndNeverABarePort(t *testing.T) {
	t.Parallel()

	rendered, err := caddyfile.Caddyfile{Network: "coolify"}.Render(proxy.Spec{})
	if err != nil || rendered == nil || len(rendered) != 0 {
		t.Errorf("Render() = %#v, %v; want an empty file: a site block with no hostname is a bare port, and :80 clashes with Coolify's own fallback", rendered, err)
	}
}

func TestTheSnippetIsPlacedAsOcelCaddyInTheDirectoryYourCaddyImports(t *testing.T) {
	t.Parallel()

	if got, want := (caddyfile.Caddyfile{Directory: "/etc/caddy/ocel.d"}).File(), "/etc/caddy/ocel.d/ocel.caddy"; got != want {
		t.Errorf("File() = %q, want %q", got, want)
	}
}
