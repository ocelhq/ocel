package vps

import (
	"bytes"
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/ocelhq/ocel/pkg/configdoc"
	"github.com/ocelhq/ocel/pkg/providerkit"
	"github.com/ocelhq/ocel/platform/vps/provider/host"
	"github.com/ocelhq/ocel/platform/vps/provider/proxy/manual"
	"github.com/ocelhq/ocel/platform/vps/provider/session"
)

type Options struct {
	SSH          Target            `json:"ssh" doc:"The machine to deploy onto: a Host alias from ssh_config, or the destination spelled out."`
	DeployKey    string            `json:"deployKey,omitempty" doc:"Path to the public key the ocel-deploy login accepts; defaults to the bootstrapping login's keys."`
	Certificates map[string]string `json:"certificates,omitempty" doc:"Certificates to serve a hostname with, keyed by hostname, valued by the path to the certificate on the machine."`
	Proxy        *Proxy            `json:"proxy,omitempty" doc:"What fronts this machine on ports 80 and 443. Leave it out and ocel runs its own proxy; name the one the machine already runs to deploy behind it."`
}

type Proxy struct {
	Traefik *Traefik `json:"traefik,omitempty" doc:"A Traefik the machine already runs, reading ocel's routes from a directory its file provider watches."`
	Caddy   *Caddy   `json:"caddy,omitempty" doc:"A Caddy the machine already runs, importing ocel's site blocks from a directory."`
	Manual  *Manual  `json:"manual,omitempty" doc:"A proxy you route to ocel yourself; ocel writes nothing to it."`
}

type Traefik struct {
	Preset          string       `json:"preset,omitempty" enum:"coolify,dokploy" doc:"The host tool whose Traefik this is. It fills in every other field, and a field written beside it overrides."`
	Directory       string       `json:"directory" unless:"preset" doc:"The directory Traefik's file provider watches, where ocel writes its routers."`
	Resolver        string       `json:"resolver" unless:"preset" doc:"The certificate resolver every hostname router ocel writes names."`
	PreviewResolver string       `json:"previewResolver,omitempty" doc:"A resolver that can issue the preview base's wildcard over DNS-01. Set, previews share one wildcard certificate; left out, each preview hostname gets its own from resolver."`
	Entrypoints     *Entrypoints `json:"entrypoints,omitempty" doc:"The entry points ocel's routers attach to."`
	Network         string       `json:"network,omitempty" doc:"A docker network ocel's switchboard joins, so Traefik reaches it by name. Not with port."`
	Port            int          `json:"port,omitempty" doc:"The loopback port ocel's switchboard is published on for Traefik to reach; 8480 when left out. Not with network."`
}

type Entrypoints struct {
	HTTP  string `json:"http,omitempty" doc:"The entry point ocel's http-to-https redirect routers attach to; web when left out."`
	HTTPS string `json:"https,omitempty" doc:"The entry point ocel's hostname routers attach to; websecure when left out."`
}

type Caddy struct {
	Preset    string `json:"preset,omitempty" enum:"coolify" doc:"The host tool whose Caddy this is. It fills in every other field, and a field written beside it overrides."`
	Directory string `json:"directory" unless:"preset" doc:"The directory the running Caddy imports site blocks from."`
	Container string `json:"container,omitempty" doc:"The container Caddy runs in; left out, Caddy runs as the systemd caddy.service."`
	Config    string `json:"config,omitempty" doc:"The config file caddy reload names inside the container; /etc/caddy/Caddyfile when left out."`
	Network   string `json:"network,omitempty" doc:"A docker network ocel's switchboard joins, so Caddy reaches it by name. Not with port."`
	Port      int    `json:"port,omitempty" doc:"The loopback port ocel's switchboard is published on for Caddy to reach; 8480 when left out. Not with network."`
}

type Manual struct {
	Port    int    `json:"port,omitempty" doc:"The loopback port your proxy forwards to ocel's switchboard on; 8480 when left out."`
	Network string `json:"network,omitempty" doc:"A docker network ocel's switchboard also joins, so a proxy on it reaches the switchboard by name even after it is recreated."`
}

const proxyManual = "manual"

func (Proxy) Shorthands() []string { return []string{proxyManual} }

func (p *Proxy) UnmarshalJSON(data []byte) error {
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) > 0 && trimmed[0] == '"' {
		var shorthand string
		if err := json.Unmarshal(trimmed, &shorthand); err != nil {
			return err
		}
		if !slices.Contains(p.Shorthands(), shorthand) {
			return fmt.Errorf(`option "proxy" names %q, which is none of %s`, shorthand, strings.Join(p.Shorthands(), ", "))
		}
		*p = Proxy{Manual: &Manual{}}
		return nil
	}
	var keyed map[string]json.RawMessage
	if err := json.Unmarshal(trimmed, &keyed); err != nil {
		return fmt.Errorf(`option "proxy": %w`, err)
	}
	if len(keyed) != 1 {
		return fmt.Errorf(`option "proxy" holds exactly one of the keys %s`, strings.Join(configdoc.KeysOf(Proxy{}), ", "))
	}
	type wire Proxy
	var decoded wire
	if err := json.Unmarshal(trimmed, &decoded); err != nil {
		return fmt.Errorf(`option "proxy": %w`, err)
	}
	*p = Proxy(decoded)
	if _, held := keyed["traefik"]; held && p.Traefik == nil {
		p.Traefik = &Traefik{}
	}
	if _, held := keyed["caddy"]; held && p.Caddy == nil {
		p.Caddy = &Caddy{}
	}
	if _, held := keyed[proxyManual]; held && p.Manual == nil {
		p.Manual = &Manual{}
	}
	return nil
}

func (p *Proxy) front() host.Front {
	switch {
	case p == nil:
		return host.Front{}
	case p.Traefik != nil:
		filled := p.Traefik.written().Filled()
		return host.Front{Traefik: &filled}
	case p.Caddy != nil:
		filled := p.Caddy.written().Filled()
		return host.Front{Caddy: &filled}
	case p.Manual != nil:
		return host.Front{Manual: &host.ManualFront{Port: cmp.Or(p.Manual.Port, manual.DefaultPort), Network: p.Manual.Network}}
	default:
		return host.Front{}
	}
}

func (t *Traefik) written() host.TraefikFront {
	var entrypoints host.Entrypoints
	if t.Entrypoints != nil {
		entrypoints = host.Entrypoints{HTTP: t.Entrypoints.HTTP, HTTPS: t.Entrypoints.HTTPS}
	}
	return host.TraefikFront{
		Preset:          t.Preset,
		Directory:       t.Directory,
		Resolver:        t.Resolver,
		PreviewResolver: t.PreviewResolver,
		Entrypoints:     entrypoints,
		Network:         t.Network,
		Port:            t.Port,
	}
}

func (c *Caddy) written() host.CaddyFront {
	return host.CaddyFront{
		Preset:    c.Preset,
		Directory: c.Directory,
		Container: c.Container,
		Config:    c.Config,
		Network:   c.Network,
		Port:      c.Port,
	}
}

func unsupported(spelled string) error {
	return fmt.Errorf("option `\"proxy\": %s` is not supported yet; route to ocel yourself with `\"proxy\": \"manual\"`", spelled)
}

func (p *Proxy) usable(certificates map[string]string) error {
	if p == nil {
		return nil
	}
	if len(certificates) > 0 {
		return providerkit.Refuse(providerkit.CodeInvalid,
			"options %q and %q are both set: your proxy serves certificates; configure them there", "certificates", "proxy")
	}
	switch {
	case p.Traefik != nil:
		if err := p.Traefik.usable(); err != nil {
			return err
		}
		return providerkit.Refuse(providerkit.CodeInvalid, "%s", unsupported(`{ "traefik": … }`))
	case p.Caddy != nil:
		if err := p.Caddy.usable(); err != nil {
			return err
		}
		return providerkit.Refuse(providerkit.CodeInvalid, "%s", unsupported(`{ "caddy": … }`))
	}
	return reaching("proxy.manual", p.Manual.Network, p.Manual.Port)
}

func (t *Traefik) usable() error {
	const at = "proxy.traefik"
	if err := presetKnown(at, t.Preset, host.TraefikPresets()); err != nil {
		return err
	}
	filled := t.written().Filled()
	if err := needs(at, "directory", filled.Directory); err != nil {
		return err
	}
	if err := needs(at, "resolver", filled.Resolver); err != nil {
		return err
	}
	return reachedOnce(at, t.Preset, t.Network, filled.Network, t.Port)
}

func (c *Caddy) usable() error {
	const at = "proxy.caddy"
	if err := presetKnown(at, c.Preset, host.CaddyPresets()); err != nil {
		return err
	}
	filled := c.written().Filled()
	if err := needs(at, "directory", filled.Directory); err != nil {
		return err
	}
	return reachedOnce(at, c.Preset, c.Network, filled.Network, c.Port)
}

func presetKnown(at, preset string, known []string) error {
	if preset == "" || slices.Contains(known, preset) {
		return nil
	}
	quoted := make([]string, 0, len(known))
	for _, name := range known {
		quoted = append(quoted, strconv.Quote(name))
	}
	return providerkit.Refuse(providerkit.CodeInvalid,
		"option %q names %q, which is none of %s", at+".preset", preset, strings.Join(quoted, ", "))
}

func needs(at, field, filled string) error {
	if filled != "" {
		return nil
	}
	return providerkit.Refuse(providerkit.CodeInvalid,
		"option %q names no %q: write one, or a %q that fills it", at, field, "preset")
}

func reachedOnce(at, preset, written, network string, port int) error {
	switch {
	case network == "" || port == 0:
		return reaching(at, network, port)
	case written == "":
		return providerkit.Refuse(providerkit.CodeInvalid,
			"option %q sets %q beside the %q %s that preset %q fills: the proxy reaches the switchboard by one of them",
			at, "port", "network", network, preset)
	default:
		return providerkit.Refuse(providerkit.CodeInvalid,
			"option %q sets both %q and %q: the proxy reaches the switchboard by one of them", at, "network", "port")
	}
}

var dockerNetworkName = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.-]*$`)

func reaching(at, network string, port int) error {
	if port < 0 || port > 65535 {
		return providerkit.Refuse(providerkit.CodeInvalid, "option %q names %d, which is outside 1-65535", at+".port", port)
	}
	if network != "" && !dockerNetworkName.MatchString(network) {
		return providerkit.Refuse(providerkit.CodeInvalid,
			"option %q names %q, which is no docker network name: letters, digits, _, . and -, starting with a letter or digit", at+".network", network)
	}
	return nil
}

type Target struct {
	Alias        string `json:"-"`
	Config       string `json:"-"`
	Host         string `json:"host" doc:"The hostname or address to reach the machine at."`
	Port         int    `json:"port,omitempty" doc:"The port sshd listens on. Omit it and ssh's own default stands."`
	User         string `json:"user,omitempty" doc:"The account to log in as. Omit it and ssh resolves the user itself."`
	IdentityFile string `json:"identityFile,omitempty" doc:"The private key to authenticate with, as a path."`
}

func (Target) AlsoAString() {}

func (t Target) session() session.Target {
	return session.Target{
		Alias:        t.Alias,
		Config:       t.Config,
		Host:         t.Host,
		Port:         t.Port,
		User:         t.User,
		IdentityFile: t.IdentityFile,
	}
}

func (t *Target) UnmarshalJSON(data []byte) error {
	trimmed := bytes.TrimSpace(data)
	switch {
	case bytes.Equal(trimmed, []byte("null")):
		return nil
	case trimmed[0] == '"':
		var alias string
		if err := json.Unmarshal(trimmed, &alias); err != nil {
			return err
		}
		*t = Target{Alias: alias}
		return nil
	case trimmed[0] == '{':
		type wire Target
		var decoded wire
		if err := json.Unmarshal(trimmed, &decoded); err != nil {
			return fmt.Errorf(`option "ssh": %w`, err)
		}
		*t = Target(decoded)
		return nil
	default:
		return errors.New(`option "ssh" is either an ssh_config alias or the destination spelled out`)
	}
}

func (p *Provider) Target() Target { return p.options.SSH }

func pins(configured map[string]string) []host.Pin {
	held := make([]host.Pin, 0, len(configured))
	for hostname, path := range configured {
		held = append(held, host.Pin{Hostname: hostname, Path: strings.TrimSuffix(path, "/")})
	}
	slices.SortFunc(held, func(a, b host.Pin) int { return strings.Compare(a.Hostname, b.Hostname) })
	return held
}
