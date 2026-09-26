package vps_test

import (
	"context"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/providerkit/provider"
	vps "github.com/ocelhq/ocel/platform/vps/provider"
	"github.com/ocelhq/ocel/platform/vps/provider/host"
)

func frontFrom(t *testing.T, proxy any) host.Front {
	t.Helper()
	decoded, err := provider.Decode[vps.Options](vps.Vendor, proxied(proxy))
	if err != nil {
		t.Fatalf("Decode(%v) = %v", proxy, err)
	}
	return vps.FrontOf(decoded.Proxy)
}

func TestEveryPresetFillsInHowItsHostToolRunsItsProxy(t *testing.T) {
	t.Parallel()

	for name, tc := range map[string]struct {
		proxy any
		want  host.Front
	}{
		"Coolify's Traefik": {
			proxy: map[string]any{"traefik": map[string]any{"preset": "coolify"}},
			want: host.Front{Traefik: &host.TraefikFront{
				Preset: "coolify", Directory: "/data/coolify/proxy/dynamic", Resolver: "letsencrypt",
				Entrypoints: host.Entrypoints{HTTP: "http", HTTPS: "https"}, Network: "coolify",
			}},
		},
		"Dokploy's Traefik": {
			proxy: map[string]any{"traefik": map[string]any{"preset": "dokploy"}},
			want: host.Front{Traefik: &host.TraefikFront{
				Preset: "dokploy", Directory: "/etc/dokploy/traefik/dynamic", Resolver: "letsencrypt",
				Entrypoints: host.Entrypoints{HTTP: "web", HTTPS: "websecure"}, Network: "dokploy-network",
			}},
		},
		"Coolify's Caddy": {
			proxy: map[string]any{"caddy": map[string]any{"preset": "coolify"}},
			want: host.Front{Caddy: &host.CaddyFront{
				Preset: "coolify", Directory: "/data/coolify/proxy/caddy/dynamic", Container: "coolify-proxy",
				Config: "/config/caddy/Caddyfile.autosave", Network: "coolify",
			}},
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			if got := frontFrom(t, tc.proxy); !reflect.DeepEqual(got, tc.want) {
				t.Errorf("the front for %v = %+v, want %+v", tc.proxy, described(got), described(tc.want))
			}
		})
	}
}

func TestAFieldWrittenBesideAPresetOverridesItAndTheRestStand(t *testing.T) {
	t.Parallel()

	for name, tc := range map[string]struct {
		proxy any
		want  host.Front
	}{
		"Coolify's Traefik with a DNS resolver of its own": {
			proxy: map[string]any{"traefik": map[string]any{"preset": "coolify", "resolver": "le-dns", "previewResolver": "cloudflare"}},
			want: host.Front{Traefik: &host.TraefikFront{
				Preset: "coolify", Directory: "/data/coolify/proxy/dynamic", Resolver: "le-dns", PreviewResolver: "cloudflare",
				Entrypoints: host.Entrypoints{HTTP: "http", HTTPS: "https"}, Network: "coolify",
			}},
		},
		"Dokploy's Traefik with one entry point renamed": {
			proxy: map[string]any{"traefik": map[string]any{"preset": "dokploy", "entrypoints": map[string]any{"https": "secure"}}},
			want: host.Front{Traefik: &host.TraefikFront{
				Preset: "dokploy", Directory: "/etc/dokploy/traefik/dynamic", Resolver: "letsencrypt",
				Entrypoints: host.Entrypoints{HTTP: "web", HTTPS: "secure"}, Network: "dokploy-network",
			}},
		},
		"Coolify's Traefik reaching the switchboard on a port in place of its network": {
			proxy: map[string]any{"traefik": map[string]any{"preset": "coolify", "port": 9000}},
			want: host.Front{Traefik: &host.TraefikFront{
				Preset: "coolify", Directory: "/data/coolify/proxy/dynamic", Resolver: "letsencrypt",
				Entrypoints: host.Entrypoints{HTTP: "http", HTTPS: "https"}, Port: 9000,
			}},
		},
		"Coolify's Caddy reaching the switchboard on a port in place of its network": {
			proxy: map[string]any{"caddy": map[string]any{"preset": "coolify", "port": 9000}},
			want: host.Front{Caddy: &host.CaddyFront{
				Preset: "coolify", Directory: "/data/coolify/proxy/caddy/dynamic", Container: "coolify-proxy",
				Config: "/config/caddy/Caddyfile.autosave", Port: 9000,
			}},
		},
		"Coolify's Caddy in a container of another name": {
			proxy: map[string]any{"caddy": map[string]any{"preset": "coolify", "container": "edge"}},
			want: host.Front{Caddy: &host.CaddyFront{
				Preset: "coolify", Directory: "/data/coolify/proxy/caddy/dynamic", Container: "edge",
				Config: "/config/caddy/Caddyfile.autosave", Network: "coolify",
			}},
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			if got := frontFrom(t, tc.proxy); !reflect.DeepEqual(got, tc.want) {
				t.Errorf("the front for %v = %+v, want %+v", tc.proxy, described(got), described(tc.want))
			}
		})
	}
}

func TestAProxySpelledOutTakesTheDefaultsItLeavesOut(t *testing.T) {
	t.Parallel()

	for name, tc := range map[string]struct {
		proxy any
		want  host.Front
	}{
		"a Traefik on the host": {
			proxy: map[string]any{"traefik": map[string]any{"directory": "/etc/traefik/dynamic", "resolver": "letsencrypt"}},
			want: host.Front{Traefik: &host.TraefikFront{
				Directory: "/etc/traefik/dynamic", Resolver: "letsencrypt",
				Entrypoints: host.Entrypoints{HTTP: "web", HTTPS: "websecure"}, Port: 8480,
			}},
		},
		"a Traefik on a network": {
			proxy: map[string]any{"traefik": map[string]any{"directory": "/etc/traefik/dynamic", "resolver": "letsencrypt", "network": "traefik"}},
			want: host.Front{Traefik: &host.TraefikFront{
				Directory: "/etc/traefik/dynamic", Resolver: "letsencrypt",
				Entrypoints: host.Entrypoints{HTTP: "web", HTTPS: "websecure"}, Network: "traefik",
			}},
		},
		"a systemd Caddy": {
			proxy: map[string]any{"caddy": map[string]any{"directory": "/etc/caddy/ocel.d"}},
			want:  host.Front{Caddy: &host.CaddyFront{Directory: "/etc/caddy/ocel.d", Config: "/etc/caddy/Caddyfile", Port: 8480}},
		},
		"a Caddy in a container on a network": {
			proxy: map[string]any{"caddy": map[string]any{"directory": "/etc/caddy/ocel.d", "container": "caddy", "network": "web"}},
			want:  host.Front{Caddy: &host.CaddyFront{Directory: "/etc/caddy/ocel.d", Container: "caddy", Config: "/etc/caddy/Caddyfile", Network: "web"}},
		},
		"a proxy routed by hand on a network": {
			proxy: map[string]any{"manual": map[string]any{"network": "coolify"}},
			want:  host.Front{Manual: &host.ManualFront{Port: 8480, Network: "coolify"}},
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			if got := frontFrom(t, tc.proxy); !reflect.DeepEqual(got, tc.want) {
				t.Errorf("the front for %v = %+v, want %+v", tc.proxy, described(got), described(tc.want))
			}
		})
	}
}

func TestAProxyMissingWhatItNeedsIsRefusedNamingTheField(t *testing.T) {
	t.Parallel()

	for name, tc := range map[string]struct {
		proxy   any
		mention []string
	}{
		"a Traefik with nothing": {
			proxy:   map[string]any{"traefik": map[string]any{}},
			mention: []string{`"proxy.traefik"`, `"directory"`, `"preset"`},
		},
		"a Traefik held null inside its object": {
			proxy:   map[string]any{"traefik": nil},
			mention: []string{`"proxy.traefik"`, `"directory"`, `"preset"`},
		},
		"a Caddy held null inside its object": {
			proxy:   map[string]any{"caddy": nil},
			mention: []string{`"proxy.caddy"`, `"directory"`, `"preset"`},
		},
		"a Traefik with no resolver": {
			proxy:   map[string]any{"traefik": map[string]any{"directory": "/etc/traefik/dynamic"}},
			mention: []string{`"proxy.traefik"`, `"resolver"`, `"preset"`},
		},
		"a Caddy with nothing": {
			proxy:   map[string]any{"caddy": map[string]any{"container": "caddy"}},
			mention: []string{`"proxy.caddy"`, `"directory"`, `"preset"`},
		},
		"a Traefik preset no host tool has": {
			proxy:   map[string]any{"traefik": map[string]any{"preset": "caprover"}},
			mention: []string{`"proxy.traefik.preset"`, `"caprover"`, `"coolify", "dokploy"`},
		},
		"a Caddy preset for a tool that runs Traefik": {
			proxy:   map[string]any{"caddy": map[string]any{"preset": "dokploy"}},
			mention: []string{`"proxy.caddy.preset"`, `"dokploy"`, `"coolify"`},
		},
		"a Traefik on a network and a port": {
			proxy:   map[string]any{"traefik": map[string]any{"directory": "/d", "resolver": "le", "network": "traefik", "port": 9000}},
			mention: []string{`"proxy.traefik"`, `"network"`, `"port"`},
		},
		"a Caddy on a network and a port": {
			proxy:   map[string]any{"caddy": map[string]any{"directory": "/d", "network": "web", "port": 9000}},
			mention: []string{`"proxy.caddy"`, `"network"`, `"port"`},
		},
		"a Traefik preset with a network and a port both written beside it": {
			proxy:   map[string]any{"traefik": map[string]any{"preset": "coolify", "network": "coolify", "port": 9000}},
			mention: []string{`"proxy.traefik"`, `"network"`, `"port"`},
		},
		"a Caddy preset with a network and a port both written beside it": {
			proxy:   map[string]any{"caddy": map[string]any{"preset": "coolify", "network": "web", "port": 9000}},
			mention: []string{`"proxy.caddy"`, `"network"`, `"port"`},
		},
		"a Traefik port outside the range": {
			proxy:   map[string]any{"traefik": map[string]any{"directory": "/d", "resolver": "le", "port": 70000}},
			mention: []string{`"proxy.traefik.port"`, "70000", "outside 1-65535"},
		},
		"a network docker would not name": {
			proxy:   map[string]any{"manual": map[string]any{"network": "coolify; rm -rf /"}},
			mention: []string{`"proxy.manual.network"`, `"coolify; rm -rf /"`, "docker network"},
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			_, err := vps.New(context.Background(), provider.Settings{Options: proxied(tc.proxy)})
			if err == nil {
				t.Fatalf("New() with %v = nil, want a refusal", tc.proxy)
			}
			for _, mention := range tc.mention {
				if !strings.Contains(err.Error(), mention) {
					t.Errorf("New() with %v = %v, want it to mention %s", tc.proxy, err, mention)
				}
			}
			if strings.Contains(err.Error(), "not supported yet") {
				t.Errorf("New() with %v = %v, want what is wrong with it named before what this ocel does not serve", tc.proxy, err)
			}
		})
	}
}

func TestEveryPresetTheSchemaOffersIsOneTheProviderFills(t *testing.T) {
	t.Parallel()

	for kind, tc := range map[string]struct {
		option any
		fills  []string
	}{
		"traefik": {option: vps.Traefik{}, fills: host.TraefikPresets()},
		"caddy":   {option: vps.Caddy{}, fills: host.CaddyPresets()},
	} {
		field, _ := reflect.TypeOf(tc.option).FieldByName("Preset")
		if offered := strings.Split(field.Tag.Get("enum"), ","); !slices.Equal(offered, tc.fills) {
			t.Errorf("the schema offers %s presets %v, and the provider fills %v", kind, offered, tc.fills)
		}
	}
}

func described(front host.Front) any {
	switch {
	case front.Traefik != nil:
		return *front.Traefik
	case front.Caddy != nil:
		return *front.Caddy
	case front.Manual != nil:
		return *front.Manual
	default:
		return "ocel's own proxy"
	}
}
