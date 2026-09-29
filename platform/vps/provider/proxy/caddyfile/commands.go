package caddyfile

const (
	hostCaddy = "/usr/bin/caddy"
	service   = "caddy.service"
	systemctl = "/usr/bin/systemctl"
	adapter   = "caddyfile"
)

func ServiceReload() []string { return []string{systemctl, "reload", service} }

func (c Caddyfile) caddy(argv ...string) []string {
	if c.Container == "" {
		return append([]string{hostCaddy}, argv...)
	}
	return append([]string{"docker", "exec", c.Container, "caddy"}, argv...)
}

func (c Caddyfile) adapting() []string {
	adapt := []string{"adapt", "--config", "/dev/stdin", "--adapter", adapter}
	if c.Container == "" {
		return append([]string{hostCaddy}, adapt...)
	}
	return append([]string{"docker", "exec", "-i", c.Container, "caddy"}, adapt...)
}

func (c Caddyfile) Reloading() []string {
	switch {
	case c.Container == "":
		return append([]string{"sudo", "-n"}, ServiceReload()...)
	case c.Preset != "":
		return c.caddy("reload", "--config", c.Config)
	default:
		return c.caddy("reload", "--config", c.Config, "--adapter", adapter)
	}
}

func (c Caddyfile) running() string {
	if c.Container == "" {
		return "your Caddy (" + service + ")"
	}
	return "your Caddy (container " + c.Container + ")"
}
