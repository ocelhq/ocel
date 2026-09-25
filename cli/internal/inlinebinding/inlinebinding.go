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

type BucketProbe func(ctx context.Context, props *bindingsv1.BucketProperties, public bool, origins []string) ([]string, error)

type Probes struct {
	Postgres Probe
	Bucket   BucketProbe
}

type Declared struct {
	Postgres map[string]string
	Buckets  map[string]*resourcesv1.BucketConfig
}

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
		if bucket := b.Inline.Bucket; bucket != nil {
			props, err := bucketProperties(bucket, read)
			if err != nil {
				return nil, err
			}
			binding.Properties = &bindingsv1.Binding_Bucket{Bucket: props}
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

func Verify(ctx context.Context, records []Record, declared Declared, probes Probes) ([]string, error) {
	var warnings []string
	for _, r := range records {
		if props := r.Binding.GetPostgres(); props != nil {
			if err := verifyPostgres(ctx, r, props, declared.Postgres[r.Declared], probes.Postgres); err != nil {
				return nil, err
			}
		}
		if props := r.Binding.GetBucket(); props != nil {
			said, err := verifyBucket(ctx, r, props, declared.Buckets[r.Declared], probes.Bucket)
			if err != nil {
				return nil, err
			}
			warnings = append(warnings, said...)
		}
	}
	return warnings, nil
}

func verifyPostgres(ctx context.Context, r Record, props *bindingsv1.PostgresProperties, declared string, probe Probe) error {
	served, err := probe(ctx, props)
	if err != nil {
		return fmt.Errorf("`%s` could not be reached to check it: %s. Ocel checks a database it is bound to rather than provisioning one, so the deploy stops here — check the host, the credentials and that this machine can reach it",
			r.Site, scrub(err.Error(), props))
	}
	if declared == "" {
		return nil
	}
	if major := majorOf(served); major != declaredMajor(declared) {
		return fmt.Errorf("%s declares postgres %s, and `%s` serves postgres %s: an app written against one major version is not promised the other. Bind a postgres %s server, or declare version %s",
			r.Declared, declared, r.Site, major, declared, major)
	}
	return nil
}

func verifyBucket(ctx context.Context, r Record, props *bindingsv1.BucketProperties, declared *resourcesv1.BucketConfig, probe BucketProbe) ([]string, error) {
	if declared.GetPublic() && props.GetPublicBaseUrl() == "" {
		return nil, fmt.Errorf("%s is declared public, and `%s` names no publicBaseUrl its objects are served from: add the address the store serves the bucket publicly on", r.Declared, r.Site)
	}
	said, err := probe(ctx, props, declared.GetPublic(), declared.GetAllowedOrigins())
	if err != nil {
		return nil, fmt.Errorf("`%s` was checked and refused: %w", r.Site, err)
	}
	warnings := make([]string, 0, len(said))
	for _, warning := range said {
		warnings = append(warnings, fmt.Sprintf("`%s`: %s", r.Site, warning))
	}
	return warnings, nil
}

func bucketProperties(b *projectconfig.BucketInline, read func(string) (string, error)) (*bindingsv1.BucketProperties, error) {
	text := func(v projectconfig.Value) (string, error) {
		if v.Variable == "" {
			return v.Literal, nil
		}
		return read(v.Variable)
	}
	props := &bindingsv1.BucketProperties{PathStyle: b.PathStyle}
	var err error
	for _, field := range []struct {
		into *string
		from projectconfig.Value
	}{
		{&props.Endpoint, b.Endpoint},
		{&props.Region, b.Region},
		{&props.Bucket, b.Bucket},
		{&props.Prefix, b.Prefix},
		{&props.PublicBaseUrl, b.PublicBaseURL},
	} {
		if *field.into, err = text(field.from); err != nil {
			return nil, err
		}
	}
	if props.AccessKeyId, err = read(b.AccessKeyID); err != nil {
		return nil, err
	}
	if props.SecretAccessKey, err = read(b.SecretAccessKey); err != nil {
		return nil, err
	}
	return props, nil
}

func majorOf(serverVersionNum int) string {
	if serverVersionNum >= 100000 {
		return strconv.Itoa(serverVersionNum / 10000)
	}
	return strconv.Itoa(serverVersionNum/10000) + "." + strconv.Itoa(serverVersionNum/100%100)
}

func declaredMajor(declared string) string {
	parts := strings.Split(strings.TrimSpace(declared), ".")
	major, err := strconv.Atoi(parts[0])
	if err != nil {
		return ""
	}
	if major >= 10 || len(parts) < 2 {
		return strconv.Itoa(major)
	}
	minor, err := strconv.Atoi(parts[1])
	if err != nil {
		return ""
	}
	return strconv.Itoa(major) + "." + strconv.Itoa(minor)
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
