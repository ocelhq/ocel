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
		{Name: "database--main", Declared: "database--main", Type: providerkit.LinkPostgres},
		{Name: "bucket--uploads", Declared: "bucket--uploads", Type: providerkit.LinkBucket},
	}
}

func servesBoth() []providerkit.LinkType {
	return []providerkit.LinkType{providerkit.LinkPostgres, providerkit.LinkBucket}
}

func TestRefuseUnreachableLinks(t *testing.T) {
	t.Parallel()

	t.Run("a provider serving every crossing type its plan reaches is let past", func(t *testing.T) {
		t.Parallel()

		if err := providerkit.RefuseUnreachableLinks("aws", servesBoth(), providerkit.CrossesMembrane,
			reachableResources(), nil); err != nil {
			t.Fatalf("RefuseUnreachableLinks = %v, want nil", err)
		}
	})

	t.Run("a provider serving no primitive at all refuses by resource, type and vendor", func(t *testing.T) {
		t.Parallel()

		err := providerkit.RefuseUnreachableLinks(boxVendor, nil, providerkit.CrossesMembrane,
			reachableResources(), nil)

		var missing *providerkit.UnreachableLinkError
		if !errors.As(err, &missing) {
			t.Fatalf("RefuseUnreachableLinks = %v, want an *UnreachableLinkError", err)
		}
		for _, want := range []string{"bucket--uploads", string(providerkit.LinkBucket), string(boxVendor)} {
			if !strings.Contains(missing.Error(), want) {
				t.Errorf("Error() = %q, missing %q", missing.Error(), want)
			}
		}
	})

	t.Run("an app is refused for a link it is granted, not only for one this deploy stands up", func(t *testing.T) {
		t.Parallel()

		grants := []providerkit.Link{{Name: "uploads", Resource: "bucket--uploads", Type: providerkit.LinkBucket}}

		var missing *providerkit.UnreachableLinkError
		if err := providerkit.RefuseUnreachableLinks(boxVendor, nil, providerkit.CrossesMembrane, nil, grants); !errors.As(err, &missing) {
			t.Fatalf("RefuseUnreachableLinks = %v, want an *UnreachableLinkError", err)
		}
		if missing.Resource != "bucket--uploads" {
			t.Errorf("Resource = %q, want the name the app declared it under", missing.Resource)
		}
	})

	t.Run("postgres goes direct, so a provider that serves none is still let past", func(t *testing.T) {
		t.Parallel()

		if err := providerkit.RefuseUnreachableLinks(boxVendor, nil, providerkit.CrossesMembrane,
			reachableResources()[:1], nil); err != nil {
			t.Fatalf("RefuseUnreachableLinks = %v, want postgres to reach its provider directly", err)
		}
	})

	t.Run("a provider that crosses nothing at all reaches every type directly", func(t *testing.T) {
		t.Parallel()

		crosses := func(providerkit.LinkType) bool { return false }
		if err := providerkit.RefuseUnreachableLinks(boxVendor, nil, crosses, reachableResources(), nil); err != nil {
			t.Fatalf("RefuseUnreachableLinks = %v, want nothing refused where nothing crosses", err)
		}
	})
}
