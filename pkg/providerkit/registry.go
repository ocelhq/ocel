package providerkit

import (
	"context"
	"fmt"
	"strings"
)

type RegistryTarget struct {
	Server    string
	Namespace string
	Username  string
	Password  string
}

func (t RegistryTarget) String() string {
	return fmt.Sprintf("registry %s namespace %q username %q password [redacted]", t.Server, t.Namespace, t.Username)
}

func (t RegistryTarget) GoString() string { return t.String() }

func (t RegistryTarget) Coordinate(repository, tag string) string {
	parts := []string{t.Server}
	if t.Namespace != "" {
		parts = append(parts, strings.Trim(t.Namespace, "/"))
	}
	return strings.Join(append(parts, repository), "/") + ":" + tag
}

type ImageRegistry interface {
	ImageRegistry(ctx context.Context, repositories []string) (RegistryTarget, error)
}
