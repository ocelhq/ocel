package providerkit_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/providerkit"
)

const boxVendor = providerkit.Vendor("vps")

func reachableResources() []providerkit.Resource {
	return []providerkit.Resource{
		{Name: "database--main", Declared: "database--main", Type: providerkit.BindingPostgres},
		{Name: "bucket--uploads", Declared: "bucket--uploads", Type: providerkit.BindingBucket},
	}
}

func servesBoth() []providerkit.BindingType {
	return []providerkit.BindingType{providerkit.BindingPostgres, providerkit.BindingBucket}
}

func TestRefuseUnreachableBindings(t *testing.T) {
	t.Parallel()

	t.Run("a provider serving every proxied type its plan reaches is let past", func(t *testing.T) {
		t.Parallel()

		if err := providerkit.RefuseUnreachableBindings("aws", servesBoth(), providerkit.Proxied,
			reachableResources(), nil); err != nil {
			t.Fatalf("RefuseUnreachableBindings = %v, want nil", err)
		}
	})

	t.Run("a provider serving no primitive at all refuses by resource, type and vendor", func(t *testing.T) {
		t.Parallel()

		err := providerkit.RefuseUnreachableBindings(boxVendor, nil, providerkit.Proxied,
			reachableResources(), nil)

		var missing *providerkit.UnreachableBindingError
		if !errors.As(err, &missing) {
			t.Fatalf("RefuseUnreachableBindings = %v, want an *UnreachableBindingError", err)
		}
		for _, want := range []string{"bucket--uploads", string(providerkit.BindingBucket), string(boxVendor)} {
			if !strings.Contains(missing.Error(), want) {
				t.Errorf("Error() = %q, missing %q", missing.Error(), want)
			}
		}
	})

	t.Run("an app is refused for a binding it is granted, not only for one this deploy stands up", func(t *testing.T) {
		t.Parallel()

		grants := []providerkit.Binding{{Name: "uploads", Resource: "bucket--uploads", Type: providerkit.BindingBucket}}

		var missing *providerkit.UnreachableBindingError
		if err := providerkit.RefuseUnreachableBindings(boxVendor, nil, providerkit.Proxied, nil, grants); !errors.As(err, &missing) {
			t.Fatalf("RefuseUnreachableBindings = %v, want an *UnreachableBindingError", err)
		}
		if missing.Resource != "bucket--uploads" {
			t.Errorf("Resource = %q, want the name the app declared it under", missing.Resource)
		}
	})

	t.Run("postgres goes direct, so a provider that serves none is still let past", func(t *testing.T) {
		t.Parallel()

		if err := providerkit.RefuseUnreachableBindings(boxVendor, nil, providerkit.Proxied,
			reachableResources()[:1], nil); err != nil {
			t.Fatalf("RefuseUnreachableBindings = %v, want postgres to reach its provider directly", err)
		}
	})

	t.Run("a provider that proxies nothing at all reaches every type directly", func(t *testing.T) {
		t.Parallel()

		proxied := func(providerkit.BindingType) bool { return false }
		if err := providerkit.RefuseUnreachableBindings(boxVendor, nil, proxied, reachableResources(), nil); err != nil {
			t.Fatalf("RefuseUnreachableBindings = %v, want nothing refused where nothing is proxied", err)
		}
	})
}
