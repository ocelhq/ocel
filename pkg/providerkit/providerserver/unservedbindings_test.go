package providerserver_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/providerkit/provider"
	"github.com/ocelhq/ocel/pkg/providerkit/providerserver"
)

const boxVendor = provider.Vendor("vps")

func proxied(kind provider.BindingType) bool {
	return naming.Proxied(provider.WireBindingType(kind))
}

func reachableResources() []provider.Resource {
	return []provider.Resource{
		{Name: "database--main", Declared: "database--main", Type: provider.BindingPostgres},
		{Name: "bucket--uploads", Declared: "bucket--uploads", Type: provider.BindingBucket},
	}
}

func servesBoth() []provider.BindingType {
	return []provider.BindingType{provider.BindingPostgres, provider.BindingBucket}
}

func TestRefuseUnservedProxiedBindings(t *testing.T) {
	t.Parallel()

	t.Run("a provider serving every proxied type its plan reaches is let past", func(t *testing.T) {
		t.Parallel()

		if err := providerserver.RefuseUnservedProxiedBindings("aws", servesBoth(), proxied,
			reachableResources(), nil); err != nil {
			t.Fatalf("RefuseUnservedProxiedBindings = %v, want nil", err)
		}
	})

	t.Run("a provider serving no primitive at all refuses by resource, type and vendor", func(t *testing.T) {
		t.Parallel()

		err := providerserver.RefuseUnservedProxiedBindings(boxVendor, nil, proxied,
			reachableResources(), nil)

		var missing *providerserver.UnservedProxiedBindingError
		if !errors.As(err, &missing) {
			t.Fatalf("RefuseUnservedProxiedBindings = %v, want an *UnservedProxiedBindingError", err)
		}
		for _, want := range []string{"bucket--uploads", string(provider.BindingBucket), string(boxVendor)} {
			if !strings.Contains(missing.Error(), want) {
				t.Errorf("Error() = %q, missing %q", missing.Error(), want)
			}
		}
	})

	t.Run("an app is refused for a binding it is granted, not only for one this deploy stands up", func(t *testing.T) {
		t.Parallel()

		grants := []provider.Binding{{Name: "uploads", Resource: "bucket--uploads", Type: provider.BindingBucket}}

		var missing *providerserver.UnservedProxiedBindingError
		if err := providerserver.RefuseUnservedProxiedBindings(boxVendor, nil, proxied, nil, grants); !errors.As(err, &missing) {
			t.Fatalf("RefuseUnservedProxiedBindings = %v, want an *UnservedProxiedBindingError", err)
		}
		if missing.Resource != "bucket--uploads" {
			t.Errorf("Resource = %q, want the name the app declared it under", missing.Resource)
		}
	})

	t.Run("postgres goes direct, so a provider that serves none is still let past", func(t *testing.T) {
		t.Parallel()

		if err := providerserver.RefuseUnservedProxiedBindings(boxVendor, nil, proxied,
			reachableResources()[:1], nil); err != nil {
			t.Fatalf("RefuseUnservedProxiedBindings = %v, want postgres to reach its provider directly", err)
		}
	})

	t.Run("a provider that proxies nothing at all reaches every type directly", func(t *testing.T) {
		t.Parallel()

		proxied := func(provider.BindingType) bool { return false }
		if err := providerserver.RefuseUnservedProxiedBindings(boxVendor, nil, proxied, reachableResources(), nil); err != nil {
			t.Fatalf("RefuseUnservedProxiedBindings = %v, want nothing refused where nothing is proxied", err)
		}
	})
}
