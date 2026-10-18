package deploy

import (
	"context"
	"fmt"
	"path/filepath"

	"github.com/ocelhq/ocel/cli/internal/project"
	"github.com/ocelhq/ocel/cli/internal/variables"
	bindingsv1 "github.com/ocelhq/ocel/pkg/proto/common/bindings/v1"
)

const defaultInlinePostgresPort = 5432

var inlineTLSModes = map[string]bindingsv1.PostgresTlsMode{
	"require":     bindingsv1.PostgresTlsMode_POSTGRES_TLS_MODE_REQUIRE,
	"verify-full": bindingsv1.PostgresTlsMode_POSTGRES_TLS_MODE_VERIFY_FULL,
}

func buildInlineBindings(bound []project.Binding, values map[string]string, source string) ([]*bindingsv1.Binding, error) {
	var out []*bindingsv1.Binding
	for _, b := range bound {
		if b.Inline == nil {
			continue
		}
		site := "bindings." + b.Group()
		read := func(variable string) (string, error) {
			value, ok := values[variable]
			if !ok {
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
		out = append(out, binding)
	}
	return out, nil
}

func postgresProperties(p *project.PostgresInline, read func(string) (string, error)) (*bindingsv1.PostgresProperties, error) {
	if p.URL != "" {
		value, err := read(p.URL)
		return &bindingsv1.PostgresProperties{Url: value}, err
	}
	props := &bindingsv1.PostgresProperties{Port: defaultInlinePostgresPort}
	if p.Port != 0 {
		props.Port = int32(p.Port)
	}
	var err error
	if props.Host, err = p.Host.Resolve(read); err != nil {
		return nil, err
	}
	if props.Database, err = p.Database.Resolve(read); err != nil {
		return nil, err
	}
	if props.Username, err = p.Username.Resolve(read); err != nil {
		return nil, err
	}
	if props.Password, err = read(p.Password); err != nil {
		return nil, err
	}
	if p.TLS != nil {
		props.TlsMode = inlineTLSModes[p.TLS.Mode]
		if p.TLS.CA != "" {
			if props.TlsCa, err = read(p.TLS.CA); err != nil {
				return nil, err
			}
		}
	}
	return props, nil
}

func bucketProperties(b *project.BucketInline, read func(string) (string, error)) (*bindingsv1.BucketProperties, error) {
	props := &bindingsv1.BucketProperties{PathStyle: b.PathStyle}
	var err error
	for _, field := range []struct {
		into *string
		from project.Text
	}{
		{&props.Endpoint, b.Endpoint},
		{&props.Region, b.Region},
		{&props.Bucket, b.Bucket},
		{&props.Prefix, b.Prefix},
		{&props.PublicBaseUrl, b.PublicBaseURL},
	} {
		if *field.into, err = field.from.Resolve(read); err != nil {
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

func inlineBindings(ctx context.Context, cfg *project.Project, declarations *variables.Declarations) ([]*bindingsv1.Binding, error) {
	values, err := declarations.ResolveBindingVariables(ctx)
	if err != nil {
		return nil, err
	}
	return buildInlineBindings(cfg.BindingsFor(declarations.Scope().Tier), values, filepath.Base(cfg.Path))
}
