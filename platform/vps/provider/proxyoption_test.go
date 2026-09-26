package vps_test

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/providerkit"
	vps "github.com/ocelhq/ocel/platform/vps/provider"
)

func proxied(proxy any) providerkit.Options {
	return providerkit.Options{"ssh": "prod", "proxy": proxy}
}

func TestTheProxyOptionReadsEveryFormItTakes(t *testing.T) {
	t.Parallel()

	for name, tc := range map[string]struct {
		options providerkit.Options
		want    *vps.Proxy
	}{
		"left out, ocel runs its own":        {options: providerkit.Options{"ssh": "prod"}, want: nil},
		"manual as a shorthand":              {options: proxied("manual"), want: &vps.Proxy{Manual: &vps.Manual{}}},
		"manual as an object with no port":   {options: proxied(map[string]any{"manual": map[string]any{}}), want: &vps.Proxy{Manual: &vps.Manual{}}},
		"manual as an object naming a port":  {options: proxied(map[string]any{"manual": map[string]any{"port": 9000}}), want: &vps.Proxy{Manual: &vps.Manual{Port: 9000}}},
		"manual held null inside its object": {options: proxied(map[string]any{"manual": nil}), want: &vps.Proxy{Manual: &vps.Manual{}}},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			provider, err := vps.New(context.Background(), providerkit.Settings{Options: tc.options})
			if err != nil {
				t.Fatalf("New() = %v, want %s accepted", err, name)
			}
			if got := provider.(*vps.Provider).Fronted(); !reflect.DeepEqual(got, tc.want) {
				t.Errorf("Fronted() = %+v, want %+v", got, tc.want)
			}
		})
	}
}

func TestTheProxyOptionRefusesWhatThisOcelDoesNotServe(t *testing.T) {
	t.Parallel()

	for name, tc := range map[string]struct {
		options providerkit.Options
		mention []string
	}{
		"a shorthand it does not list": {
			options: proxied("nginx"),
			mention: []string{`"provider.vps.proxy"`, `must be one of "manual", or`},
		},
		"no key": {
			options: proxied(map[string]any{}),
			mention: []string{`"provider.vps.proxy"`, "exactly one", "traefik, caddy, manual"},
		},
		"two keys": {
			options: proxied(map[string]any{"manual": map[string]any{}, "caddy": map[string]any{"directory": "/etc/caddy"}}),
			mention: []string{`"provider.vps.proxy"`, "exactly one"},
		},
		"a key it does not know": {
			options: proxied(map[string]any{"nginx": map[string]any{}}),
			mention: []string{"provider.vps.proxy.nginx", "traefik, caddy, manual"},
		},
		"a manual port that is not a number": {
			options: proxied(map[string]any{"manual": map[string]any{"port": "8480"}}),
			mention: []string{`"provider.vps.proxy.manual.port" must be a number`},
		},
		"a manual port outside the range": {
			options: proxied(map[string]any{"manual": map[string]any{"port": 70000}}),
			mention: []string{`"proxy.manual.port"`, "70000", "outside 1-65535"},
		},
		"coolify, which runs a proxy and is none": {options: proxied("coolify"), mention: []string{`"provider.vps.proxy"`, `must be one of "manual", or`, "traefik, caddy, manual"}},
		"dokploy, which runs a proxy and is none": {options: proxied("dokploy"), mention: []string{`"provider.vps.proxy"`, `must be one of "manual", or`, "traefik, caddy, manual"}},
		"traefik, not served yet":                 {options: proxied(map[string]any{"traefik": map[string]any{"directory": "/d", "resolver": "le"}}), mention: []string{`"traefik"`, "not supported yet"}},
		"caddy, not served yet":                   {options: proxied(map[string]any{"caddy": map[string]any{"directory": "/d"}}), mention: []string{`"caddy"`, "not supported yet"}},
		"Coolify's Traefik, not served yet":       {options: proxied(map[string]any{"traefik": map[string]any{"preset": "coolify"}}), mention: []string{`"traefik"`, "not supported yet"}},
		"Coolify's Caddy, not served yet":         {options: proxied(map[string]any{"caddy": map[string]any{"preset": "coolify"}}), mention: []string{`"caddy"`, "not supported yet"}},
		"certificates with manual":                {options: providerkit.Options{"ssh": "prod", "proxy": "manual", "certificates": map[string]any{"shop.example.com": "/etc/ocel/certs/shop"}}, mention: []string{"your proxy serves certificates; configure them there"}},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			_, err := vps.New(context.Background(), providerkit.Settings{Options: tc.options})
			if err == nil {
				t.Fatalf("New() with %s = nil, want a refusal", name)
			}
			for _, mention := range tc.mention {
				if !strings.Contains(err.Error(), mention) {
					t.Errorf("New() with %s = %v, want it to mention %s", name, err, mention)
				}
			}
		})
	}
}
