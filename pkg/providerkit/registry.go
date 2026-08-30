package providerkit

import "context"

type RegistryTarget struct {
	Server    string
	Namespace string
	Username  string
	Password  string
}

type ImageRegistry interface {
	ImageRegistry(ctx context.Context, repositories []string) (RegistryTarget, error)
}
