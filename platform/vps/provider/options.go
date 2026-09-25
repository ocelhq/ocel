package vps

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
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
	Directory  string `json:"directory" doc:"The directory Traefik's file provider watches, where ocel writes its routers."`
	Resolver   string `json:"resolver" doc:"The certificate resolver ocel's routers ask for certificates."`
	Entrypoint string `json:"entrypoint,omitempty" doc:"The entrypoint that serves https; websecure when left out."`
	Network    string `json:"network,omitempty" doc:"The docker network Traefik reaches ocel's switchboard on."`
}

type Caddy struct {
	Directory string `json:"directory" doc:"The directory the running Caddy imports site blocks from."`
	Container string `json:"container,omitempty" doc:"The container Caddy runs in, when it runs in one."`
}

type Manual struct {
	Port int `json:"port,omitempty" doc:"The loopback port your proxy forwards to ocel's switchboard on; 8480 when left out."`
}

const (
	proxyCoolify = "coolify"
	proxyDokploy = "dokploy"
	proxyManual  = "manual"
)

func (Proxy) Shorthands() []string { return []string{proxyCoolify, proxyDokploy, proxyManual} }

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
		if shorthand != proxyManual {
			return unsupported(strconv.Quote(shorthand))
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
	if _, manual := keyed[proxyManual]; manual && p.Manual == nil {
		p.Manual = &Manual{}
	}
	return nil
}

func (p *Proxy) front() host.Front {
	if p == nil || p.Manual == nil {
		return host.Front{}
	}
	port := p.Manual.Port
	if port == 0 {
		port = manual.DefaultPort
	}
	return host.Front{Manual: &host.ManualFront{Port: port}}
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
		return providerkit.Refuse(providerkit.CodeInvalid, "%s", unsupported(`{ "traefik": … }`))
	case p.Caddy != nil:
		return providerkit.Refuse(providerkit.CodeInvalid, "%s", unsupported(`{ "caddy": … }`))
	}
	if port := p.Manual.Port; port < 0 || port > 65535 {
		return providerkit.Refuse(providerkit.CodeInvalid, "option %q names port %d, which is outside 1-65535", "proxy", port)
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
