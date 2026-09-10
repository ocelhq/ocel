package providerkit

import (
	"fmt"
	"slices"
)

type UnreachableBindingError struct {
	Resource string
	Type     BindingType
	Vendor   Vendor
}

func (e *UnreachableBindingError) Error() string {
	return fmt.Sprintf(
		"%s is a %s, a type an app reaches through the runtime, and the %s provider serves no %s for it to reach. "+
			"Drop the resource, or deploy it to a provider that serves it",
		e.Resource, e.Type, e.Vendor, e.Type,
	)
}

func RefuseUnreachableBindings(vendor Vendor, serves []BindingType, proxied func(BindingType) bool, resources []Resource, grants []Binding) error {
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

func reachable(vendor Vendor, serves []BindingType, proxied func(BindingType) bool, resource string, kind BindingType) error {
	if !proxied(kind) || slices.Contains(serves, kind) {
		return nil
	}
	return &UnreachableBindingError{Resource: resource, Type: kind, Vendor: vendor}
}

func grantedResource(binding Binding) string {
	if binding.Resource != "" {
		return binding.Resource
	}
	return binding.Name
}
