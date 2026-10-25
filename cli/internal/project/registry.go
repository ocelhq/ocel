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
		return nil, errors.New("`password` must name the environment variable holding the registry token, as \"${REGISTRY_TOKEN}\"")
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
		return "", "", errors.New("`server` is required, such as \"ghcr.io/acme\"")
	}
	if strings.Contains(server, "://") {
		return "", "", errors.New("`server` is a registry host and the namespace under it, such as \"ghcr.io/acme\", not a URL: drop the scheme")
	}
	if strings.Contains(server, "@") {
		return "", "", errors.New("`server` contains credentials: write the host and namespace alone, such as \"ghcr.io/acme\", and name the token in `password`")
	}
	segments := strings.Split(server, "/")
	host := segments[0]
	if host == "" {
		return "", "", errors.New("`server` must start with a registry host, such as \"ghcr.io/acme\"")
	}
	for _, segment := range segments[1:] {
		if !naming.IsRepositorySegment(segment) {
			return "", "", errors.New("`server` namespace segments are lowercase letters, digits and single separators, such as \"ghcr.io/acme\"")
		}
	}
	return host, strings.Join(segments[1:], "/"), nil
}
