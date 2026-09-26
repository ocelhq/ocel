package host

import (
	"cmp"
	"maps"
	"slices"

	"github.com/ocelhq/ocel/platform/vps/provider/proxy/manual"
)

type hostTool struct {
	name    string
	traefik *TraefikFront
	caddy   *CaddyFront
}

var hostTools = map[string]hostTool{
	"coolify": {
		name: "Coolify",
		traefik: &TraefikFront{
			Directory:   "/data/coolify/proxy/dynamic",
			Resolver:    "letsencrypt",
			Entrypoints: Entrypoints{HTTP: "http", HTTPS: "https"},
			Network:     "coolify",
		},
		caddy: &CaddyFront{
			Directory: "/data/coolify/proxy/caddy/dynamic",
			Container: "coolify-proxy",
			Config:    "/config/caddy/Caddyfile.autosave",
			Network:   "coolify",
		},
	},
	"dokploy": {
		name: "Dokploy",
		traefik: &TraefikFront{
			Directory:   "/etc/dokploy/traefik/dynamic",
			Resolver:    "letsencrypt",
			Entrypoints: Entrypoints{HTTP: "web", HTTPS: "websecure"},
			Network:     "dokploy-network",
		},
	},
}

const (
	traefikHTTP  = "web"
	traefikHTTPS = "websecure"
	caddyConfig  = "/etc/caddy/Caddyfile"
)

func TraefikPresets() []string {
	return presetsOf(func(tool hostTool) bool { return tool.traefik != nil })
}

func CaddyPresets() []string {
	return presetsOf(func(tool hostTool) bool { return tool.caddy != nil })
}

func presetsOf(runs func(hostTool) bool) []string {
	var named []string
	for _, preset := range slices.Sorted(maps.Keys(hostTools)) {
		if runs(hostTools[preset]) {
			named = append(named, preset)
		}
	}
	return named
}

func (t TraefikFront) Filled() TraefikFront {
	var filled TraefikFront
	if tool := hostTools[t.Preset].traefik; tool != nil {
		filled = *tool
	}
	filled.Preset = t.Preset
	filled.Directory = cmp.Or(t.Directory, filled.Directory)
	filled.Resolver = cmp.Or(t.Resolver, filled.Resolver)
	filled.PreviewResolver = cmp.Or(t.PreviewResolver, filled.PreviewResolver)
	filled.Entrypoints.HTTP = cmp.Or(t.Entrypoints.HTTP, filled.Entrypoints.HTTP, traefikHTTP)
	filled.Entrypoints.HTTPS = cmp.Or(t.Entrypoints.HTTPS, filled.Entrypoints.HTTPS, traefikHTTPS)
	filled.Network, filled.Port = reached(t.Network, t.Port, filled.Network)
	return filled
}

func (c CaddyFront) Filled() CaddyFront {
	var filled CaddyFront
	if tool := hostTools[c.Preset].caddy; tool != nil {
		filled = *tool
	}
	filled.Preset = c.Preset
	filled.Directory = cmp.Or(c.Directory, filled.Directory)
	filled.Container = cmp.Or(c.Container, filled.Container)
	filled.Config = cmp.Or(c.Config, filled.Config, caddyConfig)
	filled.Network, filled.Port = reached(c.Network, c.Port, filled.Network)
	return filled
}

func reached(writtenNetwork string, writtenPort int, presetNetwork string) (network string, port int) {
	switch {
	case writtenNetwork != "":
		return writtenNetwork, writtenPort
	case writtenPort != 0:
		return "", writtenPort
	case presetNetwork != "":
		return presetNetwork, 0
	default:
		return "", manual.DefaultPort
	}
}
