package project

import (
	"errors"
	"strings"

	"github.com/ocelhq/ocel/pkg/configdoc"
	"github.com/ocelhq/ocel/pkg/naming"
)

type Registry struct {
	Server    string
	Namespace string
	Username  string
	Password  string
}

func normalizeRegistry(raw *configdoc.RegistryConfig) (*Registry, error) {
	if raw == nil {
		return nil, nil
	}
	server, namespace, err := normalizeRegistryServer(strings.TrimSpace(raw.Server))
	if err != nil {
		return nil, err
	}
	variable, ok := configdoc.SecretVariable(raw.Password)
	if !ok {
		return nil, errors.New("`password` is the environment variable containing the registry password or token, written as \"${REGISTRY_TOKEN}\", and a push authenticates, so there is no anonymous form to fall back to")
	}
	return &Registry{
		Server:    server,
		Namespace: namespace,
		Username:  strings.TrimSpace(raw.Username),
		Password:  variable,
	}, nil
}

func normalizeRegistryServer(server string) (string, string, error) {
	if server == "" {
		return "", "", errors.New("`server` is the only field naming where images land, so a registry without one would push wherever docker defaults to")
	}
	if strings.Contains(server, "://") {
		return "", "", errors.New("`server` is a registry host and the namespace under it, such as \"ghcr.io/acme\", not a URL: drop the scheme")
	}
	if strings.Contains(server, "@") {
		return "", "", errors.New("`server` contains credentials, and a registry password belongs in the environment `password` names, never in the config: write the host and namespace alone, such as \"ghcr.io/acme\"")
	}
	segments := strings.Split(server, "/")
	host := segments[0]
	if host == "" {
		return "", "", errors.New("`server` starts at a registry host, such as \"ghcr.io/acme\", and this one starts at a path separator")
	}
	for _, segment := range segments[1:] {
		if !naming.IsRepositorySegment(segment) {
			return "", "", errors.New("`server` names a host and the namespace an image sits under, such as \"ghcr.io/acme\", and a namespace segment is lowercase letters, digits and single separators")
		}
	}
	return host, strings.Join(segments[1:], "/"), nil
}
