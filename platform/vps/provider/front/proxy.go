package front

import (
	"bytes"
	"cmp"
	"encoding/json"
	"fmt"
	"reflect"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/ocelhq/ocel/pkg/configdoc"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/platform/vps/provider/proxy/manual"
)

type Proxy struct {
	Traefik *Traefik `json:"traefik,omitempty" doc:"A Traefik the machine already runs, reading ocel's routes from a directory its file provider watches."`
	Caddy   *Caddy   `json:"caddy,omitempty" doc:"A Caddy the machine already runs, importing ocel's site blocks from a directory."`
	Manual  *Manual  `json:"manual,omitempty" doc:"A proxy you route to ocel yourself; ocel writes nothing to it."`
}

type Traefik struct {
	Preset             string       `json:"preset,omitempty" enum:"coolify,dokploy" doc:"The host tool whose Traefik this is. It fills in every other field, and a field written beside it overrides."`
	Directory          string       `json:"directory" unless:"preset" doc:"The directory Traefik's file provider watches, where ocel writes its routers."`
	ContainerDirectory string       `json:"containerDirectory,omitempty" doc:"Where directory is mounted in the container your Traefik runs in, as its providers.file.directory names it; ocel.yml names the origin certificates it places in directory by this path. Left out, directory itself."`
	Resolver           string       `json:"resolver" unless:"preset" doc:"The certificate resolver every hostname router ocel writes names."`
	PreviewResolver    string       `json:"previewResolver,omitempty" doc:"A resolver that can issue the preview base's wildcard over DNS-01. Set, previews share one wildcard certificate; left out, each preview hostname gets its own from resolver."`
	Entrypoints        *Entrypoints `json:"entrypoints,omitempty" doc:"The entry points ocel's routers attach to."`
	Network            string       `json:"network,omitempty" doc:"A docker network ocel's switchboard joins, so Traefik reaches it by name. Not with port."`
	Port               int          `json:"port,omitempty" doc:"The loopback port ocel's switchboard is published on for Traefik to reach, in place of the network a preset fills; 8480 when left out. Not with network."`
}

type Entrypoints struct {
	HTTP  string `json:"http,omitempty" doc:"The entry point ocel's http-to-https redirect routers attach to; web when left out."`
	HTTPS string `json:"https,omitempty" doc:"The entry point ocel's hostname routers attach to; websecure when left out."`
}

type Caddy struct {
	Preset             string `json:"preset,omitempty" enum:"coolify" doc:"The host tool whose Caddy this is. It fills in every other field, and a field written beside it overrides."`
	Directory          string `json:"directory" unless:"preset" doc:"The directory the running Caddy imports site blocks from."`
	ContainerDirectory string `json:"containerDirectory,omitempty" doc:"Where directory is mounted in the container Caddy runs in; ocel.caddy names the origin certificates it places in directory by this path. Left out, directory itself."`
	Container          string `json:"container,omitempty" doc:"The container Caddy runs in; left out, Caddy runs as the systemd caddy.service."`
	Config             string `json:"config,omitempty" doc:"The config file caddy reload names inside the container; /etc/caddy/Caddyfile when left out."`
	Network            string `json:"network,omitempty" doc:"A docker network ocel's switchboard joins, so Caddy reaches it by name. Not with port."`
	Port               int    `json:"port,omitempty" doc:"The loopback port ocel's switchboard is published on for Caddy to reach, in place of the network a preset fills; 8480 when left out. Not with network."`
}

type Manual struct {
	Port    int    `json:"port,omitempty" doc:"The loopback port your proxy forwards to ocel's switchboard on; 8480 when left out."`
	Network string `json:"network,omitempty" doc:"A docker network ocel's switchboard also joins, so a proxy on it reaches the switchboard by name even after it is recreated."`
}

const (
	keyTraefik = "traefik"
	keyCaddy   = "caddy"
	keyManual  = "manual"
)

func (Proxy) Shorthands() []string { return []string{keyManual} }

func (o *Proxy) UnmarshalJSON(data []byte) error {
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) > 0 && trimmed[0] == '"' {
		var shorthand string
		if err := json.Unmarshal(trimmed, &shorthand); err != nil {
			return err
		}
		if !slices.Contains(o.Shorthands(), shorthand) {
			return fmt.Errorf(`option "proxy" names %q, which is none of %s`, shorthand, strings.Join(o.Shorthands(), ", "))
		}
		*o = Proxy{Manual: &Manual{}}
		return nil
	}
	var keyed map[string]json.RawMessage
	if err := json.Unmarshal(trimmed, &keyed); err != nil {
		return fmt.Errorf(`option "proxy": %w`, err)
	}
	if len(keyed) != 1 {
		return fmt.Errorf(`option "proxy" must set exactly one of the keys %s`, strings.Join(configdoc.KeysOf(Proxy{}), ", "))
	}
	type fields Proxy
	var decoded fields
	if err := json.Unmarshal(trimmed, &decoded); err != nil {
		return fmt.Errorf(`option "proxy": %w`, err)
	}
	*o = Proxy(decoded)
	if _, set := keyed[keyTraefik]; set && o.Traefik == nil {
		o.Traefik = &Traefik{}
	}
	if _, set := keyed[keyCaddy]; set && o.Caddy == nil {
		o.Caddy = &Caddy{}
	}
	if _, set := keyed[keyManual]; set && o.Manual == nil {
		o.Manual = &Manual{}
	}
	return nil
}

func (o *Proxy) Filled() Proxy {
	switch {
	case o == nil:
		return Proxy{}
	case o.Traefik != nil:
		filled := o.Traefik.Filled()
		return Proxy{Traefik: &filled}
	case o.Caddy != nil:
		filled := o.Caddy.Filled()
		return Proxy{Caddy: &filled}
	case o.Manual != nil:
		return Proxy{Manual: &Manual{Port: cmp.Or(o.Manual.Port, manual.DefaultPort), Network: o.Manual.Network}}
	default:
		return Proxy{}
	}
}

func (o Proxy) Front() Front {
	switch {
	case o.Traefik != nil:
		return o.Traefik
	case o.Caddy != nil:
		return o.Caddy
	case o.Manual != nil:
		return o.Manual
	default:
		return Builtin{}
	}
}

func (o Proxy) Adopted() bool { return o != Proxy{} }

func (o Proxy) Recorded() *Proxy {
	if !o.Adopted() {
		return nil
	}
	return &o
}

func (o Proxy) Same(other Proxy) bool { return reflect.DeepEqual(o.unlabelled(), other.unlabelled()) }

func (o Proxy) unlabelled() Proxy {
	if o.Traefik != nil {
		unset := *o.Traefik
		unset.Preset = ""
		o.Traefik = &unset
	}
	if o.Caddy != nil {
		unset := *o.Caddy
		unset.Preset = ""
		o.Caddy = &unset
	}
	return o
}

func (o *Proxy) RefuseUnusable(certificates map[string]string) error {
	if o == nil {
		return nil
	}
	if len(certificates) > 0 {
		return refusal.Refuse(refusal.CodeInvalid,
			"options %q and %q are both set: your proxy serves certificates; configure them there", "certificates", "proxy")
	}
	switch {
	case o.Traefik != nil:
		filled := o.Traefik.Filled()
		return refuseUnadoptable("proxy."+keyTraefik, o.Traefik.Preset, TraefikPresets(), o.Traefik.Network, o.Traefik.Port,
			required{"directory", filled.Directory}, required{"resolver", filled.Resolver})
	case o.Caddy != nil:
		filled := o.Caddy.Filled()
		return refuseUnadoptable("proxy."+keyCaddy, o.Caddy.Preset, CaddyPresets(), o.Caddy.Network, o.Caddy.Port,
			required{"directory", filled.Directory})
	}
	return refuseUnreached("proxy."+keyManual, o.Manual.Network, o.Manual.Port)
}

type required struct{ field, filled string }

func refuseUnadoptable(at, preset string, presets []string, writtenNetwork string, writtenPort int, fields ...required) error {
	if err := refuseUnknownPreset(at, preset, presets); err != nil {
		return err
	}
	for _, needed := range fields {
		if err := refuseUnfilled(at, needed.field, needed.filled); err != nil {
			return err
		}
	}
	return refuseReachedTwice(at, writtenNetwork, writtenPort)
}

func refuseUnknownPreset(at, preset string, known []string) error {
	if preset == "" || slices.Contains(known, preset) {
		return nil
	}
	quoted := make([]string, 0, len(known))
	for _, name := range known {
		quoted = append(quoted, strconv.Quote(name))
	}
	return refusal.Refuse(refusal.CodeInvalid,
		"option %q names %q, which is none of %s", at+".preset", preset, strings.Join(quoted, ", "))
}

func refuseUnfilled(at, field, filled string) error {
	if filled != "" {
		return nil
	}
	return refusal.Refuse(refusal.CodeInvalid,
		"option %q names no %q: write one, or a %q that fills it", at, field, "preset")
}

func refuseReachedTwice(at, writtenNetwork string, writtenPort int) error {
	if writtenNetwork != "" && writtenPort != 0 {
		return refusal.Refuse(refusal.CodeInvalid,
			"option %q sets both %q and %q: the proxy reaches the switchboard by one of them", at, "network", "port")
	}
	return refuseUnreached(at, writtenNetwork, writtenPort)
}

var dockerNetworkName = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.-]*$`)

func refuseUnreached(at, network string, port int) error {
	if port < 0 || port > 65535 {
		return refusal.Refuse(refusal.CodeInvalid, "option %q names %d, which is outside 1-65535", at+".port", port)
	}
	if network != "" && !dockerNetworkName.MatchString(network) {
		return refusal.Refuse(refusal.CodeInvalid,
			"option %q names %q, which is no docker network name: letters, digits, _, . and -, starting with a letter or digit", at+".network", network)
	}
	return nil
}
