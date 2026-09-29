package traefik

import (
	"bytes"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"path/filepath"
	"slices"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/platform/vps/provider/proxy"
)

type dynamic struct {
	HTTP routes     `yaml:"http"`
	TLS  *tlsConfig `yaml:"tls,omitempty"`
}

type tlsConfig struct {
	Certificates []servedCertificate   `yaml:"certificates,omitempty"`
	Options      map[string]tlsOptions `yaml:"options,omitempty"`
}

type servedCertificate struct {
	CertFile string `yaml:"certFile"`
	KeyFile  string `yaml:"keyFile"`
}

type tlsOptions struct {
	SniStrict  bool       `yaml:"sniStrict"`
	ClientAuth clientAuth `yaml:"clientAuth"`
}

type clientAuth struct {
	CAFiles        []string `yaml:"caFiles"`
	ClientAuthType string   `yaml:"clientAuthType"`
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
	Options      string   `yaml:"options,omitempty"`
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

const (
	templateOpen      = "{{"
	requireClientCert = "RequireAndVerifyClientCert"
)

func (t Traefik) render(spec proxy.Spec) ([]byte, error) {
	config := dynamic{HTTP: routes{
		Routers:     map[string]router{},
		Middlewares: map[string]middleware{redirect: {RedirectScheme: redirectScheme{Scheme: "https", Permanent: true}}},
		Services:    map[string]upstream{service: {LoadBalancer: loadBalancer{Servers: []server{{URL: t.upstream()}}}}},
	}}
	shields, err := t.shieldsOf(spec)
	if err != nil {
		return nil, err
	}
	config.TLS = shields
	for _, hostname := range spec.Hostnames {
		name := routerName(hostname)
		config.HTTP.Routers[name], config.HTTP.Routers[name+httpSuffix] = t.routersFor(hostname, spec)
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

func (t Traefik) shieldsOf(spec proxy.Spec) (*tlsConfig, error) {
	config := tlsConfig{Options: map[string]tlsOptions{}}
	for _, hostname := range spec.Hostnames {
		shield, shielded := spec.ShieldOf(hostname)
		if !shielded {
			continue
		}
		name := shieldName(shield)
		if _, rendered := config.Options[name]; rendered {
			continue
		}
		trusted, err := trustedCertificates(hostname, shield)
		if err != nil {
			return nil, err
		}
		config.Options[name] = tlsOptions{SniStrict: true, ClientAuth: clientAuth{CAFiles: trusted, ClientAuthType: requireClientCert}}
		if shield.OriginCertificate.Certificate != "" {
			bundle := filepath.Join(t.containerDirectory(), shield.OriginFileName())
			config.Certificates = append(config.Certificates, servedCertificate{CertFile: bundle, KeyFile: bundle})
		}
	}
	if len(config.Options) == 0 {
		return nil, nil
	}
	slices.SortFunc(config.Certificates, func(a, b servedCertificate) int { return strings.Compare(a.CertFile, b.CertFile) })
	return &config, nil
}

func trustedCertificates(hostname string, shield proxy.Shield) ([]string, error) {
	if len(shield.ClientCertificates) == 0 {
		return nil, fmt.Errorf("%s is shielded by no client certificate", hostname)
	}
	trusted := make([]string, 0, len(shield.ClientCertificates))
	for _, certificate := range shield.ClientCertificates {
		block, _ := pem.Decode([]byte(certificate))
		if block == nil || block.Type != "CERTIFICATE" {
			return nil, fmt.Errorf("a client certificate %s is shielded by is no PEM certificate", hostname)
		}
		if _, err := x509.ParseCertificate(block.Bytes); err != nil {
			return nil, fmt.Errorf("a client certificate %s is shielded by: %w", hostname, err)
		}
		trusted = append(trusted, string(pem.EncodeToMemory(block)))
	}
	return trusted, nil
}

func shieldName(shield proxy.Shield) string {
	return "ocel-shield-" + strings.TrimPrefix(routerName(strings.ReplaceAll(strings.TrimSpace(strings.ToLower(shield.Hostname)), "*", "_")), "ocel-")
}

func (t Traefik) originFiles(spec proxy.Spec) []proxy.OriginFile {
	var files []proxy.OriginFile
	for _, hostname := range spec.Hostnames {
		shield, shielded := spec.ShieldOf(hostname)
		if !shielded || shield.OriginCertificate.Certificate == "" {
			continue
		}
		path := filepath.Join(t.directory(), shield.OriginFileName())
		if slices.ContainsFunc(files, func(file proxy.OriginFile) bool { return file.Path == path }) {
			continue
		}
		files = append(files, proxy.OriginFile{Path: path, Bundle: shield.OriginCertificate.Bundle()})
	}
	slices.SortFunc(files, func(a, b proxy.OriginFile) int { return strings.Compare(a.Path, b.Path) })
	return files
}

func (t Traefik) validate(rendered []byte) error {
	if bytes.Contains(rendered, []byte(templateOpen)) {
		return refusal.Refuse(refusal.CodeInvalid,
			"the routes ocel would write to %s contain %s, which your Traefik reads as a template before it parses the file; take it out of the proxy option",
			t.file(), templateOpen)
	}
	if err := decodeStrictly(rendered); err != nil {
		return refusal.Refuse(refusal.CodeInvalid, "the routes ocel would write to %s do not read back: %v", t.file(), err)
	}
	return nil
}

func (t Traefik) routersFor(hostname string, spec proxy.Spec) (router, router) {
	rule := "Host(`" + hostname + "`)"
	return router{
		Rule:        rule,
		EntryPoints: []string{t.HTTPS},
		Service:     service,
		Priority:    priority,
		TLS:         t.tlsFor(hostname, spec),
	}, router{
		Rule:        rule,
		EntryPoints: []string{t.HTTP},
		Middlewares: []string{redirect},
		Service:     noop,
		Priority:    priority,
	}
}

func decodeStrictly(rendered []byte) error {
	decoder := yaml.NewDecoder(bytes.NewReader(rendered))
	decoder.KnownFields(true)
	var read dynamic
	return decoder.Decode(&read)
}

func (t Traefik) tlsFor(hostname string, spec proxy.Spec) *tls {
	shield, shielded := spec.ShieldOf(hostname)
	if !shielded {
		return t.orderedTLS(hostname, spec.PreviewBase)
	}
	if shield.OriginCertificate.Certificate != "" {
		return &tls{Options: shieldName(shield)}
	}
	ordered := t.orderedTLS(hostname, spec.PreviewBase)
	ordered.Options = shieldName(shield)
	return ordered
}

func (t Traefik) orderedTLS(hostname, base string) *tls {
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
