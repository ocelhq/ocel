package inlinebinding

import (
	"context"
	"fmt"
	"net/url"
	"strconv"
	"strings"

	"github.com/ocelhq/ocel/cli/internal/projectconfig"
	resourcesv1 "github.com/ocelhq/ocel/pkg/proto/app/resources/v1"
	bindingsv1 "github.com/ocelhq/ocel/pkg/proto/common/bindings/v1"
)

const defaultPostgresPort = 5432

type Record struct {
	Declared string
	Site     string
	Type     resourcesv1.ResourceType
	Binding  *bindingsv1.Binding
}

type Probe func(ctx context.Context, props *bindingsv1.PostgresProperties) (int, error)

func Build(bound []projectconfig.TierBinding, values map[string]string, source string) ([]Record, error) {
	var out []Record
	for _, b := range bound {
		if b.Inline == nil {
			continue
		}
		site := "bindings." + b.Group()
		read := func(variable string) (string, error) {
			value, held := values[variable]
			if !held {
				return "", fmt.Errorf("%s, which `%s` reads, has no value here", variable, site)
			}
			return value, nil
		}
		binding := &bindingsv1.Binding{Name: b.RecordName(), Source: source}
		if p := b.Inline.Postgres; p != nil {
			props, err := postgresProperties(p, read)
			if err != nil {
				return nil, err
			}
			binding.Properties = &bindingsv1.Binding_Postgres{Postgres: props}
		}
		out = append(out, Record{Declared: b.Name, Site: site, Type: b.Type, Binding: binding})
	}
	return out, nil
}

func postgresProperties(p *projectconfig.PostgresInline, read func(string) (string, error)) (*bindingsv1.PostgresProperties, error) {
	if p.URL != "" {
		value, err := read(p.URL)
		return &bindingsv1.PostgresProperties{Url: value}, err
	}
	text := func(v projectconfig.Value) (string, error) {
		if v.Variable == "" {
			return v.Literal, nil
		}
		return read(v.Variable)
	}
	props := &bindingsv1.PostgresProperties{Port: defaultPostgresPort}
	if p.Port != 0 {
		props.Port = int32(p.Port)
	}
	var err error
	if props.Host, err = text(p.Host); err != nil {
		return nil, err
	}
	if props.Database, err = text(p.Database); err != nil {
		return nil, err
	}
	if props.Username, err = text(p.Username); err != nil {
		return nil, err
	}
	if props.Password, err = read(p.Password); err != nil {
		return nil, err
	}
	if p.TLS != nil {
		props.TlsMode = p.TLS.Mode
		if p.TLS.CA != "" {
			if props.TlsCa, err = read(p.TLS.CA); err != nil {
				return nil, err
			}
		}
	}
	return props, nil
}

func Verify(ctx context.Context, records []Record, versions map[string]string, probe Probe) error {
	for _, r := range records {
		props := r.Binding.GetPostgres()
		if props == nil {
			continue
		}
		served, err := probe(ctx, props)
		if err != nil {
			return fmt.Errorf("`%s` could not be reached to check it: %s. Ocel checks a database it is bound to rather than provisioning one, so the deploy stops here — check the host, the credentials and that this machine can reach it",
				r.Site, scrub(err.Error(), props))
		}
		declared := versions[r.Declared]
		if declared == "" {
			continue
		}
		if major := majorOf(served); major != declaredMajor(declared) {
			return fmt.Errorf("%s declares postgres %s, and `%s` serves postgres %d: an app written against one major version is not promised the other. Bind a postgres %s server, or declare version %d",
				r.Declared, declared, r.Site, major, declared, major)
		}
	}
	return nil
}

func majorOf(serverVersionNum int) int {
	if serverVersionNum >= 100000 {
		return serverVersionNum / 10000
	}
	return serverVersionNum / 100
}

func declaredMajor(declared string) int {
	head, _, _ := strings.Cut(strings.TrimSpace(declared), ".")
	major, err := strconv.Atoi(head)
	if err != nil {
		return -1
	}
	return major
}

func scrub(message string, props *bindingsv1.PostgresProperties) string {
	var secrets []string
	if props.GetPassword() != "" {
		secrets = append(secrets, props.GetPassword())
	}
	if props.GetUrl() != "" {
		secrets = append(secrets, props.GetUrl())
		if parsed, err := url.Parse(props.GetUrl()); err == nil && parsed.User != nil {
			if password, set := parsed.User.Password(); set && password != "" {
				secrets = append(secrets, password, url.QueryEscape(password), url.PathEscape(password))
			}
		}
	}
	for _, secret := range secrets {
		message = strings.ReplaceAll(message, secret, "[redacted]")
	}
	return message
}
