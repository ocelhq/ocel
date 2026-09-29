package caddyfile

import "slices"

const (
	hostCaddy           = "/usr/bin/caddy"
	uncreatableStepPath = "STEPPATH=/dev/null/step"
	service             = "caddy.service"
	systemctl           = "/usr/bin/systemctl"
	adapter             = "caddyfile"
	stdin               = "/dev/stdin"
)

func ServiceReload() []string { return []string{systemctl, "reload", service} }

func (c Caddyfile) caddy(argv ...string) []string {
	if c.Container == "" {
		return append([]string{"env", uncreatableStepPath, hostCaddy}, argv...)
	}
	exec := []string{"docker", "exec"}
	if slices.Contains(argv, stdin) {
		exec = append(exec, "-i")
	}
	return slices.Concat(exec, []string{c.Container, "caddy"}, argv)
}

func (c Caddyfile) adapting() []string {
	return c.caddy("adapt", "--config", stdin, "--adapter", adapter)
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

func (c Caddyfile) named() string {
	if c.Container == "" {
		return "your Caddy (" + service + ")"
	}
	return "your Caddy (container " + c.Container + ")"
}
