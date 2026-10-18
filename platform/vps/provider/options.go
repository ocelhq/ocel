package vps

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/ocelhq/ocel/platform/vps/provider/boxapi"
	"github.com/ocelhq/ocel/platform/vps/provider/front"
	"github.com/ocelhq/ocel/platform/vps/provider/ssh"
)

type Options struct {
	SSH          Target            `json:"ssh" doc:"The machine to deploy onto: a Host alias from ssh_config, or the destination spelled out."`
	DeployKey    string            `json:"deployKey,omitempty" doc:"Path to the public key the ocel-deploy login accepts; defaults to the bootstrapping login's keys."`
	Certificates map[string]string `json:"certificates,omitempty" doc:"Certificates to serve a hostname with, keyed by hostname, valued by the path to the certificate on the machine."`
	Proxy        *front.Proxy      `json:"proxy,omitempty" doc:"What fronts this machine on ports 80 and 443. Leave it out and ocel runs its own proxy; name the one the machine already runs to deploy behind it."`

	PerHostnamePreviewCertificates bool `json:"perHostnamePreviewCertificates,omitempty" doc:"Deploy previews even though the proxy orders a publicly trusted certificate for each preview hostname, which Certificate Transparency logs publish within minutes. Left out, such a preview deploy is refused: set proxy.traefik.previewResolver, pin a wildcard certificate for the preview base, or front the box with an edge that shields it with an origin certificate instead."`
}

type Target struct {
	Alias        string `json:"-"`
	Config       string `json:"-"`
	Host         string `json:"host" doc:"The hostname or address to reach the machine at."`
	Port         int    `json:"port,omitempty" doc:"The port sshd listens on. Omit it and ssh's own default applies."`
	User         string `json:"user,omitempty" doc:"The account to log in as. Omit it and ssh resolves the user itself."`
	IdentityFile string `json:"identityFile,omitempty" doc:"The private key to authenticate with, as a path."`
}

func (Target) AlsoAString() {}

func (t Target) session() ssh.Target {
	return ssh.Target{
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
		type fields Target
		var decoded fields
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

func pins(configured map[string]string) []boxapi.Pin {
	sorted := make([]boxapi.Pin, 0, len(configured))
	for hostname, path := range configured {
		sorted = append(sorted, boxapi.Pin{Hostname: hostname, Path: strings.TrimSuffix(path, "/")})
	}
	slices.SortFunc(sorted, func(a, b boxapi.Pin) int { return strings.Compare(a.Hostname, b.Hostname) })
	return sorted
}
