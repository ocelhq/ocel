package traefik

import (
	"bytes"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/platform/vps/provider/proxy"
)

type dynamic struct {
	HTTP routes `yaml:"http"`
}

type routes struct {
	Routers     map[string]router     `yaml:"routers,omitempty"`
	Middlewares map[string]middleware `yaml:"middlewares"`
	Services    map[string]upstream   `yaml:"services"`
}

type router struct {
	Rule        string   `yaml:"rule"`
	EntryPoints []string `yaml:"entryPoints"`
	Middlewares []string `yaml:"middlewares,omitempty"`
	Service     string   `yaml:"service"`
	Priority    int      `yaml:"priority"`
	TLS         *tls     `yaml:"tls,omitempty"`
}

type tls struct {
	CertResolver string   `yaml:"certResolver,omitempty"`
	Domains      []domain `yaml:"domains,omitempty"`
}

type domain struct {
	Main string `yaml:"main"`
}

type middleware struct {
	RedirectScheme redirectScheme `yaml:"redirectScheme"`
}

type redirectScheme struct {
	Scheme    string `yaml:"scheme"`
	Permanent bool   `yaml:"permanent"`
}

type upstream struct {
	LoadBalancer loadBalancer `yaml:"loadBalancer"`
}

type loadBalancer struct {
	Servers []server `yaml:"servers"`
}

type server struct {
	URL string `yaml:"url"`
}

const templateOpen = "{{"

func (t Traefik) render(spec proxy.Spec) ([]byte, error) {
	config := dynamic{HTTP: routes{
		Routers:     map[string]router{},
		Middlewares: map[string]middleware{redirect: {RedirectScheme: redirectScheme{Scheme: "https", Permanent: true}}},
		Services:    map[string]upstream{service: {LoadBalancer: loadBalancer{Servers: []server{{URL: t.upstream()}}}}},
	}}
	for _, hostname := range spec.Hostnames {
		name, rule := RouterName(hostname), "Host(`"+hostname+"`)"
		config.HTTP.Routers[name] = router{
			Rule:        rule,
			EntryPoints: []string{t.HTTPS},
			Service:     service,
			Priority:    Priority,
			TLS:         t.certified(hostname, spec.PreviewBase),
		}
		config.HTTP.Routers[name+httpSuffix] = router{
			Rule:        rule,
			EntryPoints: []string{t.HTTP},
			Middlewares: []string{redirect},
			Service:     noop,
			Priority:    Priority,
		}
	}
	var written bytes.Buffer
	encoder := yaml.NewEncoder(&written)
	encoder.SetIndent(2)
	if err := encoder.Encode(config); err != nil {
		return nil, err
	}
	if err := encoder.Close(); err != nil {
		return nil, err
	}
	return written.Bytes(), nil
}

func (t Traefik) validate(rendered []byte) error {
	if bytes.Contains(rendered, []byte(templateOpen)) {
		return refusal.Refuse(refusal.CodeInvalid,
			"the routes ocel would write to %s contain %s, which your Traefik reads as a template before it parses the file; take it out of the proxy option",
			t.file(), templateOpen)
	}
	if err := strictly(rendered); err != nil {
		return refusal.Refuse(refusal.CodeInvalid, "the routes ocel would write to %s do not read back: %v", t.file(), err)
	}
	return nil
}

func strictly(rendered []byte) error {
	decoder := yaml.NewDecoder(bytes.NewReader(rendered))
	decoder.KnownFields(true)
	var read dynamic
	return decoder.Decode(&read)
}

func (t Traefik) certified(hostname, base string) *tls {
	if t.PreviewResolver == "" || base == "" {
		return &tls{CertResolver: t.Resolver}
	}
	switch {
	case hostname == edge.ProbeHostname(edge.PreviewWildcard(base)):
		return &tls{CertResolver: t.PreviewResolver, Domains: []domain{{Main: edge.PreviewWildcard(base)}}}
	case previewed(hostname, base):
		return &tls{}
	default:
		return &tls{CertResolver: t.Resolver}
	}
}

func previewed(hostname, base string) bool {
	label, under := strings.CutSuffix(strings.ToLower(hostname), "."+strings.ToLower(base))
	return under && label != "" && !strings.Contains(label, ".")
}
