package front

import (
	"cmp"
	"maps"
	"slices"

	"github.com/ocelhq/ocel/platform/vps/provider/proxy/manual"
)

type hostTool struct {
	name    string
	traefik *Traefik
	caddy   *Caddy
}

var hostTools = map[string]hostTool{
	"coolify": {
		name: "Coolify",
		traefik: &Traefik{
			Directory:          "/data/coolify/proxy/dynamic",
			ContainerDirectory: "/traefik/dynamic",
			Resolver:           "letsencrypt",
			Entrypoints:        &Entrypoints{HTTP: "http", HTTPS: "https"},
			Network:            "coolify",
		},
		caddy: &Caddy{
			Directory:          "/data/coolify/proxy/caddy/dynamic",
			ContainerDirectory: "/dynamic",
			Container:          "coolify-proxy",
			Config:             "/config/caddy/Caddyfile.autosave",
			Network:            "coolify",
		},
	},
	"dokploy": {
		name: "Dokploy",
		traefik: &Traefik{
			Directory:   "/etc/dokploy/traefik/dynamic",
			Resolver:    "letsencrypt",
			Entrypoints: &Entrypoints{HTTP: "web", HTTPS: "websecure"},
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

func runBy(preset, proxy string) string {
	if tool, ok := hostTools[preset]; ok {
		return tool.name + "'s " + proxy
	}
	return "your " + proxy
}

func (t Traefik) Filled() Traefik {
	var filled Traefik
	if tool := hostTools[t.Preset].traefik; tool != nil {
		filled = *tool
	}
	written, preset := t.entrypoints(), filled.entrypoints()
	filled.Preset = t.Preset
	if t.Directory != "" {
		filled.Directory, filled.ContainerDirectory = t.Directory, ""
	}
	filled.ContainerDirectory = cmp.Or(t.ContainerDirectory, filled.ContainerDirectory)
	filled.Resolver = cmp.Or(t.Resolver, filled.Resolver)
	filled.PreviewResolver = cmp.Or(t.PreviewResolver, filled.PreviewResolver)
	filled.Entrypoints = &Entrypoints{
		HTTP:  cmp.Or(written.HTTP, preset.HTTP, traefikHTTP),
		HTTPS: cmp.Or(written.HTTPS, preset.HTTPS, traefikHTTPS),
	}
	filled.Network, filled.Port = reached(t.Network, t.Port, filled.Network)
	return filled
}

func (t Traefik) entrypoints() Entrypoints {
	if t.Entrypoints == nil {
		return Entrypoints{}
	}
	return *t.Entrypoints
}

func (c Caddy) Filled() Caddy {
	var filled Caddy
	if tool := hostTools[c.Preset].caddy; tool != nil {
		filled = *tool
	}
	filled.Preset = c.Preset
	if c.Directory != "" {
		filled.Directory, filled.ContainerDirectory = c.Directory, ""
	}
	filled.ContainerDirectory = cmp.Or(c.ContainerDirectory, filled.ContainerDirectory)
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
