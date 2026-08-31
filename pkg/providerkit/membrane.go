package providerkit

import (
	"fmt"
	"slices"
)

type UnreachableLinkError struct {
	Resource string
	Type     LinkType
	Vendor   Vendor
}

func (e *UnreachableLinkError) Error() string {
	return fmt.Sprintf(
		"%s is a %s, a type an app reaches through the membrane, and the %s provider serves no %s for it to reach. "+
			"Drop the resource, or deploy it to a provider that serves it",
		e.Resource, e.Type, e.Vendor, e.Type,
	)
}

func RefuseUnreachableLinks(vendor Vendor, serves []LinkType, crosses func(LinkType) bool, resources []Resource, grants []Link) error {
	for _, resource := range resources {
		if err := reachable(vendor, serves, crosses, resource.Declared, resource.Type); err != nil {
			return err
		}
	}
	for _, link := range grants {
		if err := reachable(vendor, serves, crosses, grantedResource(link), link.Type); err != nil {
			return err
		}
	}
	return nil
}

func reachable(vendor Vendor, serves []LinkType, crosses func(LinkType) bool, resource string, kind LinkType) error {
	if !crosses(kind) || slices.Contains(serves, kind) {
		return nil
	}
	return &UnreachableLinkError{Resource: resource, Type: kind, Vendor: vendor}
}

func grantedResource(link Link) string {
	if link.Resource != "" {
		return link.Resource
	}
	return link.Name
}
