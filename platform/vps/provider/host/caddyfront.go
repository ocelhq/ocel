package host

import (
	"strings"

	"github.com/ocelhq/ocel/platform/vps/provider/proxy/caddyfile"
)

func (f Front) caddyfile(box caddyfile.Box) caddyfile.Caddyfile {
	return caddyfile.Caddyfile{
		Box:       box,
		Preset:    f.Caddy.Preset,
		Directory: f.Caddy.Directory,
		Container: f.Caddy.Container,
		Config:    f.Caddy.Config,
		Network:   f.Caddy.Network,
		Port:      f.Caddy.Port,
	}
}

func (f Front) reloadGrant() []Item {
	if f.Caddy == nil || f.Caddy.Container != "" {
		return nil
	}
	return []Item{{Kind: KindFile, Name: sudoersCaddyReload, Mode: 0o440, Owner: rootOwner, Content: caddyReloadSudoers(),
		Note: "sudo for reloading your Caddy"}}
}

func caddyReloadSudoers() []byte {
	return []byte(deployUser + " ALL=(root) NOPASSWD: " + strings.Join(caddyfile.ServiceReload(), " ") + "\n")
}

func (f Front) caddyReloadCommand() string {
	reload := words(f.caddyfile(frontBox{}).Reloading())
	if f.Caddy.Container == "" {
		return "if systemctl is-active --quiet caddy.service; then " + reload + "; fi"
	}
	return "if [ \"$(docker inspect --type container --format '{{.State.Running}}' " + quoted(f.Caddy.Container) + " 2>/dev/null)\" = true ]; then " + reload + "; fi"
}
