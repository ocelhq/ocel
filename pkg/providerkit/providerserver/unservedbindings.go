package providerserver

import (
	"fmt"
	"slices"

	"github.com/ocelhq/ocel/pkg/providerkit/provider"
)

type UnreachableBindingError struct {
	Resource string
	Type     provider.BindingType
	Vendor   provider.Vendor
}

func (e *UnreachableBindingError) Error() string {
	return fmt.Sprintf(
		"%s is a %s, a type an app reaches through the runtime, and the %s provider serves no %s for it to reach. "+
			"Drop the resource, or deploy it to a provider that serves it",
		e.Resource, e.Type, e.Vendor, e.Type,
	)
}

func RefuseUnreachableBindings(vendor provider.Vendor, serves []provider.BindingType, proxied func(provider.BindingType) bool, resources []provider.Resource, grants []provider.Binding) error {
	for _, resource := range resources {
		if err := reachable(vendor, serves, proxied, resource.Declared, resource.Type); err != nil {
			return err
		}
	}
	for _, binding := range grants {
		if err := reachable(vendor, serves, proxied, grantedResource(binding), binding.Type); err != nil {
			return err
		}
	}
	return nil
}

func reachable(vendor provider.Vendor, serves []provider.BindingType, proxied func(provider.BindingType) bool, resource string, kind provider.BindingType) error {
	if !proxied(kind) || slices.Contains(serves, kind) {
		return nil
	}
	return &UnreachableBindingError{Resource: resource, Type: kind, Vendor: vendor}
}

func grantedResource(binding provider.Binding) string {
	if binding.Resource != "" {
		return binding.Resource
	}
	return binding.Name
}
